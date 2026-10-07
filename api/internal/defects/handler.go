package defects

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
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

// Analyses is what promotion reads: the explanation a defect is written from, and the
// failure it belongs to.
type Analyses interface {
	LatestFor(ctx context.Context, resultID uuid.UUID) (PromotionSource, bool, error)
}

// PromotionSource is the analysed failure, flattened to what a defect needs. Declared
// here so this package does not depend on the analysis package's types.
type PromotionSource struct {
	AnalysisID uuid.UUID
	Reason     string
	RootCause  string
	Fix        string

	RunID         uuid.UUID
	ProjectID     uuid.UUID
	TestName      string
	TestCaseID    *uuid.UUID
	RequirementID *uuid.UUID
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Handler implements the defect slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	analyses Analyses
	recorder Recorder

	// mirror pushes a filed defect to an external tracker. Optional: nil on an install
	// with no external tracker, where a defect lives only in the built-in (BE-10.2).
	mirror *Mirror
}

func NewHandler(
	service *Service,
	projectsService Projects,
	analyses Analyses,
	recorder Recorder,
	options ...HandlerOption,
) *Handler {
	handler := &Handler{
		service:  service,
		projects: projectsService,
		analyses: analyses,
		recorder: recorder,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// HandlerOption configures the handler.
type HandlerOption func(*Handler)

// WithMirror turns on mirroring filed defects to an external tracker.
func WithMirror(mirror *Mirror) HandlerOption {
	return func(h *Handler) { h.mirror = mirror }
}

func (h *Handler) ListDefects(
	ctx context.Context,
	request api.ListDefectsRequestObject,
) (api.ListDefectsResponseObject, error) {
	filter := Filter{}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Status != nil {
		filter.Status = Status(*request.Params.Status)
	}
	if request.Params.Severity != nil {
		filter.Severity = Severity(*request.Params.Severity)
	}
	if request.Params.AssigneeID != nil {
		filter.AssigneeID = request.Params.AssigneeID
	}
	if request.Params.Unassigned != nil {
		filter.Unassigned = *request.Params.Unassigned
	}
	if request.Params.TestCaseID != nil {
		filter.TestCaseID = request.Params.TestCaseID
	}
	if request.Params.IncludeDuplicates != nil {
		filter.IncludeDuplicates = *request.Params.IncludeDuplicates
	}

	page, err := h.service.List(ctx, request.ProjectID, filter)
	if err != nil {
		return nil, err
	}

	counts, err := h.service.StatusCounts(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	body := api.DefectPage{Items: make([]api.Defect, 0, len(page.Items))}
	for _, defect := range page.Items {
		body.Items = append(body.Items, toAPI(defect))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}

	tally := map[string]int{}
	for status, total := range counts {
		tally[string(status)] = total
	}
	body.Counts = &tally

	return api.ListDefects200JSONResponse(body), nil
}

func (h *Handler) CreateDefect(
	ctx context.Context,
	request api.CreateDefectRequestObject,
) (api.CreateDefectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	actorID := actor.UserID
	input := CreateInput{
		ProjectID:  request.ProjectID,
		Title:      request.Body.Title,
		CreatedBy:  &actorID,
		TestCaseID: request.Body.TestCaseId,
	}
	if request.Body.Description != nil {
		input.Description = *request.Body.Description
	}
	if request.Body.Severity != nil {
		input.Severity = Severity(*request.Body.Severity)
	}
	if request.Body.AssigneeId != nil {
		input.AssigneeID = request.Body.AssigneeId
	}
	if request.Body.RequirementId != nil {
		input.RequirementID = request.Body.RequirementId
	}

	created, err := h.service.Create(ctx, input)
	if err != nil {
		return nil, err
	}

	h.audit(ctx, actor, created, audit.ActionDefectFiled, map[string]any{"via": "manual"})

	// Mirrored after the internal write, best effort: the defect exists whatever the
	// external tracker does (BE-10.2.4).
	if h.mirror != nil {
		h.mirror.Push(ctx, created)
	}

	return api.CreateDefect201JSONResponse(toAPI(created)), nil
}

func (h *Handler) GetDefect(
	ctx context.Context,
	request api.GetDefectRequestObject,
) (api.GetDefectResponseObject, error) {
	defect, err := h.authorized(ctx, request.DefectID)
	if err != nil {
		return nil, err
	}

	occurrences, err := h.service.Occurrences(ctx, defect.ID)
	if err != nil {
		return nil, err
	}

	body := api.DefectDetail{
		Defect:      toAPI(defect),
		Occurrences: make([]api.Defect, 0, len(occurrences)),
	}
	for _, occurrence := range occurrences {
		body.Occurrences = append(body.Occurrences, toAPI(occurrence))
	}
	return api.GetDefect200JSONResponse(body), nil
}

func (h *Handler) UpdateDefect(
	ctx context.Context,
	request api.UpdateDefectRequestObject,
) (api.UpdateDefectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	existing, err := h.authorized(ctx, request.DefectID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureActive(ctx, existing.ProjectID); err != nil {
		return nil, err
	}

	input := UpdateInput{
		Title:       request.Body.Title,
		Description: request.Body.Description,
	}
	if request.Body.Severity != nil {
		severity := Severity(*request.Body.Severity)
		input.Severity = &severity
	}
	if request.Body.Status != nil {
		status := Status(*request.Body.Status)
		input.Status = &status
	}

	// A flag rather than a null: once a client omits a field, absent and null are the
	// same value on the wire, and "leave the assignee alone" and "clear it" are not
	// the same instruction.
	if request.Body.ClearAssignee != nil && *request.Body.ClearAssignee {
		input.ClearAssignee = true
	} else if request.Body.AssigneeId != nil {
		input.AssigneeID = request.Body.AssigneeId
	}

	updated, err := h.service.Update(ctx, request.DefectID, input)
	if err != nil {
		return nil, err
	}

	if input.Status != nil && *input.Status != existing.Status {
		h.audit(ctx, actor, updated, audit.ActionDefectStatusChanged, map[string]any{
			"from": string(existing.Status), "to": string(updated.Status),
		})
	}

	return api.UpdateDefect200JSONResponse(toAPI(updated)), nil
}

func (h *Handler) LinkDefectDuplicate(
	ctx context.Context,
	request api.LinkDefectDuplicateRequestObject,
) (api.LinkDefectDuplicateResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	existing, err := h.authorized(ctx, request.DefectID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureActive(ctx, existing.ProjectID); err != nil {
		return nil, err
	}

	// Absent means unlink. Unambiguous on this endpoint, because linking and
	// unlinking are the only two things it does.
	original := request.Body.DuplicateOf

	actorID := actor.UserID
	linked, err := h.service.LinkDuplicate(ctx, request.DefectID, original, &actorID)
	if err != nil {
		return nil, err
	}

	detail := map[string]any{"linked": original != nil}
	if original != nil {
		detail["duplicateOf"] = original.String()
	}
	h.audit(ctx, actor, linked, audit.ActionDefectLinked, detail)

	return api.LinkDefectDuplicate200JSONResponse(toAPI(linked)), nil
}

func (h *Handler) ListDefectComments(
	ctx context.Context,
	request api.ListDefectCommentsRequestObject,
) (api.ListDefectCommentsResponseObject, error) {
	if _, err := h.authorized(ctx, request.DefectID); err != nil {
		return nil, err
	}

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}

	comments, next, err := h.service.Comments(ctx, request.DefectID, limit, cursor)
	if err != nil {
		return nil, err
	}

	body := api.DefectCommentPage{Items: make([]api.DefectComment, 0, len(comments))}
	for _, comment := range comments {
		body.Items = append(body.Items, toAPIComment(comment))
	}
	if next != "" {
		body.NextCursor.Set(next)
	}
	return api.ListDefectComments200JSONResponse(body), nil
}

func (h *Handler) CommentOnDefect(
	ctx context.Context,
	request api.CommentOnDefectRequestObject,
) (api.CommentOnDefectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	existing, err := h.authorized(ctx, request.DefectID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureActive(ctx, existing.ProjectID); err != nil {
		return nil, err
	}

	actorID := actor.UserID
	comment, err := h.service.Comment(ctx, request.DefectID, &actorID, request.Body.Body, false)
	if err != nil {
		return nil, err
	}
	return api.CommentOnDefect201JSONResponse(toAPIComment(comment)), nil
}

// PromoteFromResult files a defect from an analysed failure.
//
// It is called through the analysis handler, because the route hangs off the failure
// while the work belongs here. The name differs from the operation's on purpose: both
// handlers are embedded in one server struct, and two methods called PromoteResult
// would make the generated interface ambiguous. The actor is passed rather than read
// from the context again, so the two handlers cannot disagree about who is acting.
func (h *Handler) PromoteFromResult(
	ctx context.Context,
	request api.PromoteResultRequestObject,
	actor httpx.Principal,
) (api.PromoteResultResponseObject, error) {
	source, found, err := h.analyses.LatestFor(ctx, request.ResultID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierr.AnalysisNotReady()
	}
	if err := h.projects.EnsureActive(ctx, source.ProjectID); err != nil {
		return nil, err
	}

	actorID := actor.UserID
	input := PromoteInput{
		ProjectID:     source.ProjectID,
		RunResultID:   request.ResultID,
		TestCaseID:    source.TestCaseID,
		RequirementID: source.RequirementID,
		TestName:      source.TestName,
		Analysis: Analysis{
			ID:        source.AnalysisID,
			Reason:    source.Reason,
			RootCause: source.RootCause,
			Fix:       source.Fix,
		},
		CreatedBy: &actorID,
	}
	if request.Body != nil {
		if request.Body.Severity != nil {
			input.Severity = Severity(*request.Body.Severity)
		}
		if request.Body.AssigneeId != nil {
			input.AssigneeID = request.Body.AssigneeId
		}
	}

	promotion, err := h.service.Promote(ctx, input)
	if err != nil {
		return nil, err
	}

	if promotion.Existing {
		// Already filed. A 200 rather than a 201 is the difference the client needs to
		// say "opened the existing defect" instead of "filed a new one".
		return api.PromoteResult200JSONResponse(toAPI(promotion.Defect)), nil
	}

	detail := map[string]any{"via": "promotion", "runResultId": request.ResultID.String()}
	if promotion.DuplicateOf != nil {
		detail["duplicateOf"] = promotion.DuplicateOf.String()
	}
	h.audit(ctx, actor, promotion.Defect, audit.ActionDefectFiled, detail)

	// A promoted failure is a filed defect, so it mirrors like one — including a
	// security finding, which reaches this same path (BE-9.4.2, BE-10.2).
	if h.mirror != nil {
		h.mirror.Push(ctx, promotion.Defect)
	}

	return api.PromoteResult201JSONResponse(toAPI(promotion.Defect)), nil
}

// authorized loads a defect and checks the caller is a member of its project.
func (h *Handler) authorized(ctx context.Context, id uuid.UUID) (Defect, error) {
	actor := httpx.MustCurrentUser(ctx)

	defect, err := h.service.Get(ctx, id)
	if err != nil {
		return Defect{}, err
	}
	if err := h.projects.EnsureMember(ctx, actor, defect.ProjectID); err != nil {
		// Reported as not found, so a caller cannot enumerate other projects' defects.
		return Defect{}, apierr.DefectNotFound()
	}
	return defect, nil
}

func (h *Handler) audit(
	ctx context.Context,
	actor httpx.Principal,
	defect Defect,
	action audit.Action,
	detail map[string]any,
) {
	if h.recorder == nil {
		return
	}

	actorID := actor.UserID
	project := defect.ProjectID
	h.recorder.Record(ctx, audit.Entry{
		Action:     action,
		ActorID:    &actorID,
		ActorEmail: actor.Email,
		Subject:    defect.ID.String(),
		ProjectID:  &project,
		Detail:     detail,
	})
}

func toAPI(defect Defect) api.Defect {
	body := api.Defect{
		Id:          defect.ID,
		ProjectId:   defect.ProjectID,
		Title:       defect.Title,
		Description: &defect.Description,
		Severity:    api.DefectSeverity(defect.Severity),
		Status:      api.DefectStatus(defect.Status),
		ExternalRef: &defect.ExternalRef,
		CreatedAt:   defect.CreatedAt,
		UpdatedAt:   defect.UpdatedAt,
	}

	body.RunResultId = defect.RunResultID
	body.TestCaseId = defect.TestCaseID
	body.RequirementId = defect.RequirementID
	body.AnalysisId = defect.AnalysisID
	body.AssigneeId = defect.AssigneeID
	body.DuplicateOf = defect.DuplicateOf
	body.CreatedBy = defect.CreatedBy
	body.ResolvedAt = defect.ResolvedAt

	return body
}

func toAPIComment(comment Comment) api.DefectComment {
	return api.DefectComment{
		Id:        comment.ID,
		DefectId:  comment.DefectID,
		AuthorId:  comment.AuthorID,
		Body:      comment.Body,
		System:    comment.System,
		CreatedAt: comment.CreatedAt,
	}
}
