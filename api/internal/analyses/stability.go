package analyses

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// The stability score (BE-5.4).
//
// This number is the most tempting place in the platform to let a model guess. It
// looks like a judgement call, a model will produce one instantly, and nobody would
// notice for months. So the rule is absolute: **the score is arithmetic over stored
// data, and nothing else is permitted to produce it.**
//
// Two signals, both recomputable from the database at any time:
//
//	repeat    the proportion of stored attempts of this test that passed, across
//	          this run's retries and the project's recent runs
//	churn     how much of the test's recent history is a status change rather than
//	          a steady result: a test that alternates is unstable even when it
//	          passes half the time
//
// score = repeat * (1 - churn), clamped to [0, 1], rounded to three decimals.
//
// Read it as "how much this result can be trusted": 1.000 is a test that has always
// done the same thing, and a low score means the test's own behaviour varies. When
// there is no repeat history at all the score is **null**, with a stated reason,
// because one observation is not a measurement (BE-5.4.2).
//
// The formula is here, in code, and in the API description, so that substituting a
// model's number later would require deleting this comment first.

// historyWindow is how many recent results of the same test the score looks at. Ten
// is enough to show alternation and short enough that a fix a month ago does not
// hold a test's score down forever.
const historyWindow = 10

// Stability is a computed score and the signals behind it.
type Stability struct {
	// Score is nil when there was no repeat history to measure.
	Score *decimal.Decimal

	// Attempts and Observations are what the score was computed from, so the number
	// is explainable.
	Attempts     int
	Observations int
	Passed       int
	Changes      int

	Basis string
}

// StabilityFor computes the score for one test in one project.
//
// Both signals come from run_results, which is why this lives next to the analysis
// rather than in the agent: the agent's job is to explain the failure, and this is a
// property of the test's history.
func (s *Service) StabilityFor(
	ctx context.Context,
	projectID uuid.UUID,
	runID uuid.UUID,
	testName string,
) (Stability, error) {
	attempts, err := s.db.Queries().ResultAttempts(ctx, dbgen.ResultAttemptsParams{
		RunID: runID, Name: testName,
	})
	if err != nil {
		return Stability{}, apierr.Internal(fmt.Errorf("read this run's attempts: %w", err))
	}

	history, err := s.db.Queries().ResultHistoryByName(ctx, dbgen.ResultHistoryByNameParams{
		ProjectID: projectID,
		Name:      testName,
		Limit:     historyWindow,
	})
	if err != nil {
		return Stability{}, apierr.Internal(fmt.Errorf("read this test's history: %w", err))
	}

	stability := Stability{Attempts: len(attempts), Observations: len(history)}

	// Statuses newest first from the query; reversed here so "changed from the
	// previous run" reads in the direction time runs.
	statuses := make([]string, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		statuses = append(statuses, string(history[index].Status))
	}

	for _, status := range statuses {
		if status == string(passedStatus) {
			stability.Passed++
		}
	}
	for index := 1; index < len(statuses); index++ {
		if statuses[index] != statuses[index-1] {
			stability.Changes++
		}
	}

	// One observation is not a measurement. A test seen once has no repeat history,
	// whatever it did, so the field stays null and the reason is stated.
	if len(statuses) < 2 && len(attempts) < 2 {
		stability.Basis = "no signal: this test has been seen once, so there is nothing to compare"
		return stability, nil
	}

	repeat := decimal.NewFromInt(1)
	if len(statuses) > 0 {
		repeat = decimal.NewFromInt(int64(stability.Passed)).
			Div(decimal.NewFromInt(int64(len(statuses))))
	}

	churn := decimal.Zero
	if len(statuses) > 1 {
		churn = decimal.NewFromInt(int64(stability.Changes)).
			Div(decimal.NewFromInt(int64(len(statuses) - 1)))
	}

	// Retries inside one run are the sharpest signal there is: a test that failed and
	// then passed in the same run, against the same target, in the same container, is
	// unstable by definition. It caps the score rather than nudging it.
	if flipped(attempts) {
		capped := decimal.NewFromFloat(0.5)
		if repeat.GreaterThan(capped) {
			repeat = capped
		}
	}

	score := repeat.Mul(decimal.NewFromInt(1).Sub(churn)).Round(3)
	if score.LessThan(decimal.Zero) {
		score = decimal.Zero
	}
	if score.GreaterThan(decimal.NewFromInt(1)) {
		score = decimal.NewFromInt(1)
	}

	stability.Score = &score
	stability.Basis = fmt.Sprintf(
		"%d of %d recent results passed with %d status change(s), across %d attempt(s) in this run",
		stability.Passed, len(statuses), stability.Changes, len(attempts))

	return stability, nil
}

// passedStatus is the enum value a pass is stored as. Named rather than inlined so a
// schema change breaks the build here instead of quietly making every test look
// unstable.
const passedStatus = dbgen.RunResultStatusPassed

// flipped reports whether a test changed status between attempts of one run.
func flipped(attempts []dbgen.ResultAttemptsRow) bool {
	for index := 1; index < len(attempts); index++ {
		if attempts[index].Status != attempts[index-1].Status {
			return true
		}
	}
	return false
}

// ApplyStability writes the computed score onto an analysis.
//
// Separate from Save because it depends on data the agent never sees, and because a
// score can be recomputed later without producing a second analysis: the same
// explanation with a better-founded number is not a new explanation.
func (s *Service) ApplyStability(
	ctx context.Context,
	analysisID uuid.UUID,
	stability Stability,
) error {
	if err := s.db.Queries().SetStabilityScore(ctx, dbgen.SetStabilityScoreParams{
		ID:             analysisID,
		StabilityScore: stability.Score,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("store the stability score: %w", err))
	}
	return nil
}

// HistoryLength is how many past results exist for a test, which is what an
// evidence reference to run history is checked against (BE-5.3.2).
func (s *Service) HistoryLength(
	ctx context.Context,
	projectID uuid.UUID,
	testName string,
) (int, error) {
	history, err := s.db.Queries().ResultHistoryByName(ctx, dbgen.ResultHistoryByNameParams{
		ProjectID: projectID,
		Name:      testName,
		Limit:     historyWindow,
	})
	if err != nil {
		return 0, apierr.Internal(fmt.Errorf("read this test's history: %w", err))
	}
	return len(history), nil
}

// HistoryPoint is one past result of the same test, for the prompt and for the UI.
type HistoryPoint struct {
	Status  string
	Attempt int
	At      time.Time
}

// History returns the recent behaviour of one test by name.
//
// By name rather than by test case, because a result the platform could not map to
// exactly one case still has a history, and that history is exactly what explains a
// flake.
func (s *Service) History(
	ctx context.Context,
	projectID uuid.UUID,
	testName string,
) ([]HistoryPoint, error) {
	rows, err := s.db.Queries().ResultHistoryByName(ctx, dbgen.ResultHistoryByNameParams{
		ProjectID: projectID,
		Name:      testName,
		Limit:     historyWindow,
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read this test's history: %w", err))
	}

	points := make([]HistoryPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, HistoryPoint{
			Status:  string(row.Status),
			Attempt: int(row.Attempt),
			At:      row.CreatedAt,
		})
	}
	return points, nil
}
