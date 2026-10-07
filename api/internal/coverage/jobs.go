package coverage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/repos"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// The coverage stage (BE-6.6).
//
// It clones, copies the checkout into a container, runs **the repository's own coverage
// command**, and parses **the report that command wrote**. Every part of that sentence
// is the requirement rather than an implementation choice:
//
//   - the repository's command, because a client comparing this number against their CI
//     has to see the same number, and only the same tool produces it;
//   - inside the container, because running a client's test suite on the worker would
//     execute their code — and their dependencies' install scripts — on the host that
//     holds every project's credentials;
//   - the report it wrote, because a summary line scraped from output is a percentage
//     with no per-file detail and no way to check the arithmetic.
//
// A coverage run is a test run, so it inherits the whole execution boundary from phase
// 4: one-shot container, read-only rootfs, no network, cgroup limits, and a wall clock
// the driver enforces (BE-4.4, BE-4.5).

// TypeMeasure is the stage name the pipeline declares.
const TypeMeasure = jobs.TypeCoverageMeasure

// Repos materialises the checkout.
type Repos interface {
	Materialise(ctx context.Context, projectID uuid.UUID, ref string) (repos.Checkout, error)
	DetectedStack(ctx context.Context, projectID uuid.UUID) (repos.Stack, bool, error)
}

// Runs supplies the image, limits, and runtime, so this package does not re-read the
// runner settings it does not own.
type Runs interface {
	ImageFor(ctx context.Context, projectID uuid.UUID, framework testfiles.Framework) (string, error)
	Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error)
	Runtime(ctx context.Context) (runner.Runtime, error)
}

// Files lists the tests this platform generated, so a measurement includes them.
//
// Without this the number would describe the repository as it arrived, which is a
// figure the client's own CI already gives them. The question this platform is asked
// is whether the tests it wrote moved coverage, and that cannot be answered by
// measuring a checkout that does not contain them (BE-6.6).
type Files interface {
	All(ctx context.Context, projectID uuid.UUID, framework testfiles.Framework) ([]testfiles.File, error)
}

// Deps is everything the stage shares.
type Deps struct {
	Coverage *Service
	Repos    Repos
	Runs     Runs
	Files    Files
	Driver   runner.Driver
}

// MeasureHandler runs a project's coverage tool.
type MeasureHandler struct {
	deps Deps
}

func NewMeasureHandler(deps Deps) *MeasureHandler { return &MeasureHandler{deps: deps} }

func (h *MeasureHandler) Type() string { return TypeMeasure }

// Payload names the project to measure.
type Payload struct {
	ProjectID uuid.UUID `json:"projectId"`
	Ref       string    `json:"ref,omitempty"`

	// RequestID is the per-request nonce, for the same reason a sync has one:
	// measuring again is work somebody asks for.
	RequestID uuid.UUID `json:"requestId"`
}

func (h *MeasureHandler) IdempotencyKey(payload Payload) string {
	return MeasureIdempotencyKey(payload)
}

// MeasureIdempotencyKey is exported so the API can declare the type as enqueue-only.
func MeasureIdempotencyKey(payload Payload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("coverage:%s:%s", payload.ProjectID, payload.Ref)
	}
	return fmt.Sprintf("coverage:%s", payload.RequestID)
}

// reportPaths are where the coverage tools this platform reads write their output.
//
// Ordered by specificity: a repository configured to write both lcov and JSON is read
// from the JSON, because that is the one with per-file statement detail.
var reportPaths = []string{
	"coverage/coverage-final.json",
	"coverage/coverage.json",
	"coverage/lcov.info",
	"coverage/cobertura-coverage.xml",
	"coverage.xml",
	"build/reports/jacoco/test/jacocoTestReport.xml",
	"target/site/jacoco/jacoco.xml",
	"coverage.out",
}

func (h *MeasureHandler) Handle(
	ctx context.Context,
	payload Payload,
	jc jobs.JobContext,
) error {
	stack, _, err := h.deps.Repos.DetectedStack(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	if stack.CoverageTool == "" {
		// Refused rather than substituted. A number this platform produced with its own
		// instrumentation would not match the client's CI, which is the only thing the
		// number is for (F-7.14.1).
		return apierr.NoCoverageTool(stack.Inspected)
	}

	checkout, err := h.deps.Repos.Materialise(ctx, payload.ProjectID, payload.Ref)
	if err != nil {
		return err
	}
	defer func() {
		if err := checkout.Close(); err != nil {
			slog.WarnContext(ctx, "remove the workspace",
				"project_id", payload.ProjectID, "error", err)
		}
	}()

	command := coverageCommand(stack)
	jc.Event("Measuring coverage at %s with the repository's own tool: %s",
		shortCommit(checkout.Commit), strings.Join(command, " "))

	jobID := jc.JobID()

	report, raw, err := h.measure(ctx, payload.ProjectID, checkout, stack, command, jc)
	if err != nil {
		reason := message(err)
		if recordErr := h.deps.Coverage.RecordFailure(ctx, payload.ProjectID,
			checkout.Commit, strings.Join(command, " "), reason, &jobID); recordErr != nil {
			slog.WarnContext(ctx, "record the coverage failure",
				"project_id", payload.ProjectID, "error", recordErr)
		}

		// The row carries the failure, so the job succeeded at what it was asked to do:
		// retrying would run a client's whole test suite again to fail the same way.
		jc.Event("Coverage could not be measured: %s", reason)
		return nil
	}

	stored, err := h.deps.Coverage.Store(ctx, StoreInput{
		ProjectID: payload.ProjectID,
		Commit:    checkout.Commit,
		Command:   strings.Join(command, " "),
		Report:    report,
		JobID:     &jobID,
	})
	if err != nil {
		return err
	}

	jc.Event("%s reported %d of %d lines covered (%s) across %d file(s), from %d bytes of %s",
		report.Tool, report.LinesCovered, report.LinesTotal,
		formatRate(stored.LineRate()), len(report.Files), len(raw), report.Tool)

	if branch := stored.BranchRate(); branch >= 0 {
		jc.Event("Branches: %d of %d (%s)",
			report.BranchesCovered, report.BranchesTotal, formatRate(branch))
	} else {
		// Said rather than shown as zero: several tools do not measure branches, and
		// reporting 0% would claim every conditional is untested.
		jc.Event("This tool did not report branch coverage")
	}

	jc.Progress(100)
	return nil
}

// measure runs the tool in a container and returns the parsed report.
func (h *MeasureHandler) measure(
	ctx context.Context,
	projectID uuid.UUID,
	checkout repos.Checkout,
	stack repos.Stack,
	command []string,
	jc jobs.JobContext,
) (Report, []byte, error) {
	framework := imageFramework(stack)

	image, err := h.deps.Runs.ImageFor(ctx, projectID, framework)
	if err != nil {
		return Report{}, nil, err
	}
	limits, err := h.deps.Runs.Limits(ctx, projectID)
	if err != nil {
		return Report{}, nil, err
	}
	runtime, err := h.deps.Runs.Runtime(ctx)
	if err != nil {
		return Report{}, nil, err
	}

	files, err := readCheckout(checkout)
	if err != nil {
		return Report{}, nil, err
	}

	// The platform's own generated tests are laid over the checkout, because the
	// question is whether they moved the number. They win on a path collision: a file
	// this platform wrote is the version it means to measure.
	generated := 0
	if h.deps.Files != nil {
		written, err := h.deps.Files.All(ctx, projectID, unitFramework(stack))
		if err != nil {
			return Report{}, nil, err
		}
		for _, file := range written {
			if file.Content == "" {
				continue
			}
			files[file.Path] = file.Content
			generated++
		}
	}

	jc.Event("Copying %d file(s) into the runner, %d of them generated by this platform",
		len(files), generated)

	result, err := h.deps.Driver.Run(ctx, runner.Spec{
		RunID: "coverage-" + uuid.New().String(),
		Image: image,

		// The image's own coverage entry point, with the repository's command in the
		// environment. The image owns its toolchain setup — the node_modules link in
		// particular — and a caller assembling that itself would be a caller that has to
		// know how each image is built (BE-6.6).
		Command: []string{"qavia-run", "coverage"},
		Env:     map[string]string{"QAVIA_COVERAGE_COMMAND": strings.Join(command, " ")},

		Workspace: files,
		Limits:    limits,
		Runtime:   runtime,

		// No allowlisted host, so the container gets no network interface at all. A test
		// suite that needs the internet to measure its own coverage is a suite this
		// platform will not run (BE-4.4).
		Egress:  runner.Egress{},
		Reports: reportPaths,
	}, io.Discard)
	if err != nil {
		return Report{}, nil, err
	}

	raw, found := firstReport(result.Reports)
	if !found {
		return Report{}, nil, fmt.Errorf(
			"the coverage tool wrote no report this platform can read (exit %d): %s",
			result.ExitCode, tail(result.Logs, 2<<10))
	}

	report, err := Parse(raw)
	if err != nil {
		return Report{}, nil, err
	}

	// A tool that exited non-zero and still wrote a report is the normal case: tests
	// failed, and their coverage is still what it is. Worth saying, because a coverage
	// number from a red suite means something slightly different.
	if result.ExitCode != 0 {
		jc.Event("The suite exited %d, so this coverage is from a run with failures",
			result.ExitCode)
	}

	return report, raw, nil
}

// coverageCommand is what the container runs.
//
// `sh -c`, not `sh -lc`: a login shell re-reads /etc/profile and replaces the PATH the
// image set, which is where the toolchain lives. The image's own PATH is the one that
// finds vitest.
//
// The repository's own test command where it defines one, because that is the command
// its CI runs; otherwise the coverage tool's own conventional invocation. Either way it
// is the project's tool, never this platform's.
func coverageCommand(stack repos.Stack) []string {
	if command := strings.TrimSpace(stack.TestCommand); command != "" {
		if strings.Contains(command, "--coverage") || strings.Contains(command, "cov") {
			return []string{command}
		}
		// A test command that does not ask for coverage gets the flag its framework
		// understands, rather than a different command entirely: the closer this stays to
		// what the project runs, the more the number means.
		switch strings.ToLower(stack.Framework) {
		case "vitest", "jest":
			return []string{command + " --coverage"}
		case "pytest":
			return []string{command + " --cov --cov-report=xml"}
		}
		return []string{command}
	}

	switch strings.ToLower(stack.Framework) {
	case "vitest":
		return []string{"vitest run --coverage"}
	case "jest":
		return []string{"jest --coverage --coverageReporters=json"}
	case "pytest":
		return []string{"python -m pytest --cov --cov-report=xml"}
	case "gotest":
		return []string{"go test ./... -coverprofile=coverage.out"}
	default:
		return []string{"echo 'no coverage command for this stack' >&2; exit 64"}
	}
}

// unitFramework is the framework the platform's own generated tests were written in,
// which is the repository's own: detection read it, and generation used it (BE-6.3).
func unitFramework(stack repos.Stack) testfiles.Framework {
	switch strings.ToLower(stack.Framework) {
	case "vitest":
		return testfiles.FrameworkVitest
	case "jest":
		return testfiles.FrameworkJest
	case "pytest":
		return testfiles.FrameworkPytest
	default:
		return ""
	}
}

// imageFramework picks the runner image for a stack. The Node image covers the
// JavaScript tools; pytest has its own.
func imageFramework(stack repos.Stack) testfiles.Framework {
	switch strings.ToLower(stack.Framework) {
	case "pytest":
		return testfiles.FrameworkPytest
	default:
		return testfiles.FrameworkVitest
	}
}

// maxCheckoutFiles and maxCheckoutBytes bound what is copied into the container.
//
// A coverage run needs the source and the tests, not the repository's history or its
// vendored dependencies. Past these bounds the copy itself becomes the slow part.
const (
	maxCheckoutFiles = 6000
	maxCheckoutBytes = 64 << 20
	maxCopiedFile    = 2 << 20
)

// readCheckout reads the checkout into memory for the container copy.
//
// Copied rather than mounted, for the reason every runner workspace is copied: a
// writable host mount is a hole straight through the isolation, and a test suite is
// exactly the thing that would write through it (BE-4.9.1).
func readCheckout(checkout repos.Checkout) (map[string]string, error) {
	root := checkout.Space.Root()
	files := make(map[string]string, 512)

	var total int64

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // one unreadable entry must not fail the copy
		}
		if entry.IsDir() {
			if skipCopy(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > maxCopiedFile {
			return nil //nolint:nilerr // a file too large to be source is skipped
		}
		if len(files) >= maxCheckoutFiles || total+info.Size() > maxCheckoutBytes {
			return fs.SkipAll
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // a path outside the root is skipped
		}

		handle, openErr := checkout.Space.Open(relative)
		if openErr != nil {
			// Refused by the workspace: a symlink out of the checkout is not copied into
			// the container.
			return nil //nolint:nilerr // see above
		}
		content, readErr := readAll(handle)
		if readErr != nil {
			return nil //nolint:nilerr // an unreadable file is skipped
		}

		files[relative] = content
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the checkout: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the checkout has no files to measure")
	}
	return files, nil
}

// skipCopy names what never needs to reach the container: version control, build
// output, and dependencies the image installs itself.
func skipCopy(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "build", "target", ".venv", "venv",
		"__pycache__", ".next", ".nuxt", ".gradle", ".mvn", ".pytest_cache",
		".mypy_cache", ".turbo", "coverage":
		return true
	}
	return false
}

// firstReport returns the first report the container produced, in the order the paths
// were asked for.
func firstReport(reports map[string][]byte) ([]byte, bool) {
	for _, name := range reportPaths {
		if raw, found := reports[name]; found && len(strings.TrimSpace(string(raw))) > 0 {
			return raw, true
		}
	}
	return nil, false
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	if commit == "" {
		return "an unknown revision"
	}
	return commit
}

func formatRate(rate float64) string {
	if rate < 0 {
		return "not measurable"
	}
	return fmt.Sprintf("%.1f%%", rate*100)
}

func tail(text string, size int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= size {
		return trimmed
	}
	return "…" + trimmed[len(trimmed)-size:]
}

// message is the sentence a user reads on a failed measurement. A domain error already
// carries one written for a person; anything else falls back to the raw text.
func message(err error) string {
	var domain *apierr.Error
	if errors.As(err, &domain) {
		return domain.Message
	}
	return err.Error()
}

// readAll reads one file and closes it on every path out.
func readAll(handle *os.File) (string, error) {
	defer func() {
		if err := handle.Close(); err != nil {
			slog.Warn("close a checkout file", "file", handle.Name(), "error", err)
		}
	}()

	content, err := io.ReadAll(io.LimitReader(handle, maxCopiedFile))
	if err != nil {
		return "", err
	}
	return string(content), nil
}
