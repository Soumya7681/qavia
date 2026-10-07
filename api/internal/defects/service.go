// Package defects is the built-in bug tracker (F-9.7 to F-9.10).
//
// It ships in phase 5 rather than with the integrations, and that is the whole
// point: an installation with no Jira, no GitHub, and no Slack still has to be able
// to file, assign, discuss, and close a bug. Everything an external tracker later
// adds is a mirror of these rows, never a replacement for them (BE-5.5).
//
// Two behaviours carry the phase:
//
//   - **Promotion.** One call turns an analysed failure into a defect with its
//     artifacts linked rather than copied, and promoting the same failure twice
//     returns the defect that already exists (BE-5.6).
//   - **Duplicate linking.** The same test failing the same way across three runs is
//     one defect with three linked occurrences, matched deterministically on the test
//     case plus a normalised root cause. The platform proposes the link; a person can
//     always undo it (BE-5.7).
package defects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service owns defects and their comment threads.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Severity and Status mirror the enums, checked before a write so a bad value is a
// validation error naming the field rather than a constraint violation surfacing as
// a 500.
type (
	Severity string
	Status   string
)

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

const (
	StatusOpen         Status = "open"
	StatusAcknowledged Status = "acknowledged"
	StatusInProgress   Status = "in_progress"
	StatusFixed        Status = "fixed"
	StatusWontFix      Status = "wont_fix"
	StatusDuplicate    Status = "duplicate"
)

func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	default:
		return false
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusAcknowledged, StatusInProgress,
		StatusFixed, StatusWontFix, StatusDuplicate:
		return true
	default:
		return false
	}
}

// Open reports whether a defect in this status is still somebody's problem.
func (s Status) Open() bool {
	switch s {
	case StatusOpen, StatusAcknowledged, StatusInProgress:
		return true
	default:
		return false
	}
}

// Defect is one bug.
type Defect struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	// Every link is optional, and every one is a reference rather than a copy: a
	// defect promoted from a failure points at that failure's artifacts instead of
	// duplicating them (BE-5.6.2).
	RunResultID   *uuid.UUID
	TestCaseID    *uuid.UUID
	RequirementID *uuid.UUID
	AnalysisID    *uuid.UUID

	Title       string
	Description string

	Severity Severity
	Status   Status

	AssigneeID *uuid.UUID

	DuplicateOf  *uuid.UUID
	RootCauseKey string

	// TestName is the failing test this defect came from. Stored because duplicate
	// detection needs "this test, failing this way", and a result the platform could
	// not map to exactly one case has a name but no case ID (BE-5.7.2).
	TestName string

	// ExternalRef is where this defect lives in somebody else's tracker, once one is
	// configured. Empty on a zero-integration install, which is the install this
	// phase has to work on.
	ExternalRef map[string]any

	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ResolvedAt *time.Time
}

// Comment is one message on a defect's thread.
type Comment struct {
	ID        uuid.UUID
	DefectID  uuid.UUID
	AuthorID  *uuid.UUID
	Body      string
	System    bool
	CreatedAt time.Time
}

// CreateInput is a defect filed by hand.
type CreateInput struct {
	ProjectID uuid.UUID

	Title       string
	Description string
	Severity    Severity

	RunResultID   *uuid.UUID
	TestCaseID    *uuid.UUID
	RequirementID *uuid.UUID
	AnalysisID    *uuid.UUID

	AssigneeID *uuid.UUID
	CreatedBy  *uuid.UUID

	// RootCause feeds duplicate matching when the defect came from an analysis.
	RootCause string

	// TestName is the failing test's name, the fallback half of the duplicate match.
	TestName string
}

// Create files a defect.
func (s *Service) Create(ctx context.Context, input CreateInput) (Defect, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Defect{}, apierr.Validation("A defect needs a title.",
			map[string]any{"field": "title"})
	}
	if input.Severity == "" {
		input.Severity = SeverityMedium
	}
	if !input.Severity.Valid() {
		return Defect{}, apierr.Validation(
			fmt.Sprintf("Severity %q is not one this platform uses.", input.Severity),
			map[string]any{"field": "severity"})
	}

	row, err := s.db.Queries().CreateDefect(ctx, dbgen.CreateDefectParams{
		ProjectID:     input.ProjectID,
		RunResultID:   input.RunResultID,
		TestCaseID:    input.TestCaseID,
		RequirementID: input.RequirementID,
		AnalysisID:    input.AnalysisID,
		Title:         title,
		Description:   input.Description,
		Severity:      dbgen.DefectSeverity(input.Severity),
		Status:        dbgen.DefectStatusOpen,
		AssigneeID:    input.AssigneeID,
		RootCauseKey:  RootCauseKey(input.RootCause),
		CreatedBy:     input.CreatedBy,
		TestName:      input.TestName,
	})
	if err != nil {
		return Defect{}, apierr.Internal(fmt.Errorf("create the defect: %w", err))
	}
	return toDefect(row)
}

// Get reads one defect.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Defect, error) {
	row, err := s.db.Queries().GetDefect(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Defect{}, apierr.DefectNotFound()
		}
		return Defect{}, apierr.Internal(fmt.Errorf("read the defect: %w", err))
	}
	return toDefect(row)
}

// UpdateInput is a partial edit. A nil field is left alone, which is what makes a
// status change and a reassignment the same endpoint without either overwriting the
// other.
type UpdateInput struct {
	Title       *string
	Description *string
	Severity    *Severity
	Status      *Status

	AssigneeID    *uuid.UUID
	ClearAssignee bool
}

// Update edits a defect.
func (s *Service) Update(ctx context.Context, id uuid.UUID, input UpdateInput) (Defect, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return Defect{}, err
	}

	params := dbgen.UpdateDefectParams{
		ID:          id,
		Title:       input.Title,
		Description: input.Description,
		AssigneeID:  input.AssigneeID,
	}
	if input.ClearAssignee {
		clear := true
		params.ClearAssignee = &clear
	}

	if input.Severity != nil {
		if !input.Severity.Valid() {
			return Defect{}, apierr.Validation(
				fmt.Sprintf("Severity %q is not one this platform uses.", *input.Severity),
				map[string]any{"field": "severity"})
		}
		params.Severity = dbgen.NullDefectSeverity{
			DefectSeverity: dbgen.DefectSeverity(*input.Severity), Valid: true,
		}
	}

	if input.Status != nil {
		if !input.Status.Valid() {
			return Defect{}, apierr.Validation(
				fmt.Sprintf("Status %q is not one this platform uses.", *input.Status),
				map[string]any{"field": "status"})
		}
		if *input.Status == StatusDuplicate && existing.DuplicateOf == nil {
			// Duplicate is a link, not a label. Setting the status without saying what
			// it duplicates leaves a defect nobody can navigate away from.
			return Defect{}, apierr.DefectNotClosable(
				"Mark a defect as a duplicate by linking it to the original, not by setting the status.")
		}
		params.Status = dbgen.NullDefectStatus{
			DefectStatus: dbgen.DefectStatus(*input.Status), Valid: true,
		}
	}

	row, err := s.db.Queries().UpdateDefect(ctx, params)
	if err != nil {
		return Defect{}, apierr.Internal(fmt.Errorf("update the defect: %w", err))
	}
	return toDefect(row)
}

// LinkDuplicate points one defect at another, or unlinks it when original is nil.
//
// Reversible on purpose: the platform proposes a link from a deterministic match,
// and a person who knows the two failures are unrelated has to be able to say so
// (BE-5.7.3).
func (s *Service) LinkDuplicate(
	ctx context.Context,
	id uuid.UUID,
	original *uuid.UUID,
	actor *uuid.UUID,
) (Defect, error) {
	if original != nil {
		if *original == id {
			return Defect{}, apierr.Validation("A defect cannot duplicate itself.", nil)
		}

		target, err := s.Get(ctx, *original)
		if err != nil {
			return Defect{}, err
		}
		if target.DuplicateOf != nil {
			// Chains would make "how many occurrences" a graph walk. The link points at
			// the original, so a duplicate of a duplicate points at the same place.
			original = target.DuplicateOf
		}

		duplicate, err := s.Get(ctx, id)
		if err != nil {
			return Defect{}, err
		}
		if duplicate.ProjectID != target.ProjectID {
			return Defect{}, apierr.Validation(
				"Two defects in different projects are not duplicates of each other.", nil)
		}
	}

	row, err := s.db.Queries().LinkDuplicate(ctx, dbgen.LinkDuplicateParams{
		ID: id, DuplicateOf: original,
	})
	if err != nil {
		return Defect{}, apierr.Internal(fmt.Errorf("link the duplicate: %w", err))
	}

	// The thread records the link, because a status that changed with no explanation
	// is the thing people file a second bug about.
	message := "Unlinked as a duplicate."
	if original != nil {
		message = fmt.Sprintf("Linked as a duplicate of %s.", *original)
	}
	if _, err := s.Comment(ctx, id, actor, message, true); err != nil {
		return Defect{}, err
	}

	return toDefect(row)
}

// Occurrences lists the defects linked to this one as duplicates: the recurrences of
// the same failure (BE-5.7).
func (s *Service) Occurrences(ctx context.Context, id uuid.UUID) ([]Defect, error) {
	rows, err := s.db.Queries().ListDuplicatesOf(ctx, &id)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list duplicates: %w", err))
	}

	out := make([]Defect, 0, len(rows))
	for _, row := range rows {
		defect, err := toDefect(row)
		if err != nil {
			return nil, err
		}
		out = append(out, defect)
	}
	return out, nil
}

// Comment appends to a defect's thread.
//
// A system comment has no author and is styled differently: "linked as a duplicate"
// is something the platform did, and attributing it to whoever clicked would be a
// small lie in a record people rely on.
func (s *Service) Comment(
	ctx context.Context,
	defectID uuid.UUID,
	author *uuid.UUID,
	body string,
	system bool,
) (Comment, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return Comment{}, apierr.Validation("A comment needs a body.", nil)
	}

	row, err := s.db.Queries().CreateDefectComment(ctx, dbgen.CreateDefectCommentParams{
		DefectID: defectID,
		AuthorID: author,
		Body:     trimmed,
		System:   system,
	})
	if err != nil {
		return Comment{}, apierr.Internal(fmt.Errorf("add the comment: %w", err))
	}

	if err := s.db.Queries().TouchDefect(ctx, defectID); err != nil {
		return Comment{}, apierr.Internal(fmt.Errorf("touch the defect: %w", err))
	}

	return Comment{
		ID:        row.ID,
		DefectID:  row.DefectID,
		AuthorID:  row.AuthorID,
		Body:      row.Body,
		System:    row.System,
		CreatedAt: row.CreatedAt,
	}, nil
}

// StatusCounts is the defect panel on a project dashboard.
func (s *Service) StatusCounts(ctx context.Context, projectID uuid.UUID) (map[Status]int, error) {
	rows, err := s.db.Queries().CountDefectsByStatus(ctx, projectID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("count defects: %w", err))
	}

	counts := make(map[Status]int, len(rows))
	for _, row := range rows {
		counts[Status(row.Status)] = int(row.Total)
	}
	return counts, nil
}

// rootCauseNoise is what a root cause says that does not identify it: specific
// numbers, ids, timestamps, and addresses. Stripped before hashing so the same
// underlying cause matches across runs where only the values differ.
var rootCauseNoise = regexp.MustCompile(
	`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}` + // uuids
		`|\d{4}-\d{2}-\d{2}[t ]\d{2}:\d{2}:\d{2}\S*` + // timestamps
		`|\b\d+(\.\d+)?(ms|s|kb|mb)?\b` + // numbers and durations
		`|0x[0-9a-f]+` + // hex
		`|https?://\S+)`) // urls

// RootCauseKey is the deterministic half of duplicate detection (BE-5.7.2).
//
// It is a hash of the root cause with the varying parts removed, so "expected 200,
// got 500 after 1204ms" and "expected 200, got 500 after 890ms" produce the same
// key. Deliberately not a model call: two failures being the same failure has to be
// answerable the same way every time, and a model asked twice is not.
//
// It is also deliberately conservative. Over-normalising would merge distinct bugs,
// which is the worse error: a missed duplicate costs somebody a minute, and a wrong
// merge hides a real problem behind an unrelated one.
func RootCauseKey(rootCause string) string {
	normalised := strings.ToLower(strings.TrimSpace(rootCause))
	if normalised == "" {
		return ""
	}

	normalised = rootCauseNoise.ReplaceAllString(normalised, " ")
	normalised = strings.Join(strings.Fields(normalised), " ")
	if normalised == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:16])
}

func toDefect(row dbgen.Defect) (Defect, error) {
	external, err := decodeExternalRef(row.ExternalRef)
	if err != nil {
		return Defect{}, apierr.Internal(err)
	}

	return Defect{
		ID:            row.ID,
		ProjectID:     row.ProjectID,
		RunResultID:   row.RunResultID,
		TestCaseID:    row.TestCaseID,
		RequirementID: row.RequirementID,
		AnalysisID:    row.AnalysisID,
		Title:         row.Title,
		Description:   row.Description,
		Severity:      Severity(row.Severity),
		Status:        Status(row.Status),
		AssigneeID:    row.AssigneeID,
		DuplicateOf:   row.DuplicateOf,
		RootCauseKey:  row.RootCauseKey,
		TestName:      row.TestName,
		ExternalRef:   external,
		CreatedBy:     row.CreatedBy,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		ResolvedAt:    row.ResolvedAt,
	}, nil
}

// SetExternalRef records where a defect landed in an external tracker (BE-10.2.3).
//
// The internal defect is the source of truth; this only adds the mirror's coordinates,
// so a defect found in Jira can be traced back and a person can click through. Storing
// it separately from Create keeps the internal write independent of the external one: a
// Jira outage cannot roll back a defect that already exists.
func (s *Service) SetExternalRef(ctx context.Context, id uuid.UUID, ref map[string]any) error {
	encoded, err := json.Marshal(ref)
	if err != nil {
		return apierr.Internal(fmt.Errorf("encode external ref: %w", err))
	}
	if err := s.db.Queries().SetDefectExternalRef(ctx, dbgen.SetDefectExternalRefParams{
		ID: id, ExternalRef: encoded,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("store external ref: %w", err))
	}
	return nil
}
