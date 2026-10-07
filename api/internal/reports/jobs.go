package reports

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
)

// The report stage (BE-5.9).
//
// It gathers, renders, and stores. The PDF step is the only interesting part: the
// document is rendered as HTML here and printed by the browser that already lives in
// the Playwright runner image, because the alternative is a headless browser inside
// the process that serves requests (BE-5.9.3).
//
// A worker with no container runtime still produces HTML. That is the degradation
// this platform promises everywhere: a missing capability removes a format, not the
// feature (F-13.6).

// TypeReport is the stage name the pipeline declares.
const TypeReport = jobs.TypeReportBuild

// Gatherer collects what a report contains. Declared as an interface so the stage
// does not reach across five packages itself, and so the assembly happens in main
// where every other dependency is wired.
type Gatherer interface {
	Gather(ctx context.Context, projectID uuid.UUID, windowDays int) (Contents, error)
}

// Printer turns HTML into PDF. Optional: without it a PDF request is refused with a
// reason rather than silently returning HTML labelled as a PDF.
type Printer interface {
	Print(ctx context.Context, projectID uuid.UUID, html []byte) ([]byte, error)
}

// Deps is everything the stage shares.
type Deps struct {
	Reports  *Service
	Gatherer Gatherer
	Printer  Printer
}

// BuildHandler renders one report.
type BuildHandler struct {
	deps Deps
}

func NewBuildHandler(deps Deps) *BuildHandler { return &BuildHandler{deps: deps} }

func (h *BuildHandler) Type() string { return TypeReport }

// IdempotencyKey is the report row. A redelivered task finds the report already
// claimed and stops, so one request produces one document.
func (h *BuildHandler) IdempotencyKey(payload Payload) string {
	return fmt.Sprintf("report:%s", payload.ReportID)
}

// Payload names the report to build. Its own type rather than the shared stage
// payload: a report is not a chain over a project's artifacts, and borrowing that
// shape would mean carrying six fields that are always empty.
type Payload struct {
	ReportID  uuid.UUID `json:"reportId"`
	ProjectID uuid.UUID `json:"projectId"`
}

func (h *BuildHandler) Handle(ctx context.Context, payload Payload, jc jobs.JobContext) error {
	report, err := h.deps.Reports.Get(ctx, payload.ReportID)
	if err != nil {
		return err
	}

	claimed, err := h.deps.Reports.MarkRunning(ctx, report.ID)
	if err != nil {
		return err
	}
	if !claimed {
		// Already running or finished: a redelivered task, and generating the document
		// twice would mean two files for one row.
		jc.Event("This report is already %s, so nothing was regenerated", report.Status)
		return nil
	}

	jc.Event("Gathering the last %d day(s) of results", report.WindowDays)
	contents, err := h.deps.Gatherer.Gather(ctx, report.ProjectID, report.WindowDays)
	if err != nil {
		h.failed(ctx, report, "gather the report contents", err)
		return nil
	}
	jc.Progress(40)

	html, err := Render(contents)
	if err != nil {
		h.failed(ctx, report, "render the report", err)
		return nil
	}
	jc.Progress(60)

	content := html
	if report.Format == FormatPDF {
		if h.deps.Printer == nil {
			// Refused rather than quietly handed back as HTML: a file named .pdf that is
			// not a PDF is worse than a stated missing capability.
			h.deps.Reports.Fail(ctx, report.ID,
				"PDF rendering needs a container runtime, and this worker has none. "+
					"Ask for the HTML report, or run a worker with the Playwright runner image available.")
			jc.Event("PDF rendering is unavailable on this worker")
			return nil
		}

		printed, err := h.deps.Printer.Print(ctx, report.ProjectID, html)
		if err != nil {
			h.failed(ctx, report, "render the PDF", err)
			jc.Event("The PDF could not be rendered: %v", err)
			return nil
		}
		content = printed
	}
	jc.Progress(85)

	stored, err := h.deps.Reports.Store(ctx, report, content)
	if err != nil {
		h.failed(ctx, report, "store the report", err)
		return nil
	}

	jc.Event("Report ready: %s, %d run(s), %d failure(s), %d defect(s), %d bytes",
		strings.ToUpper(string(stored.Format)), contents.Summary.Runs,
		len(contents.Failures), len(contents.Defects), stored.SizeBytes)
	jc.Progress(100)
	return nil
}

// failed marks the report and logs the cause.
//
// Both, because the two audiences differ: the row carries a sentence a user can act
// on, and an internal error's real cause is only ever in the log. Without the log line
// a masked failure would leave nothing anywhere to debug from.
func (h *BuildHandler) failed(ctx context.Context, report Report, what string, cause error) {
	slog.ErrorContext(ctx, "report generation failed",
		"report_id", report.ID, "project_id", report.ProjectID,
		// cause.Error() rather than the value: slog renders a domain error as its
		// struct, which hides the wrapped cause that is the only useful part of an
		// internal error.
		"format", string(report.Format), "step", what, "error", cause.Error())

	h.deps.Reports.Fail(ctx, report.ID, message(cause))
}

// message is the sentence a user reads on a failed report. A domain error already
// carries one written for a person; anything else falls back to the raw text.
func message(err error) string {
	var domain *apierr.Error
	if errors.As(err, &domain) {
		return domain.Message
	}
	return err.Error()
}

// BrowserPrinter renders HTML to PDF in the Playwright runner image.
//
// The browser never sees the network: the document is self-contained, so the
// container runs with no allowlisted host and therefore no interface at all. A report
// that could fetch a remote stylesheet would be a report that could exfiltrate its own
// contents.
type BrowserPrinter struct {
	driver runner.Driver
	runs   RunnerSettings
}

// RunnerSettings is the slice of the runs service the printer needs: which image,
// which limits, which runtime. Declared as an interface so this package does not
// depend on the execution package's shape.
type RunnerSettings interface {
	PlaywrightImage(ctx context.Context, projectID uuid.UUID) (string, error)
	Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error)
	Runtime(ctx context.Context) (runner.Runtime, error)
}

func NewBrowserPrinter(driver runner.Driver, settings RunnerSettings) *BrowserPrinter {
	return &BrowserPrinter{driver: driver, runs: settings}
}

// reportSource is where the HTML is written inside the container, and reportOutput is
// where the base64-encoded PDF comes back.
const (
	reportSource = "report.html"
	reportOutput = ".qavia/report.pdf.b64"
)

// Print renders the document and returns the PDF bytes.
//
// The PDF comes back base64-encoded through the same report channel every runner
// image uses, because that channel is a text stream: the workspace is a tmpfs that
// stops existing when the container does, so a binary file has to leave as text or
// not at all.
func (p *BrowserPrinter) Print(
	ctx context.Context,
	projectID uuid.UUID,
	html []byte,
) ([]byte, error) {
	image, err := p.runs.PlaywrightImage(ctx, projectID)
	if err != nil {
		return nil, err
	}
	limits, err := p.runs.Limits(ctx, projectID)
	if err != nil {
		return nil, err
	}
	runtime, err := p.runs.Runtime(ctx)
	if err != nil {
		return nil, err
	}

	// A print is quick and its own timeout is short: a browser that has not produced a
	// page in two minutes is not going to.
	if limits.Timeout > printTimeout {
		limits.Timeout = printTimeout
	}

	result, err := p.driver.Run(ctx, runner.Spec{
		RunID:     "report-" + uuid.New().String(),
		Image:     image,
		Command:   []string{"qavia-run", "pdf"},
		Workspace: map[string]string{reportSource: string(html)},
		Limits:    limits,
		Runtime:   runtime,

		// No allowlisted host, so no network interface at all.
		Egress:  runner.Egress{},
		Reports: []string{reportOutput},
	}, io.Discard)
	if err != nil {
		return nil, err
	}

	encoded, found := result.Reports[reportOutput]
	if !found {
		return nil, fmt.Errorf("the browser produced no PDF (exit %d): %s",
			result.ExitCode, excerpt(result.Logs))
	}

	decoded, err := base64.StdEncoding.DecodeString(
		strings.Join(strings.Fields(string(encoded)), ""))
	if err != nil {
		return nil, fmt.Errorf("the PDF came back unreadable: %w", err)
	}
	return decoded, nil
}

// printTimeout bounds one print.
const printTimeout = 2 * time.Minute

func excerpt(logs string) string {
	trimmed := strings.TrimSpace(logs)
	if len(trimmed) > 500 {
		return trimmed[len(trimmed)-500:]
	}
	return trimmed
}
