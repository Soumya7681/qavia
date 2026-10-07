package defects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Promotion (BE-5.6) and duplicate linking (BE-5.7).
//
// One API call has to turn an analysed failure into a defect that a developer can
// pick up: title and description from the analysis, every artifact linked rather
// than copied, and the test case and requirement carried through so the defect knows
// what it broke.
//
// Two rules make it safe to press twice:
//
//   - **One defect per failure.** The unique index on run_result_id is what enforces
//     it; this code returns the existing defect rather than racing it.
//   - **One defect per recurring cause.** The same test failing the same way in a
//     later run links to the open defect instead of filing a second one. The match is
//     a hash lookup, not a model call, so it answers the same way every time.

// Analysis is the slice of an analysis this package needs to write a defect from it.
//
// Declared here rather than imported, so the defect tracker does not depend on the
// analysis package: a defect filed by hand needs none of this.
type Analysis struct {
	ID        uuid.UUID
	Reason    string
	RootCause string
	Fix       string
}

// PromoteInput is a failure being turned into a defect.
type PromoteInput struct {
	ProjectID   uuid.UUID
	RunResultID uuid.UUID

	TestCaseID    *uuid.UUID
	RequirementID *uuid.UUID

	// TestName is the failing test's name, used for the title when the analysis has
	// no reason worth using.
	TestName string

	Analysis Analysis

	Severity   Severity
	AssigneeID *uuid.UUID
	CreatedBy  *uuid.UUID
}

// Promotion is the outcome, including whether anything was created.
type Promotion struct {
	Defect Defect

	// Existing is true when this failure had already been promoted. The caller returns
	// 200 rather than 201, and the UI says "already filed" instead of filing again.
	Existing bool

	// DuplicateOf is set when this defect was linked to an earlier open one for the
	// same cause. The defect is still created: an occurrence that leaves no row is an
	// occurrence nobody can count.
	DuplicateOf *uuid.UUID
}

// Promote files a defect from an analysed failure.
func (s *Service) Promote(ctx context.Context, input PromoteInput) (Promotion, error) {
	if strings.TrimSpace(input.Analysis.RootCause) == "" {
		// Promoting an unanalysed failure produces a defect with no root cause, and
		// somebody has to work it out again from scratch. The order matters.
		return Promotion{}, apierr.AnalysisNotReady()
	}

	// Checked before the insert as well as being enforced by the index, because the
	// answer a caller wants is the existing defect, not a constraint violation.
	existing, err := s.db.Queries().DefectForResult(ctx, &input.RunResultID)
	if err == nil {
		defect, convertErr := toDefect(existing)
		if convertErr != nil {
			return Promotion{}, convertErr
		}
		return Promotion{Defect: defect, Existing: true, DuplicateOf: defect.DuplicateOf}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Promotion{}, apierr.Internal(fmt.Errorf("look for an existing defect: %w", err))
	}

	severity := input.Severity
	if severity == "" {
		severity = SeverityMedium
	}

	created, err := s.Create(ctx, CreateInput{
		ProjectID:     input.ProjectID,
		Title:         promotedTitle(input),
		Description:   promotedDescription(input),
		Severity:      severity,
		RunResultID:   &input.RunResultID,
		TestCaseID:    input.TestCaseID,
		RequirementID: input.RequirementID,
		AnalysisID:    &input.Analysis.ID,
		AssigneeID:    input.AssigneeID,
		CreatedBy:     input.CreatedBy,
		RootCause:     input.Analysis.RootCause,
		TestName:      input.TestName,
	})
	if err != nil {
		return Promotion{}, err
	}

	promotion := Promotion{Defect: created}

	// Duplicate detection runs after the row exists, so the occurrence is recorded
	// whichever way the match goes.
	original, found, err := s.findOriginal(ctx, created)
	if err != nil {
		return Promotion{}, err
	}
	if found {
		linked, err := s.LinkDuplicate(ctx, created.ID, &original, input.CreatedBy)
		if err != nil {
			return Promotion{}, err
		}
		promotion.Defect = linked
		promotion.DuplicateOf = &original
	}

	return promotion, nil
}

// findOriginal looks for an open defect with the same cause on the same test.
//
// Identity is the test plus the cause, and both halves are required. The same root
// cause on a different test is usually a different bug — a 500 from two endpoints is
// two problems — and merging them would hide one behind the other, which is the more
// expensive mistake.
//
// The test is identified by its case when the platform mapped one, and by its name
// otherwise. A generated file covering several cases produces results left
// deliberately unmapped, and those failures recur like any other.
func (s *Service) findOriginal(ctx context.Context, candidate Defect) (uuid.UUID, bool, error) {
	if candidate.RootCauseKey == "" {
		return uuid.Nil, false, nil
	}

	var (
		row dbgen.Defect
		err error
	)
	switch {
	case candidate.TestCaseID != nil:
		row, err = s.db.Queries().OpenDefectWithRootCause(ctx,
			dbgen.OpenDefectWithRootCauseParams{
				ProjectID:    candidate.ProjectID,
				TestCaseID:   candidate.TestCaseID,
				RootCauseKey: candidate.RootCauseKey,
			})
	case candidate.TestName != "":
		row, err = s.db.Queries().OpenDefectWithRootCauseByName(ctx,
			dbgen.OpenDefectWithRootCauseByNameParams{
				ProjectID:    candidate.ProjectID,
				TestName:     candidate.TestName,
				RootCauseKey: candidate.RootCauseKey,
			})
	default:
		return uuid.Nil, false, nil
	}

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, apierr.Internal(fmt.Errorf("look for a duplicate: %w", err))
	}
	if row.ID == candidate.ID {
		// The query found the defect that was just created. Nothing to link.
		return uuid.Nil, false, nil
	}

	return row.ID, true, nil
}

// promotedTitle is what a developer sees in a list.
//
// The analysis's one-line reason, because that is the sentence that says what is
// wrong. The test's name is the fallback: it is at least specific.
func promotedTitle(input PromoteInput) string {
	reason := strings.TrimSpace(input.Analysis.Reason)
	if reason == "" {
		reason = strings.TrimSpace(input.TestName)
	}
	if reason == "" {
		reason = "A test failed and the analysis produced no summary"
	}

	// One line, capped: a title that wraps three times in a list is a title nobody
	// reads.
	if index := strings.IndexAny(reason, "\n\r"); index > 0 {
		reason = reason[:index]
	}
	if len(reason) > 160 {
		reason = strings.TrimSpace(reason[:157]) + "…"
	}
	return reason
}

// promotedDescription is the body, assembled from the analysis rather than written
// by it: the sections are the platform's, so every promoted defect reads the same
// way and a developer knows where to look.
func promotedDescription(input PromoteInput) string {
	body := &strings.Builder{}

	fmt.Fprintf(body, "**Failing test**\n\n%s\n\n", orDash(input.TestName))
	fmt.Fprintf(body, "**Root cause**\n\n%s\n\n", orDash(input.Analysis.RootCause))

	if fix := strings.TrimSpace(input.Analysis.Fix); fix != "" {
		// Labelled as a suggestion, because it is one: nothing in this platform writes
		// to a client's repository, and a suggested fix that reads like an instruction
		// invites somebody to apply it unread.
		fmt.Fprintf(body, "**Suggested fix (not applied)**\n\n%s\n\n", fix)
	}

	fmt.Fprintf(body, "Promoted from a failing test result. The analysis, logs, and "+
		"artifacts are linked to this defect rather than copied into it, so they stay "+
		"the originals.\n")

	return body.String()
}

func orDash(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "—"
	}
	return trimmed
}
