package reports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service owns report rows and the files behind them.
type Service struct {
	db      *store.DB
	objects objectstore.Store
}

func NewService(db *store.DB, objects objectstore.Store) *Service {
	return &Service{db: db, objects: objects}
}

// defaultWindowDays is how far back a report looks when nobody says. Thirty days is
// a reporting period people recognise, and it is short enough that a project with two
// years of runs does not produce a document nobody opens.
const defaultWindowDays = 30

// Request is a report somebody asked for.
type Request struct {
	ProjectID  uuid.UUID
	Format     Format
	WindowDays int

	JobID       *uuid.UUID
	RequestedBy *uuid.UUID
}

// Create writes the row for a report that has not been generated yet.
//
// The row before the file, and before the job: a generation that fails is then a
// visible failed report rather than a request that vanished (backend-standards.md 8).
func (s *Service) Create(ctx context.Context, request Request) (Report, error) {
	if request.Format == "" {
		request.Format = FormatHTML
	}
	if !request.Format.Valid() {
		return Report{}, apierr.Validation(
			fmt.Sprintf("Format %q is not one this platform renders. Use html or pdf.", request.Format),
			map[string]any{"field": "format"})
	}
	if request.WindowDays <= 0 {
		request.WindowDays = defaultWindowDays
	}

	row, err := s.db.Queries().CreateReport(ctx, dbgen.CreateReportParams{
		ProjectID:   request.ProjectID,
		Format:      dbgen.ReportFormat(request.Format),
		WindowDays:  int32(request.WindowDays), //nolint:gosec // Bounded by the API schema.
		JobID:       request.JobID,
		RequestedBy: request.RequestedBy,
	})
	if err != nil {
		return Report{}, apierr.Internal(fmt.Errorf("create the report: %w", err))
	}
	return toReport(row), nil
}

// Get reads one report.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Report, error) {
	row, err := s.db.Queries().GetReport(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Report{}, apierr.ReportNotFound()
		}
		return Report{}, apierr.Internal(fmt.Errorf("read the report: %w", err))
	}
	return toReport(row), nil
}

// Page is one page of reports plus the cursor for the next.
type Page struct {
	Items      []Report
	NextCursor string
}

// List returns a project's reports, newest first.
func (s *Service) List(
	ctx context.Context,
	projectID uuid.UUID,
	limit int,
	cursor string,
) (Page, error) {
	size := paging.ClampLimit(limit)

	var before *time.Time
	if cursor != "" {
		at, err := paging.DecodeTime(cursor)
		if err != nil {
			return Page{}, err
		}
		before = &at
	}

	rows, err := s.db.Queries().ListReports(ctx, dbgen.ListReportsParams{
		ProjectID: projectID,
		Cursor:    before,
		PageSize:  int32(size + 1), //nolint:gosec // Clamped above.
	})
	if err != nil {
		return Page{}, apierr.Internal(fmt.Errorf("list reports: %w", err))
	}

	page := Page{Items: make([]Report, 0, min(len(rows), size))}
	for index, row := range rows {
		if index == size {
			page.NextCursor = paging.EncodeTime(page.Items[size-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toReport(row))
	}
	return page, nil
}

// MarkRunning claims a queued report. A redelivered job finds it already running and
// stops rather than generating the same document twice.
func (s *Service) MarkRunning(ctx context.Context, id uuid.UUID) (bool, error) {
	affected, err := s.db.Queries().MarkReportRunning(ctx, id)
	if err != nil {
		return false, apierr.Internal(fmt.Errorf("claim the report: %w", err))
	}
	return affected > 0, nil
}

// Store writes the rendered document and marks the report ready.
func (s *Service) Store(
	ctx context.Context,
	report Report,
	content []byte,
) (Report, error) {
	if s.objects == nil {
		return Report{}, apierr.Internal(errors.New("no object store is configured for reports"))
	}

	key := objectstore.Key(report.ProjectID, "reports", report.ID,
		"report."+report.Format.Extension())

	stored, err := s.objects.Put(ctx, key, bytes.NewReader(content), objectstore.PutOptions{
		ContentType: report.Format.ContentType(),
		Size:        int64(len(content)),
	})
	if err != nil {
		return Report{}, apierr.Internal(fmt.Errorf("store the report: %w", err))
	}

	row, err := s.db.Queries().FinishReport(ctx, dbgen.FinishReportParams{
		ID:         report.ID,
		StorageKey: key,
		SizeBytes:  stored.Size,
	})
	if err != nil {
		return Report{}, apierr.Internal(fmt.Errorf("finish the report: %w", err))
	}
	return toReport(row), nil
}

// Fail records why a report could not be produced, in words a user can act on.
func (s *Service) Fail(ctx context.Context, id uuid.UUID, reason string) {
	if err := s.db.Queries().FailReport(ctx, dbgen.FailReportParams{
		ID: id, Error: reason,
	}); err != nil {
		slog.WarnContext(ctx, "mark the report failed", "report_id", id, "error", err)
	}
}

// Open streams a ready report for download.
//
// The caller closes the reader. A report that is not ready is refused with its own
// code rather than a 404, because "not yet" and "never existed" are different answers.
func (s *Service) Open(ctx context.Context, report Report) (io.ReadCloser, error) {
	if !report.Ready() {
		return nil, apierr.ReportNotReady(string(report.Status))
	}
	if s.objects == nil {
		return nil, apierr.Internal(errors.New("no object store is configured for reports"))
	}

	reader, err := s.objects.Get(ctx, report.StorageKey)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the report: %w", err))
	}
	return reader, nil
}

func toReport(row dbgen.Report) Report {
	return Report{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		Format:      Format(row.Format),
		Status:      Status(row.Status),
		WindowDays:  int(row.WindowDays),
		StorageKey:  row.StorageKey,
		SizeBytes:   row.SizeBytes,
		Error:       row.Error,
		JobID:       row.JobID,
		RequestedBy: row.RequestedBy,
		CreatedAt:   row.CreatedAt,
		FinishedAt:  row.FinishedAt,
	}
}
