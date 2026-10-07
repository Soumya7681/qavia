package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/testfiles"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service the routes outside a project path
// need, so authorisation stays in one layer (backend-standards.md 11).
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Chains starts the execute pipeline. One method rather than the jobs service, so
// this package cannot reach for anything else it owns.
type Chains interface {
	SubmitTriggered(ctx context.Context, req runtrigger.Request) (uuid.UUID, error)
}

// Recorder writes audit entries. Declared as the one method this package needs.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Handler implements the execution slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	chains   Chains
	recorder Recorder

	// hub wakes the log stream. Optional: without it the stream still works, on the
	// poll interval rather than on notification.
	hub Subscriber
}

func NewHandler(
	service *Service,
	projectsService Projects,
	chains Chains,
	recorder Recorder,
	hub Subscriber,
) *Handler {
	return &Handler{
		service:  service,
		projects: projectsService,
		chains:   chains,
		recorder: recorder,
		hub:      hub,
	}
}

// TriggerRun checks the target, writes the run row, then queues the chain.
//
// That order is the control: the allowlist check and the concurrency cap happen on
// the request that asked for the run, where a refusal can be explained, rather than
// inside a worker where it becomes a failed job (BE-4.7.1).
func (h *Handler) TriggerRun(
	ctx context.Context,
	request api.TriggerRunRequestObject,
) (api.TriggerRunResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	framework := testfiles.FrameworkSupertest
	target := ""
	if request.Body != nil {
		if request.Body.Framework != nil {
			framework = testfiles.Framework(*request.Body.Framework)
		}
		if request.Body.TargetUrl != nil {
			target = *request.Body.TargetUrl
		}
	}

	actorID := actor.UserID
	run, checked, err := h.service.Create(ctx, CreateInput{
		ProjectID:   request.ProjectID,
		Framework:   framework,
		Trigger:     TriggerManual,
		TargetURL:   target,
		TriggeredBy: &actorID,
	})
	if err != nil {
		h.auditRejection(ctx, actor, request.ProjectID, target, err)
		return nil, err
	}

	// The row exists, so the chain can be submitted against it. A submit that fails
	// leaves a queued run the sweeper eventually errors with a reason, which is a
	// better outcome than a task with no row.
	jobID, err := h.chains.SubmitTriggered(ctx, runtrigger.Request{
		ProjectID: request.ProjectID,
		Chain:     string(jobs.ChainExecute),
		Source:    runtrigger.KindManual,
		ActorID:   &actorID,
		RunID:     &run.ID,
		Reference: fmt.Sprintf("run %s", run.ID),
	})
	if err != nil {
		return nil, err
	}
	run.JobID = &jobID

	if h.recorder != nil {
		project := request.ProjectID
		h.recorder.Record(ctx, audit.Entry{
			Action:     audit.ActionRunTriggered,
			ActorID:    &actorID,
			ActorEmail: actor.Email,
			Subject:    checked.Host,
			ProjectID:  &project,
			Detail: map[string]any{
				"runId":     run.ID,
				"framework": string(framework),
				"target":    checked.URL,
				"image":     run.Image,
			},
		})
	}

	return api.TriggerRun202JSONResponse(toAPIRun(run)), nil
}

// auditRejection records a refused run.
//
// Only the refusals that are about where a run would have gone: a target that is not
// on the allowlist, resolves somewhere a run must not reach, or cannot be resolved at
// all. A missing target or a concurrency cap is a configuration message, not a
// security event, and auditing those would bury the ones that matter (BE-4.7.5).
func (h *Handler) auditRejection(
	ctx context.Context,
	actor httpx.Principal,
	projectID uuid.UUID,
	requested string,
	cause error,
) {
	if h.recorder == nil {
		return
	}

	var domain *apierr.Error
	if !errors.As(cause, &domain) {
		return
	}
	switch domain.Code {
	case apierr.CodeTargetHostNotAllowed,
		apierr.CodeTargetAddressNotAllowed,
		apierr.CodeTargetUnresolvable:
	default:
		return
	}

	subject := requested
	if host, ok := domain.Details["host"].(string); ok && host != "" {
		subject = host
	}

	detail := map[string]any{"code": domain.Code, "reason": domain.Message}
	for key, value := range domain.Details {
		detail[key] = value
	}
	if requested != "" {
		detail["requested"] = requested
	}

	actorID := actor.UserID
	project := projectID
	h.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionTargetRejected,
		ActorID:    &actorID,
		ActorEmail: actor.Email,
		Subject:    subject,
		ProjectID:  &project,
		Detail:     detail,
	})
}

func (h *Handler) ListRuns(
	ctx context.Context,
	request api.ListRunsRequestObject,
) (api.ListRunsResponseObject, error) {
	filter := ListFilter{Limit: 0}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Status != nil {
		filter.Status = Status(*request.Params.Status)
	}
	if request.Params.Type != nil {
		filter.Kind = Kind(*request.Params.Type)
	}

	page, err := h.service.List(ctx, request.ProjectID, filter)
	if err != nil {
		return nil, err
	}

	body := api.RunPage{Items: make([]api.Run, 0, len(page.Items))}
	for _, run := range page.Items {
		body.Items = append(body.Items, toAPIRun(run))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListRuns200JSONResponse(body), nil
}

func (h *Handler) GetRun(
	ctx context.Context,
	request api.GetRunRequestObject,
) (api.GetRunResponseObject, error) {
	run, err := h.authorizedRun(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	return api.GetRun200JSONResponse(toAPIRun(run)), nil
}

func (h *Handler) CancelRun(
	ctx context.Context,
	request api.CancelRunRequestObject,
) (api.CancelRunResponseObject, error) {
	if _, err := h.authorizedRun(ctx, request.RunID); err != nil {
		return nil, err
	}

	cancelled, err := h.service.Cancel(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	if h.recorder != nil {
		actor := httpx.MustCurrentUser(ctx)
		actorID := actor.UserID
		project := cancelled.ProjectID
		h.recorder.Record(ctx, audit.Entry{
			Action:     audit.ActionRunCancelled,
			ActorID:    &actorID,
			ActorEmail: actor.Email,
			Subject:    cancelled.ID.String(),
			ProjectID:  &project,
			Detail:     map[string]any{"target": cancelled.TargetURL},
		})
	}

	return api.CancelRun200JSONResponse(toAPIRun(cancelled)), nil
}

func (h *Handler) ListRunResults(
	ctx context.Context,
	request api.ListRunResultsRequestObject,
) (api.ListRunResultsResponseObject, error) {
	if _, err := h.authorizedRun(ctx, request.RunID); err != nil {
		return nil, err
	}

	filter := ResultFilter{}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Status != nil {
		filter.Status = ResultStatus(*request.Params.Status)
	}

	page, err := h.service.Results(ctx, request.RunID, filter)
	if err != nil {
		return nil, err
	}

	counts, err := h.service.StatusCounts(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	body := api.RunResultPage{Items: make([]api.RunResult, 0, len(page.Items))}
	for _, result := range page.Items {
		body.Items = append(body.Items, toAPIResult(result))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}

	tally := map[string]int{}
	for status, total := range counts {
		tally[string(status)] = total
	}
	body.Counts = &tally

	return api.ListRunResults200JSONResponse(body), nil
}

func (h *Handler) ListRunCommands(
	ctx context.Context,
	request api.ListRunCommandsRequestObject,
) (api.ListRunCommandsResponseObject, error) {
	if _, err := h.authorizedRun(ctx, request.RunID); err != nil {
		return nil, err
	}

	commands, err := h.service.Commands(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	body := api.RunCommandList{Items: make([]api.RunCommand, 0, len(commands))}
	for _, command := range commands {
		body.Items = append(body.Items, toAPICommand(command))
	}
	return api.ListRunCommands200JSONResponse(body), nil
}

func (h *Handler) GetRunTrend(
	ctx context.Context,
	request api.GetRunTrendRequestObject,
) (api.GetRunTrendResponseObject, error) {
	days := 30
	if request.Params.Days != nil {
		days = *request.Params.Days
	}

	points, err := h.service.Trend(ctx, request.ProjectID, time.Duration(days)*24*time.Hour)
	if err != nil {
		return nil, err
	}

	body := api.RunTrend{Items: make([]api.RunTrendPoint, 0, len(points))}
	for _, point := range points {
		body.Items = append(body.Items, api.RunTrendPoint{
			RunId:      point.RunID,
			At:         point.At,
			Status:     api.RunStatus(point.Status),
			Total:      point.Total,
			Passed:     point.Passed,
			Failed:     point.Failed,
			Flaky:      point.Flaky,
			Skipped:    point.Skipped,
			DurationMs: intPtr(int(point.Duration.Milliseconds())),
			PassRate:   point.PassRate,
		})
	}
	return api.GetRunTrend200JSONResponse(body), nil
}

func (h *Handler) GetProjectDashboard(
	ctx context.Context,
	request api.GetProjectDashboardRequestObject,
) (api.GetProjectDashboardResponseObject, error) {
	dashboard, err := h.service.DashboardFor(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	return api.GetProjectDashboard200JSONResponse(api.ProjectDashboard{
		Requirements:  dashboard.Requirements,
		TestCases:     dashboard.TestCases,
		ApprovedCases: dashboard.ApprovedCases,
		TestFiles:     dashboard.TestFiles,
		Runs:          dashboard.Runs,
		Passed:        dashboard.Passed,
		Failed:        dashboard.Failed,
		Flaky:         dashboard.Flaky,
	}), nil
}

func (h *Handler) GetTestCaseHistory(
	ctx context.Context,
	request api.GetTestCaseHistoryRequestObject,
) (api.GetTestCaseHistoryResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}

	history, err := h.service.History(ctx, request.TestCaseID, limit)
	if err != nil {
		return nil, err
	}

	// Authorisation by way of the runs the results belong to: a case a caller cannot
	// see has no history they can read either. An empty history is indistinguishable
	// from one they are not a member of, which is the same rule as everywhere else.
	if len(history) > 0 {
		actor := httpx.MustCurrentUser(ctx)

		// One check, on the newest run: every result for a case belongs to the same
		// project as the case itself, so membership of that project is the whole
		// question. Checking each row would be the same check N times.
		run, err := h.service.Get(ctx, history[0].RunID)
		if err != nil {
			return nil, err
		}
		if err := h.projects.EnsureMember(ctx, actor, run.ProjectID); err != nil {
			return nil, err
		}
	}

	body := api.CaseHistory{Items: make([]api.CaseHistoryEntry, 0, len(history))}
	for _, entry := range history {
		body.Items = append(body.Items, api.CaseHistoryEntry{
			RunId:      entry.RunID,
			Status:     api.RunResultStatus(entry.Status),
			DurationMs: intPtr(int(entry.Duration.Milliseconds())),
			Attempt:    entry.Attempt,
			At:         entry.CreatedAt,
		})
	}
	return api.GetTestCaseHistory200JSONResponse(body), nil
}

// authorizedRun loads a run and checks the caller is a member of its project.
//
// The run routes are not under a project path parameter, so the membership
// middleware has not run for them: the check belongs here rather than being
// scattered through each handler body.
func (h *Handler) authorizedRun(ctx context.Context, runID uuid.UUID) (Run, error) {
	actor := httpx.MustCurrentUser(ctx)

	run, err := h.service.Get(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	if err := h.projects.EnsureMember(ctx, actor, run.ProjectID); err != nil {
		// Not visible is reported as not found, so a caller cannot enumerate runs in
		// projects they are not in.
		return Run{}, apierr.RunNotFound()
	}
	return run, nil
}

func toAPIRun(run Run) api.Run {
	body := api.Run{
		Id:          run.ID,
		ProjectId:   run.ProjectID,
		TargetUrl:   run.TargetURL,
		Trigger:     api.RunTrigger(run.Trigger),
		Status:      api.RunStatus(run.Status),
		Framework:   api.TestFramework(run.Framework),
		Image:       run.Image,
		Kind:        runKindPtr(run.Kind),
		Total:       run.Total,
		Passed:      run.Passed,
		Failed:      run.Failed,
		Flaky:       run.Flaky,
		Skipped:     run.Skipped,
		Quarantined: intPtr(run.Quarantined),
		DurationMs:  intPtr(int(run.Duration.Milliseconds())),
		Error:       strPtr(run.Error),
		HasLog:      boolPtr(run.LogKey != ""),
		CreatedAt:   run.CreatedAt,
	}

	body.JobId = run.JobID
	body.TriggeredBy = run.TriggeredBy
	body.StartedAt = run.StartedAt
	body.FinishedAt = run.FinishedAt
	return body
}

// ListRunArtifacts lists the recordings a run left behind.
//
// A signed URL when the storage driver can sign one, and a platform path either way:
// the local-disk driver cannot sign, and an artifact nobody can fetch is an artifact
// that was not captured (BE-7.5.2).
func (h *Handler) ListRunArtifacts(
	ctx context.Context,
	request api.ListRunArtifactsRequestObject,
) (api.ListRunArtifactsResponseObject, error) {
	if _, err := h.authorizedRun(ctx, request.RunID); err != nil {
		return nil, err
	}

	artifacts, err := h.service.ArtifactsFor(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	items := make([]api.RunArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		item := api.RunArtifact{
			ResultId:     artifact.ResultID,
			TestName:     artifact.TestName,
			Attempt:      artifact.Attempt,
			Kind:         api.RunArtifactKind(artifact.Kind),
			ContentType:  strPtr(artifact.ContentType),
			DownloadPath: downloadPath(artifact),
		}
		if signed := h.service.SignedArtifactURL(ctx, artifact.Key); signed != "" {
			item.Url = &signed
		}
		items = append(items, item)
	}

	return api.ListRunArtifacts200JSONResponse(api.RunArtifactList{Items: items}), nil
}

// GetRunMetrics returns a performance run's measured series.
func (h *Handler) GetRunMetrics(
	ctx context.Context,
	request api.GetRunMetricsRequestObject,
) (api.GetRunMetricsResponseObject, error) {
	if _, err := h.authorizedRun(ctx, request.RunID); err != nil {
		return nil, err
	}

	metrics, found, err := h.service.MetricsFor(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierr.NoPerformanceMetrics()
	}

	requests := metrics.Requests
	duration := metrics.DurationMs
	return api.GetRunMetrics200JSONResponse(api.PerformanceMetrics{
		RunId:        request.RunID,
		Requests:     requests,
		Throughput:   metrics.Throughput,
		ErrorRate:    metrics.ErrorRate,
		LatencyAvgMs: &metrics.LatencyAvgMs,
		LatencyP50Ms: &metrics.LatencyP50Ms,
		LatencyP90Ms: &metrics.LatencyP90Ms,
		LatencyP95Ms: &metrics.LatencyP95Ms,
		LatencyP99Ms: &metrics.LatencyP99Ms,
		LatencyMaxMs: &metrics.LatencyMaxMs,
		VirtualUsers: &metrics.VirtualUsers,
		DurationMs:   &duration,
	}), nil
}

// DownloadRunArtifact streams one recording.// DownloadRunArtifact streams one recording.
func (h *Handler) DownloadRunArtifact(
	ctx context.Context,
	request api.DownloadRunArtifactRequestObject,
) (api.DownloadRunArtifactResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	result, err := h.service.Result(ctx, request.ResultID)
	if err != nil {
		return nil, err
	}

	projectID, err := h.service.ProjectFor(ctx, result.RunID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureMember(ctx, actor, projectID); err != nil {
		// Reported as not found, so a caller cannot enumerate another project's runs by
		// asking for their artifacts.
		return nil, apierr.RunResultNotFound()
	}

	artifact, body, err := h.service.OpenArtifact(ctx, request.ResultID, string(request.Kind))
	if err != nil {
		return nil, err
	}

	return &artifactDownload{ctx: ctx, artifact: artifact, body: body}, nil
}

// downloadPath is the platform route that streams an artifact, which is what a client
// uses when the storage driver cannot sign a URL.
func downloadPath(artifact Artifact) string {
	return fmt.Sprintf("/api/v1/run-results/%s/artifacts/%s", artifact.ResultID, artifact.Kind)
}

// artifactDownload writes the file and closes the object-store reader on the same path
// that opened it.
type artifactDownload struct {
	ctx      context.Context
	artifact Artifact
	body     io.ReadCloser
}

func (d *artifactDownload) VisitDownloadRunArtifactResponse(w http.ResponseWriter) error {
	defer func() {
		if err := d.body.Close(); err != nil {
			slog.WarnContext(d.ctx, "close an artifact stream",
				"result_id", d.artifact.ResultID, "kind", d.artifact.Kind, "error", err)
		}
	}()

	w.Header().Set("Content-Type", d.artifact.ContentType)

	// inline rather than attachment: a video and a screenshot are meant to be watched
	// and looked at in the browser. A trace is a zip and downloads anyway.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename=%q", filenameFor(d.artifact)))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, d.body); err != nil {
		// The response is already committed, so there is nothing to report to the
		// client: a dropped connection mid-video is the common cause.
		slog.DebugContext(d.ctx, "stream an artifact",
			"result_id", d.artifact.ResultID, "error", err)
	}
	return nil
}

// filenameFor names the download after the test rather than after a key, so a saved
// file is recognisable a week later.
func filenameFor(artifact Artifact) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, artifact.TestName)

	if len(name) > 60 {
		name = name[:60]
	}
	if name == "" {
		name = "artifact"
	}

	extension := map[string]string{
		ArtifactVideo:      "webm",
		ArtifactTrace:      "zip",
		ArtifactScreenshot: "png",
		"log":              "txt",
	}[artifact.Kind]
	if extension == "" {
		extension = "bin"
	}

	return fmt.Sprintf("%s-attempt%d-%s.%s", name, artifact.Attempt, artifact.Kind, extension)
}

func runKindPtr(kind Kind) *api.RunKind {
	if kind == "" {
		return nil
	}
	value := api.RunKind(kind)
	return &value
}

func toAPIResult(result Result) api.RunResult {
	body := api.RunResult{
		Id:             result.ID,
		RunId:          result.RunID,
		Name:           result.Name,
		Status:         api.RunResultStatus(result.Status),
		DurationMs:     intPtr(int(result.Duration.Milliseconds())),
		Attempt:        result.Attempt,
		FailureMessage: strPtr(result.FailureMessage),
		Quarantined:    boolPtr(result.Quarantined),
		Artifacts: &api.RunResultArtifacts{
			HasLog:        boolPtr(result.LogKey != ""),
			HasScreenshot: boolPtr(result.ScreenshotKey != ""),
			HasVideo:      boolPtr(result.VideoKey != ""),
			HasTrace:      boolPtr(result.TraceKey != ""),
		},
	}
	body.TestCaseId = result.TestCaseID
	body.TestFileId = result.TestFileID
	return body
}

func toAPICommand(command Command) api.RunCommand {
	body := api.RunCommand{
		Id:            command.ID,
		Command:       command.Command,
		DurationMs:    intPtr(int(command.Duration.Milliseconds())),
		OutputExcerpt: strPtr(command.OutputExcerpt),
		At:            command.At,
	}
	body.ExitCode = command.ExitCode
	return body
}

func intPtr(value int) *int       { return &value }
func strPtr(value string) *string { return &value }
func boolPtr(value bool) *bool    { return &value }

// ------------------------------------------------------------- quarantine

// ListQuarantines returns the tests a project excuses (BE-7.6).
func (h *Handler) ListQuarantines(
	ctx context.Context,
	request api.ListQuarantinesRequestObject,
) (api.ListQuarantinesResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureMember(ctx, actor, request.ProjectID); err != nil {
		return nil, err
	}

	includeReleased := false
	if request.Params.IncludeReleased != nil {
		includeReleased = *request.Params.IncludeReleased
	}

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}

	quarantines, err := h.service.Quarantines(ctx, request.ProjectID, includeReleased, limit)
	if err != nil {
		return nil, err
	}

	// The review age is read once for the project rather than per row: it is one
	// setting, and asking for it per quarantine would be a query per line of a list.
	policy, err := h.service.QuarantinePolicyFor(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	items := make([]api.Quarantine, 0, len(quarantines))
	for _, quarantine := range quarantines {
		items = append(items, toAPIQuarantine(quarantine, policy.MaxAge))
	}

	return api.ListQuarantines200JSONResponse(api.QuarantineList{Items: items}), nil
}

// QuarantineTest excuses a test by hand.
//
// The caller becomes its owner, because a manual quarantine is somebody's decision and
// recording it without a name would lose the one fact that makes it reviewable.
func (h *Handler) QuarantineTest(
	ctx context.Context,
	request api.QuarantineTestRequestObject,
) (api.QuarantineTestResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(request.Body.TestName)
	if name == "" {
		return nil, apierr.Validation(
			"A quarantine needs the name of the test it excuses.",
			map[string]any{"field": "testName"})
	}

	reason := "Quarantined by hand."
	if request.Body.Reason != nil && strings.TrimSpace(*request.Body.Reason) != "" {
		reason = strings.TrimSpace(*request.Body.Reason)
	}

	quarantine, err := h.service.QuarantineTest(ctx, QuarantineInput{
		ProjectID: request.ProjectID,
		TestName:  name,
		Reason:    reason,
		Source:    QuarantineManual,
		OwnerID:   &actor.UserID,
	})
	if err != nil {
		return nil, err
	}

	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionTestQuarantined,
		Subject:   quarantine.ID.String(),
		ProjectID: &request.ProjectID,
		Detail:    map[string]any{"test": name},
	})

	policy, err := h.service.QuarantinePolicyFor(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	return api.QuarantineTest201JSONResponse(toAPIQuarantine(quarantine, policy.MaxAge)), nil
}

// AssignQuarantineOwner records who is answerable for a quarantine.
func (h *Handler) AssignQuarantineOwner(
	ctx context.Context,
	request api.AssignQuarantineOwnerRequestObject,
) (api.AssignQuarantineOwnerResponseObject, error) {
	actor, quarantine, err := h.authorizedQuarantine(ctx, request.QuarantineID)
	if err != nil {
		return nil, err
	}

	updated, err := h.service.AssignQuarantineOwner(ctx, quarantine.ID, request.Body.OwnerId)
	if err != nil {
		return nil, err
	}

	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionQuarantineOwned,
		Subject:   updated.ID.String(),
		ProjectID: &updated.ProjectID,
		Detail:    map[string]any{"owner_id": request.Body.OwnerId.String(), "test": updated.TestName},
	})

	policy, err := h.service.QuarantinePolicyFor(ctx, updated.ProjectID)
	if err != nil {
		return nil, err
	}

	return api.AssignQuarantineOwner200JSONResponse(toAPIQuarantine(updated, policy.MaxAge)), nil
}

// ReleaseQuarantine ends a quarantine, keeping the row.
func (h *Handler) ReleaseQuarantine(
	ctx context.Context,
	request api.ReleaseQuarantineRequestObject,
) (api.ReleaseQuarantineResponseObject, error) {
	actor, quarantine, err := h.authorizedQuarantine(ctx, request.QuarantineID)
	if err != nil {
		return nil, err
	}

	note := ""
	if request.Body != nil && request.Body.Note != nil {
		note = strings.TrimSpace(*request.Body.Note)
	}

	released, err := h.service.ReleaseQuarantine(ctx, quarantine.ID, actor.UserID, note)
	if err != nil {
		return nil, err
	}

	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionQuarantineEnded,
		Subject:   released.ID.String(),
		ProjectID: &released.ProjectID,
		Detail:    map[string]any{"test": released.TestName, "note": note},
	})

	policy, err := h.service.QuarantinePolicyFor(ctx, released.ProjectID)
	if err != nil {
		return nil, err
	}

	return api.ReleaseQuarantine200JSONResponse(toAPIQuarantine(released, policy.MaxAge)), nil
}

// authorizedQuarantine loads a quarantine and checks the caller is in its project.
func (h *Handler) authorizedQuarantine(
	ctx context.Context,
	id uuid.UUID,
) (httpx.Principal, Quarantine, error) {
	actor := httpx.MustCurrentUser(ctx)

	quarantine, err := h.service.Quarantine(ctx, id)
	if err != nil {
		return actor, Quarantine{}, err
	}
	if err := h.projects.EnsureMember(ctx, actor, quarantine.ProjectID); err != nil {
		// Reported as not found, so a caller cannot enumerate another project's
		// quarantines by asking for them one ID at a time.
		return actor, Quarantine{}, apierr.QuarantineNotFound()
	}
	return actor, quarantine, nil
}

// toAPIQuarantine renders a quarantine, with the two numbers that make a review list
// actionable: how old it is, and whether that is too old.
func toAPIQuarantine(quarantine Quarantine, maxAge time.Duration) api.Quarantine {
	body := api.Quarantine{
		Id:          quarantine.ID,
		ProjectId:   quarantine.ProjectID,
		TestName:    quarantine.TestName,
		TestCaseId:  quarantine.TestCaseID,
		Reason:      strPtr(quarantine.Reason),
		FlakeCount:  intPtr(quarantine.FlakeCount),
		WindowRuns:  intPtr(quarantine.WindowRuns),
		Source:      api.QuarantineSource(quarantine.Source),
		OwnerId:     quarantine.OwnerID,
		AgeHours:    int(quarantine.Age().Hours()),
		Stale:       quarantine.Stale(maxAge),
		Active:      quarantine.Active(),
		ReleaseNote: strPtr(quarantine.ReleaseNote),
		CreatedAt:   quarantine.CreatedAt,
	}
	body.LastFlakedAt = quarantine.LastFlakedAt
	body.ReleasedAt = quarantine.ReleasedAt
	return body
}
