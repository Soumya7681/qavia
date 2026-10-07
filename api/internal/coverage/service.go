package coverage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service owns measured coverage.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Measurement is one coverage run.
type Measurement struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Commit  string
	Tool    string
	Command string

	LinesTotal      int
	LinesCovered    int
	BranchesTotal   int
	BranchesCovered int

	// Error is set when the tool could not run. A different state from zero coverage,
	// and the API says which one it is looking at.
	Error string

	JobID     *uuid.UUID
	CreatedAt time.Time
}

// LineRate is the proportion of lines executed, or -1 when nothing was measurable.
func (m Measurement) LineRate() float64 {
	if m.LinesTotal == 0 {
		return -1
	}
	return float64(m.LinesCovered) / float64(m.LinesTotal)
}

// BranchRate is the same for branches, or -1 when the tool does not measure them.
func (m Measurement) BranchRate() float64 {
	if m.BranchesTotal == 0 {
		return -1
	}
	return float64(m.BranchesCovered) / float64(m.BranchesTotal)
}

// StoreInput is a parsed report being recorded.
type StoreInput struct {
	ProjectID uuid.UUID
	Commit    string
	Command   string
	Report    Report
	JobID     *uuid.UUID
}

// Store writes a measurement and its per-file detail.
//
// One transaction and a CopyFrom for the files, because a repository with three
// thousand files is three thousand rows and this runs after every coverage pass
// (backend-standards.md 8).
func (s *Service) Store(ctx context.Context, input StoreInput) (Measurement, error) {
	var stored dbgen.CoverageRun

	err := s.db.InTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.CreateCoverageRun(ctx, dbgen.CreateCoverageRunParams{
			ProjectID:       input.ProjectID,
			CommitSha:       input.Commit,
			Tool:            input.Report.Tool,
			Command:         input.Command,
			LinesTotal:      int32(input.Report.LinesTotal),      //nolint:gosec // A repository this large does not exist.
			LinesCovered:    int32(input.Report.LinesCovered),    //nolint:gosec
			BranchesTotal:   int32(input.Report.BranchesTotal),   //nolint:gosec
			BranchesCovered: int32(input.Report.BranchesCovered), //nolint:gosec
			JobID:           input.JobID,
		})
		if err != nil {
			return fmt.Errorf("create the coverage run: %w", err)
		}
		stored = row

		if len(input.Report.Files) == 0 {
			return nil
		}

		files := make([]dbgen.CreateCoverageFilesBulkParams, 0, len(input.Report.Files))
		for _, file := range input.Report.Files {
			files = append(files, dbgen.CreateCoverageFilesBulkParams{
				CoverageRunID:   row.ID,
				Path:            file.Path,
				LinesTotal:      int32(file.LinesTotal),      //nolint:gosec
				LinesCovered:    int32(file.LinesCovered),    //nolint:gosec
				BranchesTotal:   int32(file.BranchesTotal),   //nolint:gosec
				BranchesCovered: int32(file.BranchesCovered), //nolint:gosec
			})
		}

		if _, err := queries.CreateCoverageFilesBulk(ctx, files); err != nil {
			return fmt.Errorf("write the per-file coverage: %w", err)
		}
		return nil
	})
	if err != nil {
		return Measurement{}, apierr.Internal(err)
	}

	return toMeasurement(stored), nil
}

// RecordFailure stores that the tool could not run.
//
// A row rather than nothing, because "we tried and this is what happened" is the
// answer a user needs, and an absent row reads as "nobody has measured yet".
func (s *Service) RecordFailure(
	ctx context.Context,
	projectID uuid.UUID,
	commit, command, reason string,
	jobID *uuid.UUID,
) error {
	if _, err := s.db.Queries().CreateCoverageRun(ctx, dbgen.CreateCoverageRunParams{
		ProjectID: projectID,
		CommitSha: commit,
		Command:   command,
		Error:     reason,
		JobID:     jobID,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("record the coverage failure: %w", err))
	}
	return nil
}

// Latest is the newest successful measurement for a project.
func (s *Service) Latest(ctx context.Context, projectID uuid.UUID) (Measurement, bool, error) {
	row, err := s.db.Queries().LatestCoverageRun(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Absent rather than zero. A project with no repository connected has no code
			// coverage, and reporting 0% would be a claim about code nobody has (BE-6.7.3).
			return Measurement{}, false, nil
		}
		return Measurement{}, false, apierr.Internal(
			fmt.Errorf("read the coverage run: %w", err))
	}
	return toMeasurement(row), true, nil
}

// Files is the per-file detail, least covered first.
func (s *Service) Files(
	ctx context.Context,
	runID uuid.UUID,
	limit int,
) ([]File, error) {
	rows, err := s.db.Queries().ListCoverageFiles(ctx, dbgen.ListCoverageFilesParams{
		CoverageRunID: runID,
		PageSize:      int32(paging.ClampLimit(limit)), //nolint:gosec // Clamped.
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the per-file coverage: %w", err))
	}

	files := make([]File, 0, len(rows))
	for _, row := range rows {
		files = append(files, File{
			Path:            row.Path,
			LinesTotal:      int(row.LinesTotal),
			LinesCovered:    int(row.LinesCovered),
			BranchesTotal:   int(row.BranchesTotal),
			BranchesCovered: int(row.BranchesCovered),
		})
	}
	return files, nil
}

// TrendPoint is one measurement in the trend.
type TrendPoint struct {
	ID       uuid.UUID
	Commit   string
	LineRate float64
	At       time.Time
}

// Trend is what gives a measured number its point: whether it went up.
func (s *Service) Trend(
	ctx context.Context,
	projectID uuid.UUID,
	window time.Duration,
) ([]TrendPoint, error) {
	if window <= 0 {
		window = 90 * 24 * time.Hour
	}

	rows, err := s.db.Queries().CoverageTrend(ctx, dbgen.CoverageTrendParams{
		ProjectID: projectID,
		CreatedAt: time.Now().Add(-window),
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the coverage trend: %w", err))
	}

	points := make([]TrendPoint, 0, len(rows))
	for _, row := range rows {
		rate := -1.0
		if row.LinesTotal > 0 {
			rate = float64(row.LinesCovered) / float64(row.LinesTotal)
		}
		points = append(points, TrendPoint{
			ID: row.ID, Commit: row.CommitSha, LineRate: rate, At: row.CreatedAt,
		})
	}
	return points, nil
}

func toMeasurement(row dbgen.CoverageRun) Measurement {
	return Measurement{
		ID:              row.ID,
		ProjectID:       row.ProjectID,
		Commit:          row.CommitSha,
		Tool:            row.Tool,
		Command:         row.Command,
		LinesTotal:      int(row.LinesTotal),
		LinesCovered:    int(row.LinesCovered),
		BranchesTotal:   int(row.BranchesTotal),
		BranchesCovered: int(row.BranchesCovered),
		Error:           row.Error,
		JobID:           row.JobID,
		CreatedAt:       row.CreatedAt,
	}
}
