package reports

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service the routes outside a project path
// need, so authorisation stays in one layer (backend-standards.md 11).
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Queue pushes the build job. One method rather than the jobs service, so this
// package cannot reach for anything else it owns.
type Queue interface {
	SubmitReport(ctx context.Context, projectID, reportID uuid.UUID, actor *uuid.UUID) (uuid.UUID, error)
}

// Handler implements the report slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	queue    Queue
}

func NewHandler(service *Service, projectsService Projects, queue Queue) *Handler {
	return &Handler{service: service, projects: projectsService, queue: queue}
}

// RequestReport writes the row, then queues the work.
//
// That order is the platform's rule for anything queued: a row with no task is
// visible and retryable, and a task with no row is invisible
// (backend-standards.md 8).
func (h *Handler) RequestReport(
	ctx context.Context,
	request api.RequestReportRequestObject,
) (api.RequestReportResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	format := FormatHTML
	window := defaultWindowDays
	if request.Body != nil {
		if request.Body.Format != nil {
			format = Format(*request.Body.Format)
		}
		if request.Body.Days != nil {
			window = *request.Body.Days
		}
	}

	actorID := actor.UserID
	report, err := h.service.Create(ctx, Request{
		ProjectID:   request.ProjectID,
		Format:      format,
		WindowDays:  window,
		RequestedBy: &actorID,
	})
	if err != nil {
		return nil, err
	}

	if _, err := h.queue.SubmitReport(ctx, request.ProjectID, report.ID, &actorID); err != nil {
		// The row stays, marked failed, so the request is visible rather than lost.
		h.service.Fail(ctx, report.ID, "The report could not be queued: "+err.Error())
		return nil, err
	}

	return api.RequestReport202JSONResponse(toAPI(report)), nil
}

func (h *Handler) ListReports(
	ctx context.Context,
	request api.ListReportsRequestObject,
) (api.ListReportsResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}

	page, err := h.service.List(ctx, request.ProjectID, limit, cursor)
	if err != nil {
		return nil, err
	}

	body := api.ReportPage{Items: make([]api.Report, 0, len(page.Items))}
	for _, report := range page.Items {
		body.Items = append(body.Items, toAPI(report))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListReports200JSONResponse(body), nil
}

func (h *Handler) GetReport(
	ctx context.Context,
	request api.GetReportRequestObject,
) (api.GetReportResponseObject, error) {
	report, err := h.authorized(ctx, request.ReportID)
	if err != nil {
		return nil, err
	}
	return api.GetReport200JSONResponse(toAPI(report)), nil
}

// DownloadReport streams the file.
//
// It satisfies the generated response interface directly rather than reading the
// object into memory first: a report is small, but "small" is a property of today's
// projects and streaming costs nothing to write.
func (h *Handler) DownloadReport(
	ctx context.Context,
	request api.DownloadReportRequestObject,
) (api.DownloadReportResponseObject, error) {
	report, err := h.authorized(ctx, request.ReportID)
	if err != nil {
		return nil, err
	}

	reader, err := h.service.Open(ctx, report)
	if err != nil {
		return nil, err
	}

	return &download{ctx: ctx, report: report, body: reader}, nil
}

// authorized loads a report and checks the caller is a member of its project.
func (h *Handler) authorized(ctx context.Context, id uuid.UUID) (Report, error) {
	actor := httpx.MustCurrentUser(ctx)

	report, err := h.service.Get(ctx, id)
	if err != nil {
		return Report{}, err
	}
	if err := h.projects.EnsureMember(ctx, actor, report.ProjectID); err != nil {
		// Reported as not found, so a caller cannot enumerate other projects' reports.
		return Report{}, apierr.ReportNotFound()
	}
	return report, nil
}

// download writes the file and closes the object-store reader on the same path that
// opened it.
type download struct {
	ctx    context.Context
	report Report
	body   io.ReadCloser
}

func (d *download) VisitDownloadReportResponse(w http.ResponseWriter) error {
	defer func() {
		if err := d.body.Close(); err != nil {
			slog.WarnContext(d.ctx, "close the report stream", "report_id", d.report.ID, "error", err)
		}
	}()

	filename := fmt.Sprintf("qavia-report-%s.%s",
		d.report.CreatedAt.Format("2006-01-02"), d.report.Format.Extension())

	w.Header().Set("Content-Type", d.report.Format.ContentType())
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if d.report.SizeBytes > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", d.report.SizeBytes))
	}
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, d.body); err != nil {
		// The client went away mid-download. Not a server error, and the header is
		// already written, so there is nothing to report but the log line.
		slog.WarnContext(d.ctx, "stream the report", "report_id", d.report.ID, "error", err)
	}
	return nil
}

func toAPI(report Report) api.Report {
	body := api.Report{
		Id:         report.ID,
		ProjectId:  report.ProjectID,
		Format:     api.ReportFormat(report.Format),
		Status:     api.ReportStatus(report.Status),
		WindowDays: report.WindowDays,
		SizeBytes:  &report.SizeBytes,
		Error:      &report.Error,
		CreatedAt:  report.CreatedAt,
	}
	body.JobId = report.JobID
	body.RequestedBy = report.RequestedBy
	body.FinishedAt = report.FinishedAt
	return body
}
