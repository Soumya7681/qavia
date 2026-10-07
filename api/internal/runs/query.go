package runs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Read side: history, results, trends, and the dashboard counts (BE-4.14).
//
// Everything here is cursor paginated and every aggregate is one query. The
// dashboard is the reason: five panels that each cost a table scan is a dashboard
// that gets slower every week, and the counts on the run row exist so it does not
// have to.

// ListFilter narrows a project's runs.
type ListFilter struct {
	Status Status
	Kind   Kind
	Limit  int
	Cursor string
}

// Page is one page of runs plus the cursor for the next.
type Page struct {
	Items      []Run
	NextCursor string
}

// List returns a project's runs, newest first.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, filter ListFilter) (Page, error) {
	limit := paging.ClampLimit(filter.Limit)

	var cursor *time.Time
	if filter.Cursor != "" {
		at, err := paging.DecodeTime(filter.Cursor)
		if err != nil {
			return Page{}, err
		}
		cursor = &at
	}

	status := dbgen.NullRunStatus{}
	if filter.Status != "" {
		status = dbgen.NullRunStatus{RunStatus: dbgen.RunStatus(filter.Status), Valid: true}
	}
	kind := dbgen.NullRunKind{}
	if filter.Kind != "" {
		kind = dbgen.NullRunKind{RunKind: dbgen.RunKind(filter.Kind), Valid: true}
	}

	rows, err := s.db.Queries().ListRuns(ctx, dbgen.ListRunsParams{
		ProjectID: projectID,
		Status:    status,
		Kind:      kind,
		Cursor:    cursor,

		// One more than asked for, so "is there another page" is an answer rather
		// than a guess.
		PageSize: int32(limit + 1), //nolint:gosec // Clamped above.
	})
	if err != nil {
		return Page{}, apierr.Internal(fmt.Errorf("list runs: %w", err))
	}

	page := Page{Items: make([]Run, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			page.NextCursor = paging.EncodeTime(page.Items[limit-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toRun(row))
	}
	return page, nil
}

// ResultFilter narrows a run's results.
type ResultFilter struct {
	Status ResultStatus
	Limit  int
	Cursor string
}

// ResultPage is one page of results plus the cursor for the next.
type ResultPage struct {
	Items      []Result
	NextCursor string
}

// Results returns one run's results, ordered so a reader lands on the failures
// first: a 400-test run with 3 failures should not need pagination to find them.
func (s *Service) Results(ctx context.Context, runID uuid.UUID, filter ResultFilter) (ResultPage, error) {
	limit := paging.ClampLimit(filter.Limit)

	offset := 0
	if filter.Cursor != "" {
		decoded, err := paging.DecodeID(filter.Cursor)
		if err != nil {
			return ResultPage{}, err
		}
		offset = int(decoded)
	}

	status := dbgen.NullRunResultStatus{}
	if filter.Status != "" {
		status = dbgen.NullRunResultStatus{
			RunResultStatus: dbgen.RunResultStatus(filter.Status), Valid: true,
		}
	}

	rows, err := s.db.Queries().ListRunResults(ctx, dbgen.ListRunResultsParams{
		RunID:     runID,
		Status:    status,
		RowOffset: int32(offset),    //nolint:gosec // Derived from a cursor this service issued.
		PageSize:  int32(limit + 1), //nolint:gosec // Clamped above.
	})
	if err != nil {
		return ResultPage{}, apierr.Internal(fmt.Errorf("list run results: %w", err))
	}

	page := ResultPage{Items: make([]Result, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			// Results are ordered by status then name, not by a monotonic key, so the
			// cursor is an offset. It is stable for a finished run, which is the only
			// state this list is paged in.
			page.NextCursor = paging.EncodeID(int64(offset + limit))
			break
		}
		page.Items = append(page.Items, toResult(row))
	}
	return page, nil
}

// StatusCounts is a run's results tallied by status, for the summary strip.
func (s *Service) StatusCounts(ctx context.Context, runID uuid.UUID) (map[ResultStatus]int, error) {
	rows, err := s.db.Queries().CountRunResultsByStatus(ctx, runID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("count run results: %w", err))
	}

	counts := make(map[ResultStatus]int, len(rows))
	for _, row := range rows {
		counts[ResultStatus(row.Status)] = int(row.Total)
	}
	return counts, nil
}

// Commands returns the run's command log (F-17.2).
func (s *Service) Commands(ctx context.Context, runID uuid.UUID) ([]Command, error) {
	rows, err := s.db.Queries().ListRunCommands(ctx, runID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list run commands: %w", err))
	}

	commands := make([]Command, 0, len(rows))
	for _, row := range rows {
		command := Command{
			ID:            row.ID,
			RunID:         row.RunID,
			Command:       row.Command,
			Duration:      time.Duration(row.DurationMs) * time.Millisecond,
			OutputExcerpt: row.OutputExcerpt,
			At:            row.At,
		}
		if row.ExitCode != nil {
			code := int(*row.ExitCode)
			command.ExitCode = &code
		}
		commands = append(commands, command)
	}
	return commands, nil
}

// CaseHistory is one test case's result across runs, newest first.
type CaseHistory struct {
	RunID     uuid.UUID
	Status    ResultStatus
	Duration  time.Duration
	Attempt   int
	CreatedAt time.Time
}

// History is what makes a flaky test visible as a pattern rather than as one odd
// run (F-7.11, F-12.6).
func (s *Service) History(
	ctx context.Context,
	testCaseID uuid.UUID,
	limit int,
) ([]CaseHistory, error) {
	rows, err := s.db.Queries().ResultHistoryForCase(ctx, dbgen.ResultHistoryForCaseParams{
		TestCaseID: &testCaseID,
		PageSize:   int32(paging.ClampLimit(limit)), //nolint:gosec // Clamped.
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the result history: %w", err))
	}

	history := make([]CaseHistory, 0, len(rows))
	for _, row := range rows {
		history = append(history, CaseHistory{
			RunID:     row.RunID,
			Status:    ResultStatus(row.Status),
			Duration:  time.Duration(row.DurationMs) * time.Millisecond,
			Attempt:   int(row.Attempt),
			CreatedAt: row.CreatedAt,
		})
	}
	return history, nil
}

// TrendPoint is one run in the trend panel.
type TrendPoint struct {
	RunID    uuid.UUID
	At       time.Time
	Status   Status
	Total    int
	Passed   int
	Failed   int
	Flaky    int
	Skipped  int
	Duration time.Duration
	PassRate float64
}

// Trend returns a project's finished runs in a window, oldest first, so a chart
// plots them without reversing the slice.
//
// A window rather than a count, because the panel answers "how has this looked
// lately", and the last 50 runs mean something different for a project that runs
// hourly than for one that runs monthly.
func (s *Service) Trend(
	ctx context.Context,
	projectID uuid.UUID,
	window time.Duration,
) ([]TrendPoint, error) {
	if window <= 0 {
		window = 30 * 24 * time.Hour
	}

	rows, err := s.db.Queries().RunTrend(ctx, dbgen.RunTrendParams{
		ProjectID: projectID,
		CreatedAt: time.Now().Add(-window),
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the run trend: %w", err))
	}

	points := make([]TrendPoint, 0, len(rows))
	for _, row := range rows {
		point := TrendPoint{
			RunID:    row.ID,
			At:       row.CreatedAt,
			Status:   Status(row.Status),
			Total:    int(row.Total),
			Passed:   int(row.Passed),
			Failed:   int(row.Failed),
			Flaky:    int(row.Flaky),
			Skipped:  int(row.Skipped),
			Duration: time.Duration(row.DurationMs) * time.Millisecond,
		}

		// Computed here rather than in SQL so the definition is visible: a flaky
		// test is not a pass, and a skipped test is not a failure, so neither counts
		// toward the rate.
		if executed := point.Passed + point.Failed + point.Flaky; executed > 0 {
			point.PassRate = float64(point.Passed) / float64(executed)
		}

		points = append(points, point)
	}
	return points, nil
}

// Dashboard is the counts panel (F-12.1).
type Dashboard struct {
	Requirements  int
	TestCases     int
	ApprovedCases int
	TestFiles     int
	Runs          int
	Passed        int
	Failed        int
	Flaky         int
}

// DashboardFor reads every panel count in one query.
//
// One request per panel is the requirement; one query for all of them is what makes
// that affordable, and the numbers reconcile because they come from the same
// snapshot rather than from five reads a user's page load interleaved with a run.
func (s *Service) DashboardFor(ctx context.Context, projectID uuid.UUID) (Dashboard, error) {
	row, err := s.db.Queries().ProjectDashboard(ctx, projectID)
	if err != nil {
		return Dashboard{}, apierr.Internal(fmt.Errorf("read the project dashboard: %w", err))
	}

	return Dashboard{
		Requirements:  int(row.Requirements),
		TestCases:     int(row.TestCases),
		ApprovedCases: int(row.ApprovedCases),
		TestFiles:     int(row.TestFiles),
		Runs:          int(row.Runs),
		Passed:        int(row.Passed),
		Failed:        int(row.Failed),
		Flaky:         int(row.Flaky),
	}, nil
}
