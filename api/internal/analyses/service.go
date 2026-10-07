package analyses

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service owns analyses, their evidence, and the feedback on them.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Analysis is one explanation of one failure.
type Analysis struct {
	ID          uuid.UUID
	RunResultID uuid.UUID

	Reason       string
	RootCause    string
	SuggestedFix string
	Evidence     Evidence

	RelatedCommit string

	// Stability is nil when no permitted signal was available. Nil is not zero: zero
	// would claim the test is perfectly unstable, and the honest answer is "the
	// platform cannot tell yet" (BE-5.4.2).
	Stability *decimal.Decimal

	// StabilityBasis says which signals produced the score, so the number is
	// explainable rather than merely present.
	StabilityBasis string

	PromptVersion string
	ModelName     string
	CreatedAt     time.Time
}

// Result is the failure an analysis is about, as this package needs it.
type Result struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	TestCaseID *uuid.UUID
	TestFileID *uuid.UUID

	Name           string
	Status         string
	FailureMessage string
	Attempt        int

	LogKey        string
	ScreenshotKey string
	VideoKey      string
}

// SaveInput is a validated analysis, ready to store.
type SaveInput struct {
	RunResultID uuid.UUID

	Reason       string
	RootCause    string
	SuggestedFix string
	Evidence     Evidence

	RelatedCommit string
	PromptVersion string
	ModelName     string
}

// Save writes an analysis after its evidence has been checked.
//
// The check is a separate call rather than something this method does, because the
// caller is the only one holding the artifacts: it fetched the log to build the
// prompt, and re-fetching it here would double the object-store traffic per
// analysis. What Save enforces is the floor: an analysis with no evidence never
// reaches the table, whatever the caller did or did not check (BE-5.3.1).
func (s *Service) Save(ctx context.Context, input SaveInput) (Analysis, error) {
	if len(input.Evidence) == 0 {
		return Analysis{}, apierr.AnalysisWithoutEvidence([]string{ErrNoEvidence.Error()})
	}
	if strings.TrimSpace(input.RootCause) == "" {
		return Analysis{}, apierr.Validation("An analysis needs a root cause.", nil)
	}

	encoded, err := input.Evidence.Encode()
	if err != nil {
		return Analysis{}, apierr.Internal(err)
	}

	row, err := s.db.Queries().CreateAnalysis(ctx, dbgen.CreateAnalysisParams{
		RunResultID:   input.RunResultID,
		Reason:        input.Reason,
		RootCause:     input.RootCause,
		SuggestedFix:  input.SuggestedFix,
		Evidence:      encoded,
		RelatedCommit: input.RelatedCommit,
		PromptVersion: input.PromptVersion,
		ModelName:     input.ModelName,

		// Written in a second statement, once the signals have been counted: the score
		// is the platform's arithmetic, not part of what the agent returned.
		StabilityScore: nil,
	})
	if err != nil {
		return Analysis{}, apierr.Internal(fmt.Errorf("store the analysis: %w", err))
	}

	return toAnalysis(row)
}

// Get reads one analysis.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Analysis, error) {
	row, err := s.db.Queries().GetAnalysis(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Analysis{}, apierr.AnalysisNotFound()
		}
		return Analysis{}, apierr.Internal(fmt.Errorf("read the analysis: %w", err))
	}
	return toAnalysis(row)
}

// Latest is what a failure's page shows: a re-analysis under a newer prompt is a new
// row, and the newest one wins.
func (s *Service) Latest(ctx context.Context, resultID uuid.UUID) (Analysis, bool, error) {
	row, err := s.db.Queries().LatestAnalysisForResult(ctx, resultID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not an error: most failures have not been analysed yet, and that is a
			// state the UI renders rather than a problem.
			return Analysis{}, false, nil
		}
		return Analysis{}, false, apierr.Internal(fmt.Errorf("read the analysis: %w", err))
	}

	analysis, err := toAnalysis(row)
	if err != nil {
		return Analysis{}, false, err
	}
	return analysis, true, nil
}

// ForRun lists every analysis produced for one run.
func (s *Service) ForRun(ctx context.Context, runID uuid.UUID) ([]Analysis, error) {
	rows, err := s.db.Queries().ListAnalysesForRun(ctx, runID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list analyses: %w", err))
	}

	out := make([]Analysis, 0, len(rows))
	for _, row := range rows {
		analysis, err := toAnalysis(row)
		if err != nil {
			return nil, err
		}
		out = append(out, analysis)
	}
	return out, nil
}

// FailingResults are the results of a run worth explaining.
//
// Flaky results are included on purpose: "why is this flaky" is the question a
// reviewer actually has, and a flake with no explanation is the failure mode that
// gets a suite ignored (F-7.11).
func (s *Service) FailingResults(ctx context.Context, runID uuid.UUID) ([]Result, error) {
	rows, err := s.db.Queries().FailingResultsForRun(ctx, runID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list failing results: %w", err))
	}

	out := make([]Result, 0, len(rows))
	for _, row := range rows {
		out = append(out, toResult(row))
	}
	return out, nil
}

// Result reads one failure.
func (s *Service) Result(ctx context.Context, id uuid.UUID) (Result, error) {
	row, err := s.db.Queries().GetRunResult(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, apierr.RunResultNotFound()
		}
		return Result{}, apierr.Internal(fmt.Errorf("read the run result: %w", err))
	}
	return toResult(row), nil
}

// ProjectForResult is the project a failure belongs to, resolved through its run.
//
// It exists so a caller holding only a result ID can scope an authorisation check or
// a settings read without learning the run tables.
func (s *Service) ProjectForResult(ctx context.Context, resultID uuid.UUID) (uuid.UUID, error) {
	result, err := s.Result(ctx, resultID)
	if err != nil {
		return uuid.Nil, err
	}

	row, err := s.db.Queries().GetRun(ctx, result.RunID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, apierr.RunNotFound()
		}
		return uuid.Nil, apierr.Internal(fmt.Errorf("read the run: %w", err))
	}
	return row.ProjectID, nil
}

// Feedback is one person's verdict on an analysis (BE-5.8).
type Feedback struct {
	AnalysisID    uuid.UUID
	UserID        uuid.UUID
	Helpful       bool
	Note          string
	PromptVersion string
	CreatedAt     time.Time
}

// RecordFeedback stores a thumb up or down, replacing that user's previous vote.
//
// The prompt version is copied onto the row rather than joined at read time, because
// the question this answers is "how did prompt v3 do", and the analysis may be
// re-analysed under a later version.
func (s *Service) RecordFeedback(
	ctx context.Context,
	analysisID, userID uuid.UUID,
	helpful bool,
	note string,
) error {
	analysis, err := s.Get(ctx, analysisID)
	if err != nil {
		return err
	}

	if err := s.db.Queries().UpsertAnalysisFeedback(ctx, dbgen.UpsertAnalysisFeedbackParams{
		AnalysisID:    analysisID,
		UserID:        userID,
		Helpful:       helpful,
		Note:          note,
		PromptVersion: analysis.PromptVersion,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("store the feedback: %w", err))
	}
	return nil
}

// ClearFeedback removes a vote, because a person who changes their mind should not
// have to leave a wrong answer behind.
func (s *Service) ClearFeedback(ctx context.Context, analysisID, userID uuid.UUID) error {
	if err := s.db.Queries().DeleteAnalysisFeedback(ctx, dbgen.DeleteAnalysisFeedbackParams{
		AnalysisID: analysisID, UserID: userID,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("clear the feedback: %w", err))
	}
	return nil
}

// MyFeedback reads one person's vote, so the UI can show which thumb is lit.
func (s *Service) MyFeedback(
	ctx context.Context,
	analysisID, userID uuid.UUID,
) (Feedback, bool, error) {
	row, err := s.db.Queries().GetAnalysisFeedback(ctx, dbgen.GetAnalysisFeedbackParams{
		AnalysisID: analysisID, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Feedback{}, false, nil
		}
		return Feedback{}, false, apierr.Internal(fmt.Errorf("read the feedback: %w", err))
	}

	return Feedback{
		AnalysisID:    row.AnalysisID,
		UserID:        row.UserID,
		Helpful:       row.Helpful,
		Note:          row.Note,
		PromptVersion: row.PromptVersion,
		CreatedAt:     row.CreatedAt,
	}, true, nil
}

// PromptScore is how one prompt version has been received.
type PromptScore struct {
	PromptVersion string
	Helpful       int
	Unhelpful     int
}

// ByPromptVersion is the export that makes prompt iteration an argument about
// numbers rather than impressions.
func (s *Service) ByPromptVersion(ctx context.Context) ([]PromptScore, error) {
	rows, err := s.db.Queries().FeedbackByPromptVersion(ctx)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read feedback by prompt version: %w", err))
	}

	scores := make([]PromptScore, 0, len(rows))
	for _, row := range rows {
		scores = append(scores, PromptScore{
			PromptVersion: row.PromptVersion,
			Helpful:       int(row.Helpful),
			Unhelpful:     int(row.Unhelpful),
		})
	}
	return scores, nil
}

func toAnalysis(row dbgen.Analysis) (Analysis, error) {
	evidence, err := DecodeEvidence(row.Evidence)
	if err != nil {
		return Analysis{}, apierr.Internal(err)
	}

	return Analysis{
		ID:             row.ID,
		RunResultID:    row.RunResultID,
		Reason:         row.Reason,
		RootCause:      row.RootCause,
		SuggestedFix:   row.SuggestedFix,
		Evidence:       evidence,
		RelatedCommit:  row.RelatedCommit,
		Stability:      row.StabilityScore,
		StabilityBasis: basisFor(row.StabilityScore),
		PromptVersion:  row.PromptVersion,
		ModelName:      row.ModelName,
		CreatedAt:      row.CreatedAt,
	}, nil
}

// basisFor states why a score is present or absent. The absent case is the one that
// matters: a UI showing a blank cell needs a reason to put in the tooltip.
func basisFor(score *decimal.Decimal) string {
	if score == nil {
		return "no signal: this test has no repeat history, and no repository is connected"
	}
	return "computed from repeat behaviour across attempts and recent runs"
}

func toResult(row dbgen.RunResult) Result {
	return Result{
		ID:             row.ID,
		RunID:          row.RunID,
		TestCaseID:     row.TestCaseID,
		TestFileID:     row.TestFileID,
		Name:           row.Name,
		Status:         string(row.Status),
		FailureMessage: row.FailureMessage,
		Attempt:        int(row.Attempt),
		LogKey:         row.LogKey,
		ScreenshotKey:  row.ScreenshotKey,
		VideoKey:       row.VideoKey,
	}
}
