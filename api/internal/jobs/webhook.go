package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// WebhookPathPrefix is the one route whose raw body is kept, so the HMAC can be
// checked against the bytes as sent.
const WebhookPathPrefix = "/api/v1/hooks/"

// SubmitTriggered starts a chain on behalf of a trigger rather than a signed-in
// user.
//
// It satisfies runtrigger.Submitter. The same validation runs as for a manual
// submission, because a CI system deserves the same clear refusal a person gets:
// an archived project or an unimplemented chain fails here, at the request, rather
// than as a job that quietly never produces anything.
func (s *Service) SubmitTriggered(ctx context.Context, req runtrigger.Request) (uuid.UUID, error) {
	if _, declared := StagesFor(Chain(req.Chain)); !declared {
		return uuid.Nil, apierr.Validation(
			fmt.Sprintf("%q is not a pipeline this platform runs.", req.Chain),
			map[string]any{"field": "chain"})
	}
	if !Runnable(Chain(req.Chain)) {
		return uuid.Nil, apierr.NotImplemented(Label(Chain(req.Chain)))
	}
	if err := s.projects.EnsureActive(ctx, req.ProjectID); err != nil {
		return uuid.Nil, err
	}
	if err := s.ensureReady(ctx, Chain(req.Chain), req.ProjectID); err != nil {
		return uuid.Nil, err
	}

	stages, _ := StagesFor(Chain(req.Chain))

	parent, err := s.client.createTriggeredChain(ctx, req)
	if err != nil {
		return uuid.Nil, err
	}
	if parent.existed {
		return parent.job.ID, nil
	}

	if _, err := s.client.Enqueue(ctx, EnqueueRequest{
		Type:      stages[0],
		ProjectID: &req.ProjectID,
		Payload: StagePayload{
			Chain:       Chain(req.Chain),
			Stage:       0,
			ProjectID:   req.ProjectID,
			ArtifactID:  req.ArtifactID,
			ParentJobID: parent.job.ID,
			Reference:   req.Reference,
			RunID:       req.RunID,
		},
		ParentJobID: &parent.job.ID,
		EnqueuedBy:  req.ActorID,
	}); err != nil {
		return uuid.Nil, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:    audit.ActionRunTriggered,
		ActorID:   req.ActorID,
		Subject:   req.Chain,
		ProjectID: &req.ProjectID,
		Detail: map[string]any{
			"source": string(req.Source), "reference": req.Reference,
			"jobId": parent.job.ID.String(),
		},
	})
	return parent.job.ID, nil
}

// createTriggeredChain writes the parent row for a run that nobody submitted from
// the UI.
func (c *Client) createTriggeredChain(ctx context.Context, req runtrigger.Request) (chainCreation, error) {
	return c.createChain(ctx, SubmitInput{
		ProjectID:  req.ProjectID,
		Chain:      Chain(req.Chain),
		ArtifactID: req.ArtifactID,
		Reference:  req.Reference,
	}, uuid.Nil)
}

// WebhookHandler serves the inbound trigger endpoint.
//
// It is separate from Handler because it is the one route with no session: the
// signature is the credential, so nothing here reads a principal.
type WebhookHandler struct {
	webhook  *runtrigger.Webhook
	recorder *audit.Recorder
}

func NewWebhookHandler(webhook *runtrigger.Webhook, recorder *audit.Recorder) *WebhookHandler {
	return &WebhookHandler{webhook: webhook, recorder: recorder}
}

// TriggerWebhook verifies the delivery and starts the chain.
//
// A rejected delivery is audited with the token's project where one could be read,
// because repeated failures are how a leaked token or a misconfigured sender shows
// up, and neither is visible from a 401 alone.
func (h *WebhookHandler) TriggerWebhook(
	ctx context.Context,
	request api.TriggerWebhookRequestObject,
) (api.TriggerWebhookResponseObject, error) {
	raw := httpx.RawBody(ctx)
	if raw == nil {
		// The capture middleware is not in front of this route. That is a wiring
		// bug: verifying a re-encoded body would accept requests that were never
		// signed, so it fails closed.
		return nil, fmt.Errorf("jobs: raw body was not captured for the webhook route")
	}

	delivery := runtrigger.Delivery{
		Token:      request.Token,
		Signature:  request.Params.XQaviaSignature,
		Timestamp:  request.Params.XQaviaTimestamp,
		Body:       raw,
		Chain:      string(request.Body.Chain),
		ArtifactID: request.Body.ArtifactId,
	}
	if request.Body.Reference != nil {
		delivery.Reference = *request.Body.Reference
	}

	jobID, err := h.webhook.Accept(ctx, delivery)
	if err != nil {
		h.auditRejection(ctx, request.Token, err)
		return nil, err
	}

	eventsURL := fmt.Sprintf("/api/v1/jobs/%s/events", jobID)
	return api.TriggerWebhook202JSONResponse{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &eventsURL,
	}, nil
}

func (h *WebhookHandler) auditRejection(ctx context.Context, token string, cause error) {
	entry := audit.Entry{
		Action:  audit.ActionWebhookRejected,
		Subject: "webhook",
		Detail:  map[string]any{"reason": apierr.CodeOf(cause)},
	}
	if projectID, ok := runtrigger.ProjectFromToken(token); ok {
		entry.ProjectID = &projectID
	}
	h.recorder.Record(ctx, entry)
}
