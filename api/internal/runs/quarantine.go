package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Flake quarantine (BE-7.6, F-7.12).
//
// A test that fails one run in four teaches a team to ignore red, and a team that
// ignores red has no test suite whatever the dashboard says. Quarantine is the
// alternative, and its three rules are the whole feature:
//
//  1. **It is automatic**, above a threshold, because a policy that needs somebody to
//     notice and act is a policy that runs when somebody has time.
//  2. **A quarantined test still runs and still records results.** Skipping it would
//     hide whether it ever recovers, and recovery is the only way a quarantine ends
//     well. What changes is that its failure no longer fails the run.
//  3. **It has an owner and an age**, so it cannot become permanent silently. A list of
//     quarantines with nobody's name on it is a suite that stopped testing things and
//     did not tell anyone.

// Quarantine is one excused test.
type Quarantine struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	TestName   string
	TestCaseID *uuid.UUID

	// Reason is the sentence a person reads, and the two numbers are the arithmetic
	// behind it: this many flaky runs out of this many looked at. Stored rather than
	// recomputed, because the threshold can change and the decision that was actually
	// made should still be explainable.
	Reason     string
	FlakeCount int
	WindowRuns int

	// Source is auto or manual.
	Source string

	OwnerID *uuid.UUID

	ReleasedAt  *time.Time
	ReleasedBy  *uuid.UUID
	ReleaseNote string

	LastFlakedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Age is how long the quarantine has stood, which is the number that makes a review
// list actionable: a fortnight is a decision, a year is an abandonment.
func (q Quarantine) Age() time.Duration {
	if q.ReleasedAt != nil {
		return q.ReleasedAt.Sub(q.CreatedAt)
	}
	return time.Since(q.CreatedAt)
}

// Active reports whether the quarantine is still excusing failures.
func (q Quarantine) Active() bool { return q.ReleasedAt == nil }

// Stale reports whether the quarantine has stood longer than the configured limit
// without being claimed or released.
//
// Not enforced by expiring it: releasing a quarantine automatically would turn a
// forgotten flaky test into a suddenly red suite, which is the failure mode this
// feature exists to prevent. It is surfaced instead (BE-7.6.3).
func (q Quarantine) Stale(limit time.Duration) bool {
	return q.Active() && limit > 0 && q.Age() > limit
}

// Sources a quarantine can have.
const (
	QuarantineAuto   = "auto"
	QuarantineManual = "manual"
)

// QuarantinePolicy is what the settings say.
type QuarantinePolicy struct {
	// Enabled turns automatic quarantine off for an installation that would rather
	// see the failures.
	Enabled bool

	// Threshold is how many of the recent runs a test may flake in before it is
	// quarantined, and Window is how many runs are looked at.
	Threshold int
	Window    int

	// MaxAge is how long a quarantine may stand before it is called stale.
	MaxAge time.Duration
}

// QuarantinePolicyFor reads the policy for a project.
func (s *Service) QuarantinePolicyFor(
	ctx context.Context,
	projectID uuid.UUID,
) (QuarantinePolicy, error) {
	scope := settings.Target{ProjectID: &projectID}

	enabled, err := s.settings.Bool(ctx, "quarantine.auto", scope)
	if err != nil {
		return QuarantinePolicy{}, apierr.Internal(fmt.Errorf("read the quarantine switch: %w", err))
	}
	threshold, err := s.settings.Int(ctx, "quarantine.flake_threshold", scope)
	if err != nil {
		return QuarantinePolicy{}, apierr.Internal(fmt.Errorf("read the flake threshold: %w", err))
	}
	window, err := s.settings.Int(ctx, "quarantine.window_runs", scope)
	if err != nil {
		return QuarantinePolicy{}, apierr.Internal(fmt.Errorf("read the flake window: %w", err))
	}
	maxAge, err := s.settings.Duration(ctx, "quarantine.max_age", scope)
	if err != nil {
		return QuarantinePolicy{}, apierr.Internal(fmt.Errorf("read the quarantine age limit: %w", err))
	}

	return QuarantinePolicy{
		Enabled:   enabled,
		Threshold: threshold,
		Window:    window,
		MaxAge:    maxAge,
	}, nil
}

// ActiveQuarantines is the set of test names a project currently excuses.
//
// Returned as a set rather than a list because that is how the execute stage uses it:
// once per result, on every run, and a linear scan per result on a four-hundred-test
// suite is four hundred scans for no reason.
func (s *Service) ActiveQuarantines(
	ctx context.Context,
	projectID uuid.UUID,
) (map[string]uuid.UUID, error) {
	rows, err := s.db.Queries().ActiveQuarantines(ctx, projectID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the quarantine list: %w", err))
	}

	excused := make(map[string]uuid.UUID, len(rows))
	for _, row := range rows {
		excused[row.TestName] = row.ID
	}
	return excused, nil
}

// Quarantines lists a project's quarantines for a person to review.
func (s *Service) Quarantines(
	ctx context.Context,
	projectID uuid.UUID,
	includeReleased bool,
	limit int,
) ([]Quarantine, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.db.Queries().ListQuarantines(ctx, dbgen.ListQuarantinesParams{
		ProjectID:       projectID,
		IncludeReleased: &includeReleased,
		PageSize:        int32(limit), //nolint:gosec // Bounded just above.
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list quarantines: %w", err))
	}

	quarantines := make([]Quarantine, 0, len(rows))
	for _, row := range rows {
		quarantines = append(quarantines, toQuarantine(row))
	}
	return quarantines, nil
}

// Quarantine is one row by ID.
func (s *Service) Quarantine(ctx context.Context, id uuid.UUID) (Quarantine, error) {
	row, err := s.db.Queries().GetQuarantine(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Quarantine{}, apierr.QuarantineNotFound()
		}
		return Quarantine{}, apierr.Internal(fmt.Errorf("read the quarantine: %w", err))
	}
	return toQuarantine(row), nil
}

// QuarantineInput is one decision to excuse a test.
type QuarantineInput struct {
	ProjectID  uuid.UUID
	TestName   string
	TestCaseID *uuid.UUID

	Reason     string
	FlakeCount int
	WindowRuns int

	Source  string
	OwnerID *uuid.UUID
}

// QuarantineTest excuses a test, or refreshes the arithmetic on one already excused.
//
// Idempotent on the live row: a later run that finds the same test still flaking
// updates the counts and the last-flaked time rather than creating a second
// quarantine, so the age stays the age of the decision.
func (s *Service) QuarantineTest(
	ctx context.Context,
	input QuarantineInput,
) (Quarantine, error) {
	if input.TestName == "" {
		return Quarantine{}, apierr.Validation(
			"A quarantine needs the name of the test it excuses.",
			map[string]any{"field": "testName"})
	}

	source := input.Source
	if source != QuarantineManual {
		source = QuarantineAuto
	}

	row, err := s.db.Queries().QuarantineTest(ctx, dbgen.QuarantineTestParams{
		ProjectID:  input.ProjectID,
		TestName:   input.TestName,
		TestCaseID: input.TestCaseID,
		Reason:     input.Reason,
		FlakeCount: int32(input.FlakeCount), //nolint:gosec // Bounded by the window.
		WindowRuns: int32(input.WindowRuns), //nolint:gosec // Bounded by the window setting.
		Source:     source,
		OwnerID:    input.OwnerID,
	})
	if err != nil {
		return Quarantine{}, apierr.Internal(fmt.Errorf("quarantine a test: %w", err))
	}
	return toQuarantine(row), nil
}

// AssignQuarantineOwner records who is answerable for a quarantine.
func (s *Service) AssignQuarantineOwner(
	ctx context.Context,
	id, ownerID uuid.UUID,
) (Quarantine, error) {
	row, err := s.db.Queries().AssignQuarantineOwner(ctx, dbgen.AssignQuarantineOwnerParams{
		ID: id, OwnerID: &ownerID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either it does not exist or it is already released, and the two are the same
			// answer to the caller: there is nothing live to assign.
			return Quarantine{}, apierr.QuarantineNotFound()
		}
		return Quarantine{}, apierr.Internal(fmt.Errorf("assign a quarantine owner: %w", err))
	}
	return toQuarantine(row), nil
}

// ReleaseQuarantine ends a quarantine, keeping the row.
//
// Kept rather than deleted, because the history of what used to be flaky is what tells
// somebody whether a fix held.
func (s *Service) ReleaseQuarantine(
	ctx context.Context,
	id, actorID uuid.UUID,
	note string,
) (Quarantine, error) {
	row, err := s.db.Queries().ReleaseQuarantine(ctx, dbgen.ReleaseQuarantineParams{
		ID: id, ReleasedBy: &actorID, ReleaseNote: note,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Quarantine{}, apierr.QuarantineNotFound()
		}
		return Quarantine{}, apierr.Internal(fmt.Errorf("release a quarantine: %w", err))
	}
	return toQuarantine(row), nil
}

// FlakeCandidate is a test and its flake arithmetic over the recent runs.
type FlakeCandidate struct {
	TestName  string
	FlakyRuns int
	SeenRuns  int
}

// FlakeCandidates counts how many of the recent finished runs saw each test flake.
//
// Counted from the results rather than from a stored score, because a score is a
// summary and this decision deserves the underlying facts: "three of the last ten runs"
// is a sentence somebody can check, and "stability 0.62" is not (BE-4.13, BE-5.4).
func (s *Service) FlakeCandidates(
	ctx context.Context,
	projectID uuid.UUID,
	window int,
) ([]FlakeCandidate, error) {
	if window <= 0 {
		window = 10
	}

	rows, err := s.db.Queries().FlakeCountsForProject(ctx, dbgen.FlakeCountsForProjectParams{
		ProjectID:  projectID,
		WindowRuns: int32(window), //nolint:gosec // Bounded by the window setting.
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("count flakes: %w", err))
	}

	candidates := make([]FlakeCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, FlakeCandidate{
			TestName:  row.Name,
			FlakyRuns: int(row.FlakyRuns),
			SeenRuns:  int(row.SeenRuns),
		})
	}
	return candidates, nil
}

func toQuarantine(row dbgen.Quarantine) Quarantine {
	return Quarantine{
		ID:           row.ID,
		ProjectID:    row.ProjectID,
		TestName:     row.TestName,
		TestCaseID:   row.TestCaseID,
		Reason:       row.Reason,
		FlakeCount:   int(row.FlakeCount),
		WindowRuns:   int(row.WindowRuns),
		Source:       row.Source,
		OwnerID:      row.OwnerID,
		ReleasedAt:   row.ReleasedAt,
		ReleasedBy:   row.ReleasedBy,
		ReleaseNote:  row.ReleaseNote,
		LastFlakedAt: row.LastFlakedAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}
