package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Gathering (BE-5.9.2).
//
// Every figure here comes from stored rows, which is what makes a report defensible:
// the pass rate in a document sent to a client has to be the same number the
// dashboard showed, computed the same way, and neither may be a model's opinion.
//
// It reads across four domains — runs, requirements, analyses, defects — with plain
// SQL rather than by calling four services. That is a deliberate exception to the
// usual layering: the alternative is four services each exposing a
// report-shaped method, which spreads one document's definition across four packages
// and guarantees the sections drift apart.

// Collector gathers a project's report contents.
type Collector struct {
	db *store.DB
}

func NewCollector(db *store.DB) *Collector { return &Collector{db: db} }

// maxReportRows bounds every list in a document. A report is something a person
// reads; a hundred runs and fifty failures is already more than anybody scrolls, and
// an unbounded list turns one request into a document nobody can open.
const maxReportRows = 50

// Gather assembles the contents.
func (c *Collector) Gather(
	ctx context.Context,
	projectID uuid.UUID,
	windowDays int,
) (Contents, error) {
	if windowDays <= 0 {
		windowDays = defaultWindowDays
	}
	since := time.Now().AddDate(0, 0, -windowDays)

	project, err := c.db.Queries().GetProject(ctx, projectID)
	if err != nil {
		return Contents{}, apierr.ProjectNotFound(projectID)
	}

	contents := Contents{
		ProjectName: project.Name,
		GeneratedAt: time.Now(),
		WindowDays:  windowDays,
	}

	trend, err := c.db.Queries().RunTrend(ctx, dbgen.RunTrendParams{
		ProjectID: projectID,
		CreatedAt: since,
	})
	if err != nil {
		return Contents{}, apierr.Internal(fmt.Errorf("read the run trend: %w", err))
	}

	// Newest first in the document, oldest first from the query: a reader wants the
	// most recent run at the top, and a chart wants time to run forwards.
	for index := len(trend) - 1; index >= 0 && len(contents.Runs) < maxReportRows; index-- {
		row := trend[index]
		contents.Runs = append(contents.Runs, RunLine{
			ID:       row.ID,
			At:       row.CreatedAt,
			Status:   string(row.Status),
			Total:    int(row.Total),
			Passed:   int(row.Passed),
			Failed:   int(row.Failed),
			Flaky:    int(row.Flaky),
			Duration: time.Duration(row.DurationMs) * time.Millisecond,
		})
	}

	contents.Summary = summarise(trend)

	dashboard, err := c.db.Queries().ProjectDashboard(ctx, projectID)
	if err != nil {
		return Contents{}, apierr.Internal(fmt.Errorf("read the dashboard counts: %w", err))
	}
	contents.Coverage = Coverage{
		Requirements:  int(dashboard.Requirements),
		ApprovedCases: int(dashboard.ApprovedCases),
		TotalCases:    int(dashboard.TestCases),
		TestFiles:     int(dashboard.TestFiles),
	}

	covered, err := c.db.Queries().CountCoveredRequirements(ctx, projectID)
	if err != nil {
		return Contents{}, apierr.Internal(fmt.Errorf("read requirement coverage: %w", err))
	}
	contents.Coverage.Covered = int(covered)

	failures, err := c.db.Queries().ReportFailures(ctx, dbgen.ReportFailuresParams{
		ProjectID: projectID,
		CreatedAt: since,
		PageSize:  maxReportRows,
	})
	if err != nil {
		return Contents{}, apierr.Internal(fmt.Errorf("read failures: %w", err))
	}
	for _, row := range failures {
		contents.Failures = append(contents.Failures, toFailure(row))
	}

	defectRows, err := c.db.Queries().ReportDefects(ctx, dbgen.ReportDefectsParams{
		ProjectID: projectID,
		PageSize:  maxReportRows,
	})
	if err != nil {
		return Contents{}, apierr.Internal(fmt.Errorf("read defects: %w", err))
	}
	for _, row := range defectRows {
		contents.Defects = append(contents.Defects, DefectLine{
			ID:          row.ID,
			Title:       row.Title,
			Severity:    string(row.Severity),
			Status:      string(row.Status),
			Occurrences: int(row.Occurrences),
			CreatedAt:   row.CreatedAt,
		})
	}

	open, err := c.db.Queries().CountOpenDefects(ctx, projectID)
	if err != nil {
		return Contents{}, apierr.Internal(fmt.Errorf("count open defects: %w", err))
	}
	contents.Summary.OpenDefect = int(open)

	return contents, nil
}

// summarise totals the window.
//
// The pass rate counts a flaky test as neither a pass nor a failure, and skips
// skipped tests, which is the same definition the trend endpoint uses. Two places
// computing it differently is how a report stops being trusted.
func summarise(trend []dbgen.RunTrendRow) Summary {
	summary := Summary{Runs: len(trend)}

	executed := 0
	for _, row := range trend {
		summary.Passed += int(row.Passed)
		summary.Failed += int(row.Failed)
		summary.Flaky += int(row.Flaky)
		summary.Skipped += int(row.Skipped)
		executed += int(row.Passed) + int(row.Failed) + int(row.Flaky)
	}

	if executed > 0 {
		summary.PassRate = float64(summary.Passed) / float64(executed)
	}
	if len(trend) > 0 {
		latest := trend[len(trend)-1].CreatedAt
		summary.LastRunAt = &latest
	}
	return summary
}

// toFailure flattens one failure and whatever explanation exists for it.
func toFailure(row dbgen.ReportFailuresRow) Failure {
	failure := Failure{
		TestName: row.Name,
		Status:   string(row.Status),
		Message:  row.FailureMessage,
		DefectID: row.DefectID,
	}

	if row.Reason == "" && row.RootCause == "" {
		// Unanalysed, and shown anyway: a report that omitted the failures nobody has
		// explained would be a report that looks better than the project is.
		return failure
	}

	failure.Analysed = true
	failure.Reason = row.Reason
	failure.RootCause = row.RootCause
	failure.SuggestedFix = row.SuggestedFix
	if row.StabilityScore != nil {
		score, _ := row.StabilityScore.Float64()
		failure.Stability = &score
	}
	failure.Evidence = evidenceLines(row.Evidence)

	return failure
}

// evidenceLines renders an analysis's citations as the one-line references a document
// shows. The structured form is what the UI links; a printed report needs the words.
func evidenceLines(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var references []struct {
		Kind     string `json:"kind"`
		Detail   string `json:"detail"`
		File     string `json:"file"`
		FromLine int    `json:"fromLine"`
		ToLine   int    `json:"toLine"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(raw, &references); err != nil {
		// A report is not the place to fail over unreadable evidence: the analysis is
		// still worth printing without its citation list.
		return nil
	}

	lines := make([]string, 0, len(references))
	for _, reference := range references {
		where := reference.File
		switch {
		case reference.Kind == "log" && reference.FromLine > 0:
			where = fmt.Sprintf("log lines %d to %d", reference.FromLine, max(reference.ToLine, reference.FromLine))
		case reference.Kind == "response":
			where = "response " + reference.Path
		case reference.Kind == "history":
			where = "run history"
		case reference.FromLine > 0:
			where = fmt.Sprintf("%s:%d", reference.File, reference.FromLine)
		}

		if where == "" {
			lines = append(lines, reference.Detail)
			continue
		}
		lines = append(lines, fmt.Sprintf("%s — %s", where, reference.Detail))
	}
	return lines
}
