package jobs

import (
	"fmt"
	"strconv"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// ToAPI maps a job to its response shape.
//
// The payload and the idempotency key are deliberately absent: they are how the
// platform runs the work, not what a client asked about, and a payload can hold a
// storage key or an internal identifier that has no business in a response
// (backend-standards.md 4).
func ToAPI(job Job) api.Job {
	out := api.Job{
		Id:          job.ID,
		Type:        job.Type,
		Status:      api.JobStatus(job.Status),
		Progress:    job.Progress,
		Attempts:    job.Attempts,
		MaxAttempts: job.MaxAttempts,
		QueuedAt:    job.QueuedAt,
	}

	if chain := job.Chain(); chain != "" {
		value := api.JobChain(chain)
		out.Chain = &value
	}
	if job.ProjectID != nil {
		out.ProjectId.Set(*job.ProjectID)
	}
	if job.ParentJobID != nil {
		out.ParentJobId.Set(*job.ParentJobID)
	}
	if job.Error != nil {
		out.Error.Set(*job.Error)
	}
	if job.StartedAt != nil {
		out.StartedAt.Set(*job.StartedAt)
	}
	if job.FinishedAt != nil {
		out.FinishedAt.Set(*job.FinishedAt)
	}

	if len(job.Stages) > 0 {
		stages := make([]api.Job, 0, len(job.Stages))
		for _, child := range job.Stages {
			stages = append(stages, ToAPI(child))
		}
		out.Stages = &stages
	}

	if len(job.Events) > 0 {
		events := make([]api.JobEvent, 0, len(job.Events))
		for _, event := range job.Events {
			events = append(events, ToAPIEvent(event))
		}
		out.Events = &events
	}

	return out
}

// ToAPIEvent maps one log line.
func ToAPIEvent(event Event) api.JobEvent {
	return api.JobEvent{
		Id:      strconv.FormatInt(event.ID, 10),
		Level:   api.JobEventLevel(event.Level),
		Message: event.Message,
		At:      event.At,
	}
}

// toReference is what a 202 carries: enough to subscribe, and nothing that
// encourages polling the whole job (backend-standards.md 11).
func toReference(job Job) api.JobReference {
	eventsURL := fmt.Sprintf("/api/v1/jobs/%s/events", job.ID)
	return api.JobReference{
		JobId:     job.ID,
		Status:    api.JobStatus(job.Status),
		EventsUrl: &eventsURL,
	}
}
