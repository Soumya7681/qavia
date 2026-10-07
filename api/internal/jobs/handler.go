package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Stream tuning.
//
// The heartbeat keeps proxies from closing an idle connection, which is the
// classic cause of a job view that silently stops updating. The poll is a safety
// net rather than the mechanism: notifications drive the stream, and this only
// covers the window around a listener reconnect, so it is deliberately slower than
// the five seconds NFR-3 asks for.
const (
	streamHeartbeat = 15 * time.Second
	streamPoll      = 10 * time.Second
	streamEventPage = 200
)

// Handler implements the jobs slice of the generated server interface.
type Handler struct {
	service *Service
	hub     *Hub
}

func NewHandler(service *Service, hub *Hub) *Handler {
	return &Handler{service: service, hub: hub}
}

func (h *Handler) SubmitJob(
	ctx context.Context,
	request api.SubmitJobRequestObject,
) (api.SubmitJobResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := SubmitInput{
		ProjectID:  request.ProjectID,
		Chain:      Chain(request.Body.Chain),
		ArtifactID: request.Body.ArtifactId,
	}
	if request.Params.IdempotencyKey != nil {
		input.IdempotencyKey = *request.Params.IdempotencyKey
	}

	job, err := h.service.Submit(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	return api.SubmitJob202JSONResponse(toReference(job)), nil
}

func (h *Handler) ListJobs(
	ctx context.Context,
	request api.ListJobsRequestObject,
) (api.ListJobsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}

	page, err := h.service.List(ctx, actor, request.ProjectID, limit, cursor)
	if err != nil {
		return nil, err
	}

	body := api.JobPage{Items: make([]api.Job, 0, len(page.Items))}
	for _, job := range page.Items {
		body.Items = append(body.Items, ToAPI(job))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListJobs200JSONResponse(body), nil
}

func (h *Handler) GetJob(
	ctx context.Context,
	request api.GetJobRequestObject,
) (api.GetJobResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	job, err := h.service.Get(ctx, actor, request.JobID)
	if err != nil {
		return nil, err
	}
	return api.GetJob200JSONResponse(ToAPI(job)), nil
}

func (h *Handler) CancelJob(
	ctx context.Context,
	request api.CancelJobRequestObject,
) (api.CancelJobResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	job, err := h.service.Cancel(ctx, actor, request.JobID)
	if err != nil {
		return nil, err
	}
	return api.CancelJob200JSONResponse(ToAPI(job)), nil
}

// StreamJobEvents opens the SSE stream.
//
// Access is checked here, before the stream object is built, so an unauthorised
// caller gets a normal JSON error rather than an event stream that says no.
func (h *Handler) StreamJobEvents(
	ctx context.Context,
	request api.StreamJobEventsRequestObject,
) (api.StreamJobEventsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	job, err := h.service.Get(ctx, actor, request.JobID)
	if err != nil {
		return nil, err
	}

	var lastEventID int64
	if request.Params.LastEventID != nil {
		// An unreadable value resumes from the beginning rather than failing: a
		// reconnect that has to send the whole log again is a slow recovery, and
		// refusing it is no recovery at all.
		if parsed, parseErr := strconv.ParseInt(*request.Params.LastEventID, 10, 64); parseErr == nil {
			lastEventID = parsed
		}
	}

	return &eventStream{
		ctx:         ctx,
		service:     h.service,
		hub:         h.hub,
		actor:       actor,
		job:         job,
		lastEventID: lastEventID,
	}, nil
}

// eventStream writes server-sent events until the job finishes or the client
// leaves.
//
// It satisfies the generated response interface directly rather than using the
// generated text/event-stream response, which takes an io.Reader and would buffer:
// a stream that arrives in one lump when the job ends is not a live view.
type eventStream struct {
	ctx     context.Context
	service *Service
	hub     *Hub
	actor   httpx.Principal

	job         Job
	lastEventID int64
}

func (s *eventStream) VisitStreamJobEventsResponse(w http.ResponseWriter) error {
	flusher, streamable := w.(http.Flusher)
	if !streamable {
		return fmt.Errorf("jobs: response writer does not support streaming")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which turns a live stream into a
	// single delivery at the end.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribed before the first read, so a change that lands between the read and
	// the subscription still wakes this stream instead of being missed.
	changed, unsubscribe := s.hub.Subscribe(s.job.ID)
	defer unsubscribe()

	if err := s.drain(w, flusher); err != nil {
		return err
	}
	if s.job.Status.Terminal() {
		// Nothing further will happen. Closing immediately is kinder than holding a
		// connection open for a job that finished last week.
		return writeEvent(w, flusher, "", "done", terminalPayload(s.job))
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()

	poll := time.NewTicker(streamPoll)
	defer poll.Stop()

	for {
		select {
		case <-s.ctx.Done():
			// The client disconnected or the server is shutting down. Returning here
			// runs the deferred unsubscribe, which is what stops this leaking a
			// subscription per abandoned tab.
			return nil

		case <-changed:
			if err := s.drain(w, flusher); err != nil {
				return err
			}
			if s.job.Status.Terminal() {
				return writeEvent(w, flusher, "", "done", terminalPayload(s.job))
			}

		case <-poll.C:
			if err := s.drain(w, flusher); err != nil {
				return err
			}
			if s.job.Status.Terminal() {
				return writeEvent(w, flusher, "", "done", terminalPayload(s.job))
			}

		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

// drain sends the current status and every event the client has not seen.
//
// Status first, then events, and always both: a late joiner needs the state to
// render anything at all, and a reconnecting client needs the gap filled from
// Last-Event-ID rather than from now.
func (s *eventStream) drain(w http.ResponseWriter, flusher http.Flusher) error {
	job, err := s.service.Get(s.ctx, s.actor, s.job.ID)
	if err != nil {
		// The job was deleted, or the database is unreachable. Either way the
		// stream is over; the error goes to the log through the caller.
		return err
	}
	s.job = job

	if err := writeEvent(w, flusher, "", "status", statusPayload(job)); err != nil {
		return err
	}

	for {
		events, err := s.service.Events(s.ctx, s.actor, job.ID, s.lastEventID, streamEventPage)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}

		for _, event := range events {
			if err := writeEvent(w, flusher,
				strconv.FormatInt(event.ID, 10), "event", eventPayload(event)); err != nil {
				return err
			}
			s.lastEventID = event.ID
		}

		if len(events) < streamEventPage {
			return nil
		}
	}
}

// writeEvent renders one SSE frame.
//
// The id field is what a browser sends back as Last-Event-ID on reconnect, which
// is why the job_events primary key is a bigserial: the ordering and the resume
// cursor are the same number.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, id, name string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("jobs: encode stream payload: %w", err)
	}

	// A write failure means the client went away mid-frame. That ends the stream
	// and is not a server error, so it is reported as a clean finish rather than
	// logged as a failure on every closed tab.
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return nil //nolint:nilerr // a disconnected client is a normal end of stream
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body); err != nil {
		return nil //nolint:nilerr // a disconnected client is a normal end of stream
	}
	flusher.Flush()
	return nil
}

type statusFrame struct {
	JobID    uuid.UUID `json:"jobId"`
	Status   Status    `json:"status"`
	Progress int       `json:"progress"`
	Attempts int       `json:"attempts"`
	Error    *string   `json:"error,omitempty"`
	Stages   []stage   `json:"stages,omitempty"`
}

type stage struct {
	JobID    uuid.UUID `json:"jobId"`
	Type     string    `json:"type"`
	Status   Status    `json:"status"`
	Progress int       `json:"progress"`
}

type eventFrame struct {
	ID      string     `json:"id"`
	Level   EventLevel `json:"level"`
	Message string     `json:"message"`
	At      time.Time  `json:"at"`
}

func statusPayload(job Job) statusFrame {
	frame := statusFrame{
		JobID:    job.ID,
		Status:   job.Status,
		Progress: job.Progress,
		Attempts: job.Attempts,
		Error:    job.Error,
	}
	for _, child := range job.Stages {
		frame.Stages = append(frame.Stages, stage{
			JobID:    child.ID,
			Type:     child.Type,
			Status:   child.Status,
			Progress: child.Progress,
		})
	}
	return frame
}

func terminalPayload(job Job) statusFrame { return statusPayload(job) }

func eventPayload(event Event) eventFrame {
	return eventFrame{
		ID:      strconv.FormatInt(event.ID, 10),
		Level:   event.Level,
		Message: event.Message,
		At:      event.At,
	}
}
