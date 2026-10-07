package comprehension

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/repos"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// The comprehension stage (BE-6.4).
//
// It clones, explores, verifies, and stores. The clone is its own because a map is
// about a revision: reusing a checkout from an earlier job would produce a map
// labelled with one commit and describing another.

// TypeComprehend is the stage name the pipeline declares.
const TypeComprehend = jobs.TypeRepoComprehend

// Repos is the slice of the repository service this stage needs.
type Repos interface {
	Materialise(ctx context.Context, projectID uuid.UUID, ref string) (repos.Checkout, error)
	DetectedStack(ctx context.Context, projectID uuid.UUID) (repos.Stack, bool, error)
	StoreMap(ctx context.Context, input repos.MapInput) (uuid.UUID, error)
}

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
}

// Deps is everything the stage shares.
type Deps struct {
	Repos    Repos
	Gateway  Gateway
	Settings Settings
}

// ComprehendHandler maps a project's repository.
type ComprehendHandler struct {
	deps     Deps
	explorer *Explorer
}

func NewComprehendHandler(deps Deps) *ComprehendHandler {
	return &ComprehendHandler{deps: deps, explorer: NewExplorer(deps.Gateway)}
}

func (h *ComprehendHandler) Type() string { return TypeComprehend }

// Payload names the project to map.
type Payload struct {
	ProjectID uuid.UUID `json:"projectId"`
	Ref       string    `json:"ref,omitempty"`

	// RequestID is the per-request nonce the idempotency key is built from, for the
	// same reason a sync has one: mapping a repository again is work somebody asks
	// for, and keying on the project would make the second request find the first
	// one's row and push nothing.
	RequestID uuid.UUID `json:"requestId"`
}

func (h *ComprehendHandler) IdempotencyKey(payload Payload) string {
	return ComprehendIdempotencyKey(payload)
}

// ComprehendIdempotencyKey is exported so the API can declare the type as
// enqueue-only without building a handler it cannot run.
func ComprehendIdempotencyKey(payload Payload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("comprehend:%s:%s", payload.ProjectID, payload.Ref)
	}
	return fmt.Sprintf("comprehend:%s", payload.RequestID)
}

func (h *ComprehendHandler) Handle(
	ctx context.Context,
	payload Payload,
	jc jobs.JobContext,
) error {
	checkout, err := h.deps.Repos.Materialise(ctx, payload.ProjectID, payload.Ref)
	if err != nil {
		return err
	}
	defer func() {
		// Removed on every path out. A map is a small document; the checkout it came
		// from is not, and holding one open is how a disk fills (BE-6.2.1).
		if err := checkout.Close(); err != nil {
			slog.WarnContext(ctx, "remove the workspace",
				"project_id", payload.ProjectID, "error", err)
		}
	}()

	stack, _, err := h.deps.Repos.DetectedStack(ctx, payload.ProjectID)
	if err != nil {
		return err
	}

	budget, err := h.budget(ctx, payload.ProjectID)
	if err != nil {
		return err
	}

	jc.Event("Exploring %s at %s, up to %d steps",
		stack.Summary(), shortCommit(checkout.Commit), budget.Steps)

	started := time.Now()
	jobID := jc.JobID()

	produced, err := h.explorer.Explore(ctx, checkout.Space,
		CallFor(payload.ProjectID, jobID),
		stack.Summary(), checkout.Commit, budget,
		func(step int, action, detail string) {
			// Every step is in the job log, because an exploration is a model choosing
			// what to read twenty times and a reader has to be able to follow it.
			jc.Event("Step %d: %s %s", step, action, detail)
			jc.Progress(step * 90 / max(budget.Steps, 1))
		})
	if err != nil {
		return err
	}

	// Files the map named and the repository does not have are dropped here, while the
	// checkout still exists to check against. A map listing a plausible file that is
	// not there is worse than a shorter map, because somebody writes a test for it.
	if dropped := produced.Verify(checkout.Space); len(dropped) > 0 {
		jc.Event("Dropped %d named file(s) that do not exist at this revision: %v",
			len(dropped), dropped)
	}

	mapID, err := h.deps.Repos.StoreMap(ctx, repos.MapInput{
		ProjectID: payload.ProjectID,
		Commit:    checkout.Commit,
		Stack:     stack.Summary(),
		Document:  produced,
		Steps:     produced.Steps,
		CutShort:  produced.CutShort,
		JobID:     &jobID,
	})
	if err != nil {
		return err
	}

	ending := "finished"
	if produced.CutShort {
		// Said plainly: a map produced because the budget ran out describes less than
		// one the agent chose to finish, and a reader should know which they have.
		ending = "cut short by the step budget"
	}

	jc.Event("Mapped %d controller(s), %d service(s), %d data-access site(s) and "+
		"%d untested path(s) in %d step(s), %s, in %s (map %s)",
		len(produced.Controllers), len(produced.Services), len(produced.DataAccess),
		len(produced.Uncovered), produced.Steps, ending,
		time.Since(started).Round(time.Second), mapID)

	if len(produced.Unknowns) > 0 {
		jc.Event("Unknowns: %v", produced.Unknowns)
	}

	jc.Progress(100)
	return nil
}

// budget reads how many steps an exploration may take.
//
// From the model's own declared `max_tool_iterations` where the platform knows it,
// because the limit that matters is the one the model can actually sustain, and a
// platform-wide constant would be wrong for every model in one direction or the
// other.
func (h *ComprehendHandler) budget(ctx context.Context, projectID uuid.UUID) (Budget, error) {
	steps, err := h.deps.Settings.Int(ctx, "repo.max_exploration_steps",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return Budget{}, apierr.Internal(fmt.Errorf("read the exploration budget: %w", err))
	}
	return Budget{Steps: steps}, nil
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	if commit == "" {
		return "an unknown revision"
	}
	return commit
}
