package generation

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/testcases"
)

// nearMissCandidates bounds what the dedupe agent is shown.
//
// The candidate set is deliberately narrow — the cases already stored for the same
// requirement — because that is where a near miss actually happens, and comparing
// every new case against every stored one would turn a bounded cost into a
// quadratic one (F-5.4).
const nearMissCandidates = 25

// verdict is the dedupe agent's answer.
type verdict struct {
	Duplicates []struct {
		CandidateIndex   int    `json:"candidate_index"`
		DuplicateOfIndex int    `json:"duplicate_of_index"`
		Reason           string `json:"reason"`
	} `json:"duplicates"`
}

// mergeNearMisses asks the cheap tier whether newly written cases duplicate
// something already stored, and records the merges.
//
// It runs after the deterministic pass rather than instead of it: exact duplicates
// were already refused by a hash for free, and this only sees what survived that
// (BE-2.8). A failure here is not a failure of the generation: the cases are
// written, and an unmerged near miss is a review nuisance rather than lost work.
func mergeNearMisses(
	ctx context.Context,
	deps Deps,
	projectID uuid.UUID,
	requirementID uuid.UUID,
	jobID uuid.UUID,
	written []testcases.TestCase,
) int {
	if len(written) == 0 {
		return 0
	}

	stored, err := deps.TestCases.ForRequirement(ctx, requirementID)
	if err != nil {
		slog.WarnContext(ctx, "load stored cases for dedupe", "error", err)
		return 0
	}

	// Only the cases that were already there before this batch are candidates to
	// have been duplicated.
	fresh := make(map[uuid.UUID]struct{}, len(written))
	for _, item := range written {
		fresh[item.ID] = struct{}{}
	}

	existing := make([]testcases.TestCase, 0, len(stored))
	for _, item := range stored {
		if _, isNew := fresh[item.ID]; isNew {
			continue
		}
		existing = append(existing, item)
		if len(existing) >= nearMissCandidates {
			break
		}
	}
	if len(existing) == 0 {
		return 0
	}

	result, err := deps.Gateway.Dedupe(ctx, llm.AgentCall{
		ProjectID: &projectID,
		JobID:     &jobID,
	}, forDedupe(existing), forDedupe(written))
	if err != nil {
		slog.WarnContext(ctx, "near-miss dedupe unavailable", "error", err)
		return 0
	}

	var answer verdict
	if err := json.Unmarshal(result.Raw, &answer); err != nil {
		slog.WarnContext(ctx, "decode dedupe verdict", "error", err)
		return 0
	}

	var merged int
	for _, duplicate := range answer.Duplicates {
		if duplicate.CandidateIndex < 0 || duplicate.CandidateIndex >= len(written) {
			continue
		}
		if duplicate.DuplicateOfIndex < 0 || duplicate.DuplicateOfIndex >= len(existing) {
			continue
		}

		loser := written[duplicate.CandidateIndex]
		winner := existing[duplicate.DuplicateOfIndex]

		// The newer case is the one superseded: the stored one may already have been
		// reviewed or approved, and replacing it would throw that away.
		if err := deps.TestCases.Supersede(ctx, loser.ID, winner.ID); err != nil {
			slog.WarnContext(ctx, "record merge", "test_case_id", loser.ID, "error", err)
			continue
		}
		merged++
	}
	return merged
}

// forDedupe renders cases as the agent's input: a title and the assertion, and
// nothing else. The prompt is a yes-or-no question about two short strings, and
// sending steps and preconditions would cost tokens to make it harder.
func forDedupe(cases []testcases.TestCase) []any {
	out := make([]any, 0, len(cases))
	for _, item := range cases {
		out = append(out, map[string]any{
			"title": item.Title,
			// The stored assertion is not a column: the title is what a reviewer
			// reads, and the expected result is what distinguishes two cases that
			// look alike.
			"assertion": item.Expected,
		})
	}
	return out
}
