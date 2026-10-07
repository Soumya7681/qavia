// Package testcases owns generated test cases (F-5.1 to F-5.5).
//
// Two things in here carry the phase. The fingerprint is what makes re-running
// generation on an unchanged specification free: a deterministic hash of
// (method, path, assertion), enforced by a unique index rather than by a habit.
// And the bulk write is a CopyFrom: 400 cases is one statement, not 400 round
// trips inside a transaction that is now long (backend-standards.md 8).
package testcases

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// MaxBulkStatus caps a bulk approve or reject. Large enough for "approve
// everything on this screen", small enough that one request cannot hold a
// transaction open over 50,000 rows.
const MaxBulkStatus = 500

// Priority, Category, and Status mirror the enums. Values are checked before a
// write so a bad one is a validation error naming the field, not a constraint
// violation surfacing as a 500.
type (
	Priority string
	Category string
	Status   string
)

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

const (
	CategoryFunctional  Category = "functional"
	CategoryNegative    Category = "negative"
	CategoryBoundary    Category = "boundary"
	CategorySecurity    Category = "security"
	CategoryAuth        Category = "auth"
	CategoryPerformance Category = "performance"
	CategoryData        Category = "data"
)

const (
	StatusDraft    Status = "draft"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

func (p Priority) Valid() bool {
	switch p {
	case PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow:
		return true
	default:
		return false
	}
}

func (c Category) Valid() bool {
	switch c {
	case CategoryFunctional, CategoryNegative, CategoryBoundary, CategorySecurity,
		CategoryAuth, CategoryPerformance, CategoryData:
		return true
	default:
		return false
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusApproved, StatusRejected:
		return true
	default:
		return false
	}
}

// Step is one step of a test case. A named type, because the column is jsonb and
// no domain type here holds a map (backend-standards.md 9).
type Step struct {
	Action   string `json:"action"`
	Data     string `json:"data,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// TestCase is one generated or hand-written case.
type TestCase struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	RequirementID *uuid.UUID

	Title         string
	Preconditions string
	Steps         []Step
	Expected      string

	Priority Priority
	Category Category
	Status   Status

	// Endpoint is "METHOD /path" as the case asserts against, which is not always
	// the requirement's endpoint: one auth rule produces cases across several
	// operations, and code generation groups by what a case actually calls.
	Endpoint string

	// SupersededBy names the case that replaced this one. A merge keeps its
	// history rather than deleting the evidence (F-5.4).
	SupersededBy *uuid.UUID

	GeneratedBy string
	CreatedBy   *uuid.UUID

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Designed is one case as the agent returned it, before it has an ID.
type Designed struct {
	Title         string `json:"title"`
	Preconditions string `json:"preconditions"`
	Steps         []Step `json:"steps"`
	Expected      string `json:"expected"`

	Priority Priority `json:"priority"`
	Category Category `json:"category"`

	// Endpoint and Assertion are what the fingerprint is built from. The agent is
	// told to keep the assertion phrasing stable for the same check, which is what
	// makes two wordings of one case collide (F-5.3).
	Endpoint  string `json:"endpoint"`
	Assertion string `json:"assertion"`
}

// DesignResult is the design agent's whole answer for one requirement.
type DesignResult struct {
	TestCases []Designed `json:"test_cases"`
}

// Service owns the test_cases table.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Fingerprint identifies a case for deduplication.
//
// Deterministic, free, and instant, which is why it runs before the AI dedupe
// pass rather than instead of it: exact duplicates never cost a provider call
// (F-5.3).
func Fingerprint(endpoint, assertion string) []byte {
	method, path := splitEndpoint(endpoint)
	return ingest.Fingerprint(method, path, assertion)
}

// SaveInput is one requirement's designed cases.
type SaveInput struct {
	ProjectID     uuid.UUID
	RequirementID *uuid.UUID
	GeneratedBy   string

	Items []Designed
}

// SaveResult reports what a bulk write did.
type SaveResult struct {
	Written    int
	Duplicates int

	// Fingerprints are what was actually written, so a caller can read the rows
	// back for the near-miss pass without guessing which of its candidates landed.
	Fingerprints [][]byte
}

// Save persists designed cases, skipping what is already stored.
//
// Two passes, cheapest first. The fingerprints already present are read in one
// query and filtered in memory, so a re-run writes nothing and costs one SELECT;
// what survives goes in with CopyFrom, so 400 cases is one statement (BE-2.9).
func (s *Service) Save(ctx context.Context, input SaveInput) (SaveResult, error) {
	rows, fingerprints, duplicates := s.prepare(input)
	if len(rows) == 0 {
		return SaveResult{Duplicates: duplicates}, nil
	}

	existing, err := s.db.Queries().ListTestCasesByFingerprint(ctx,
		dbgen.ListTestCasesByFingerprintParams{
			ProjectID: input.ProjectID, Fingerprints: fingerprints,
		})
	if err != nil {
		return SaveResult{}, fmt.Errorf("read existing fingerprints: %w", err)
	}

	stored := make(map[string]struct{}, len(existing))
	for _, row := range existing {
		stored[string(row.Fingerprint)] = struct{}{}
	}

	fresh := rows[:0]
	for _, row := range rows {
		if _, known := stored[string(row.Fingerprint)]; known {
			duplicates++
			continue
		}
		stored[string(row.Fingerprint)] = struct{}{}
		fresh = append(fresh, row)
	}
	if len(fresh) == 0 {
		return SaveResult{Duplicates: duplicates}, nil
	}

	written, err := s.db.Queries().CreateTestCasesBulk(ctx, fresh)
	if err != nil {
		return SaveResult{}, fmt.Errorf("bulk insert test cases: %w", err)
	}

	result := SaveResult{Written: int(written), Duplicates: duplicates}
	for _, row := range fresh {
		result.Fingerprints = append(result.Fingerprints, row.Fingerprint)
	}
	return result, nil
}

// prepare validates and normalizes one batch, dropping in-batch duplicates.
//
// A model asked for twelve cases sometimes returns the same check twice; catching
// that here costs nothing and keeps the CopyFrom from failing on its own batch.
func (s *Service) prepare(input SaveInput) ([]dbgen.CreateTestCasesBulkParams, [][]byte, int) {
	rows := make([]dbgen.CreateTestCasesBulkParams, 0, len(input.Items))
	fingerprints := make([][]byte, 0, len(input.Items))
	seen := make(map[string]struct{}, len(input.Items))

	var duplicates int

	for _, item := range input.Items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}

		priority := item.Priority
		if !priority.Valid() {
			priority = PriorityMedium
		}
		category := item.Category
		if !category.Valid() {
			category = CategoryFunctional
		}

		assertion := item.Assertion
		if assertion == "" {
			// Without an assertion the fingerprint would collapse every case for an
			// endpoint into one. The title is the next most stable thing the model
			// gave us.
			assertion = title
		}

		fingerprint := Fingerprint(item.Endpoint, assertion)
		if _, repeated := seen[string(fingerprint)]; repeated {
			duplicates++
			continue
		}
		seen[string(fingerprint)] = struct{}{}

		steps, err := json.Marshal(item.Steps)
		if err != nil {
			// A step list that will not encode is a broken case, and dropping it is
			// better than failing the other eleven.
			continue
		}

		rows = append(rows, dbgen.CreateTestCasesBulkParams{
			ProjectID:     input.ProjectID,
			Endpoint:      strings.TrimSpace(item.Endpoint),
			RequirementID: input.RequirementID,
			Title:         title,
			Preconditions: strings.TrimSpace(item.Preconditions),
			Steps:         steps,
			Expected:      strings.TrimSpace(item.Expected),
			Priority:      dbgen.TestPriority(priority),
			Category:      dbgen.TestCategory(category),
			Status:        dbgen.TestCaseStatus(StatusDraft),
			Fingerprint:   fingerprint,
			GeneratedBy:   input.GeneratedBy,
		})
		fingerprints = append(fingerprints, fingerprint)
	}

	return rows, fingerprints, duplicates
}

// Get loads one case.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (TestCase, error) {
	row, err := s.db.Queries().GetTestCase(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return TestCase{}, apierr.NotFound("Test case")
		}
		return TestCase{}, fmt.Errorf("load test case %s: %w", id, err)
	}
	return toTestCase(row), nil
}

// Count reports how many live cases a project has.
func (s *Service) Count(ctx context.Context, projectID uuid.UUID) (int64, error) {
	count, err := s.db.Queries().CountTestCases(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("count test cases: %w", err)
	}
	return count, nil
}

// ForRequirement returns the live cases already stored for a requirement, which
// is the narrow candidate set the AI dedupe pass compares against (F-5.4).
func (s *Service) ForRequirement(ctx context.Context, requirementID uuid.UUID) ([]TestCase, error) {
	rows, err := s.db.Queries().ListTestCasesForRequirement(ctx, &requirementID)
	if err != nil {
		return nil, fmt.Errorf("list test cases for requirement: %w", err)
	}

	cases := make([]TestCase, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, toTestCase(row))
	}
	return cases, nil
}

// ByFingerprint reads back the rows a bulk write created.
//
// CopyFrom returns a count rather than the rows, so this is how a caller gets the
// identifiers it needs for the merge pass. One query for the batch, keyed on what
// it just wrote.
func (s *Service) ByFingerprint(
	ctx context.Context,
	projectID uuid.UUID,
	fingerprints [][]byte,
) ([]TestCase, error) {
	if len(fingerprints) == 0 {
		return nil, nil
	}

	rows, err := s.db.Queries().ListTestCasesByFingerprint(ctx,
		dbgen.ListTestCasesByFingerprintParams{
			ProjectID: projectID, Fingerprints: fingerprints,
		})
	if err != nil {
		return nil, fmt.Errorf("read written test cases: %w", err)
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	out := make([]TestCase, 0, len(ids))
	for _, id := range ids {
		testCase, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, testCase)
	}
	return out, nil
}

// Supersede records a merge.
func (s *Service) Supersede(ctx context.Context, id, replacedBy uuid.UUID) error {
	if _, err := s.db.Queries().SupersedeTestCase(ctx, dbgen.SupersedeTestCaseParams{
		ID: id, SupersededBy: &replacedBy,
	}); err != nil {
		return fmt.Errorf("supersede test case %s: %w", id, err)
	}
	return nil
}

// DecodeDesign reads the design agent's answer.
func DecodeDesign(raw json.RawMessage) (DesignResult, error) {
	var result DesignResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return DesignResult{}, fmt.Errorf("decode designed test cases: %w", err)
	}
	return result, nil
}

func toTestCase(row dbgen.TestCase) TestCase {
	testCase := TestCase{
		ID:            row.ID,
		ProjectID:     row.ProjectID,
		RequirementID: row.RequirementID,
		Title:         row.Title,
		Preconditions: row.Preconditions,
		Expected:      row.Expected,
		Endpoint:      row.Endpoint,
		Priority:      Priority(row.Priority),
		Category:      Category(row.Category),
		Status:        Status(row.Status),
		SupersededBy:  row.SupersededBy,
		GeneratedBy:   row.GeneratedBy,
		CreatedBy:     row.CreatedBy,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}

	if len(row.Steps) > 0 {
		if err := json.Unmarshal(row.Steps, &testCase.Steps); err != nil {
			// The case stays readable with no steps rather than failing the whole
			// page it appears on, and the log says which row to look at.
			slog.Warn("decode test case steps", "test_case_id", row.ID, "error", err)
		}
	}
	return testCase
}

// splitEndpoint reads "METHOD /path" as the agent writes it.
func splitEndpoint(endpoint string) (method, path string) {
	fields := strings.Fields(strings.TrimSpace(endpoint))
	switch len(fields) {
	case 0:
		return "", "/"
	case 1:
		return "", fields[0]
	default:
		return fields[0], fields[1]
	}
}

// EditInput is an inline edit of one case.
//
// A nil field is left alone, which is what makes this a PATCH: a UI that sends
// only the field somebody touched must not blank the rest.
type EditInput struct {
	Title         *string
	Preconditions *string
	Steps         *[]Step
	Expected      *string
	Priority      *Priority
	Category      *Category
	Status        *Status
}

// Update applies an edit.
//
// A superseded case is not editable: it is history, and letting somebody edit the
// losing half of a merge would make the audit trail lie.
func (s *Service) Update(ctx context.Context, id uuid.UUID, input EditInput) (TestCase, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return TestCase{}, err
	}
	if existing.SupersededBy != nil {
		return TestCase{}, apierr.Conflict(
			"This case was merged into another one and is read-only.")
	}

	updated := existing
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			return TestCase{}, apierr.Validation("Give the test case a title.",
				map[string]any{"field": "title"})
		}
		updated.Title = title
	}
	if input.Preconditions != nil {
		updated.Preconditions = strings.TrimSpace(*input.Preconditions)
	}
	if input.Steps != nil {
		updated.Steps = *input.Steps
	}
	if input.Expected != nil {
		updated.Expected = strings.TrimSpace(*input.Expected)
	}
	if input.Priority != nil {
		if !input.Priority.Valid() {
			return TestCase{}, apierr.Validation("Choose a valid priority.",
				map[string]any{"field": "priority"})
		}
		updated.Priority = *input.Priority
	}
	if input.Category != nil {
		if !input.Category.Valid() {
			return TestCase{}, apierr.Validation("Choose a valid category.",
				map[string]any{"field": "category"})
		}
		updated.Category = *input.Category
	}
	if input.Status != nil {
		if err := transition(existing.Status, *input.Status); err != nil {
			return TestCase{}, err
		}
		updated.Status = *input.Status
	}

	steps, err := json.Marshal(updated.Steps)
	if err != nil {
		return TestCase{}, fmt.Errorf("encode steps: %w", err)
	}

	row, err := s.db.Queries().UpdateTestCase(ctx, dbgen.UpdateTestCaseParams{
		ID:            id,
		Title:         updated.Title,
		Preconditions: updated.Preconditions,
		Steps:         steps,
		Expected:      updated.Expected,
		Priority:      dbgen.TestPriority(updated.Priority),
		Category:      dbgen.TestCategory(updated.Category),
		Status:        dbgen.TestCaseStatus(updated.Status),
	})
	if err != nil {
		return TestCase{}, fmt.Errorf("update test case %s: %w", id, err)
	}
	return toTestCase(row), nil
}

// CreateInput is a hand-written case.
type CreateInput struct {
	ProjectID     uuid.UUID
	RequirementID *uuid.UUID
	CreatedBy     *uuid.UUID

	Title         string
	Preconditions string
	Steps         []Step
	Expected      string

	Priority Priority
	Category Category

	// Endpoint and Assertion feed the fingerprint. A hand-written case that
	// duplicates a generated one should collide with it, so the same identity
	// rules apply to both.
	Endpoint  string
	Assertion string
}

// Create adds a case by hand.
func (s *Service) Create(ctx context.Context, input CreateInput) (TestCase, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return TestCase{}, apierr.Validation("Give the test case a title.",
			map[string]any{"field": "title"})
	}

	priority := input.Priority
	if priority == "" {
		priority = PriorityMedium
	}
	if !priority.Valid() {
		return TestCase{}, apierr.Validation("Choose a valid priority.",
			map[string]any{"field": "priority"})
	}

	category := input.Category
	if category == "" {
		category = CategoryFunctional
	}
	if !category.Valid() {
		return TestCase{}, apierr.Validation("Choose a valid category.",
			map[string]any{"field": "category"})
	}

	assertion := input.Assertion
	if assertion == "" {
		assertion = title
	}

	steps, err := json.Marshal(input.Steps)
	if err != nil {
		return TestCase{}, fmt.Errorf("encode steps: %w", err)
	}

	row, err := s.db.Queries().CreateTestCase(ctx, dbgen.CreateTestCaseParams{
		ProjectID:     input.ProjectID,
		Endpoint:      strings.TrimSpace(input.Endpoint),
		RequirementID: input.RequirementID,
		Title:         title,
		Preconditions: strings.TrimSpace(input.Preconditions),
		Steps:         steps,
		Expected:      strings.TrimSpace(input.Expected),
		Priority:      dbgen.TestPriority(priority),
		Category:      dbgen.TestCategory(category),
		// A hand-written case starts approved: somebody wrote it deliberately, and
		// asking them to approve their own typing is ceremony.
		Status:      dbgen.TestCaseStatus(StatusApproved),
		Fingerprint: Fingerprint(input.Endpoint, assertion),
		GeneratedBy: "",
		CreatedBy:   input.CreatedBy,
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			return TestCase{}, apierr.Conflict(
				"A test case checking the same thing already exists in this project.")
		}
		return TestCase{}, fmt.Errorf("create test case: %w", err)
	}
	return toTestCase(row), nil
}

// SetStatus applies one status to a batch, in one transaction.
//
// Capped, because a bulk approve over 50,000 rows is a transaction held open
// while somebody's browser decides whether to finish the request.
func (s *Service) SetStatus(
	ctx context.Context,
	projectID uuid.UUID,
	ids []uuid.UUID,
	status Status,
) (int64, error) {
	if !status.Valid() {
		return 0, apierr.Validation("Choose a valid status.", map[string]any{"field": "status"})
	}
	if len(ids) == 0 {
		return 0, apierr.Validation("Select at least one test case.",
			map[string]any{"field": "ids"})
	}
	if len(ids) > MaxBulkStatus {
		return 0, apierr.Validation(
			fmt.Sprintf("Change at most %d test cases at a time.", MaxBulkStatus),
			map[string]any{"field": "ids", "max": MaxBulkStatus})
	}

	updated, err := s.db.Queries().SetTestCaseStatus(ctx, dbgen.SetTestCaseStatusParams{
		ProjectID: projectID, Ids: ids, Status: dbgen.TestCaseStatus(status),
	})
	if err != nil {
		return 0, fmt.Errorf("set test case status: %w", err)
	}
	return updated, nil
}

// Delete removes a case entirely.
//
// A generated case is normally rejected rather than deleted, so the next run does
// not silently reproduce it; delete exists for a case somebody added by mistake.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.Queries().DeleteTestCase(ctx, id); err != nil {
		return fmt.Errorf("delete test case %s: %w", id, err)
	}
	return nil
}

// StatusCounts backs the summary above the table.
func (s *Service) StatusCounts(ctx context.Context, projectID uuid.UUID) (map[Status]int64, error) {
	rows, err := s.db.Queries().CountTestCasesByStatus(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count test cases by status: %w", err)
	}

	counts := make(map[Status]int64, len(rows))
	for _, row := range rows {
		counts[Status(row.Status)] = row.Total
	}
	return counts, nil
}

// transition rejects a status change that does not make sense.
//
// Rejected is terminal for a generated case: reviving it would put a case the
// team already refused back in the suite, and adding it again by hand is the
// deliberate act that should be required.
func transition(from, to Status) error {
	if from == to {
		return nil
	}
	if from == StatusRejected {
		return apierr.Conflict(
			"This case was rejected. Add a new case rather than reviving it.")
	}
	if !to.Valid() {
		return apierr.Validation("Choose a valid status.", map[string]any{"field": "status"})
	}
	return nil
}

// ApprovedCase is an approved case with the endpoint it belongs to.
//
// The endpoint comes from the requirement's endpoint row rather than from the case
// itself: a case does not own an endpoint, and duplicating it onto every row would
// be two places to keep in step.
type ApprovedCase struct {
	TestCase

	Method string
	Path   string
}

// Approved returns the cases code generation may implement.
//
// Approved only, and never a superseded one. Generating from a draft somebody is
// still editing produces a file that has to be thrown away, which costs a provider
// call and a review (BE-3.2).
func (s *Service) Approved(ctx context.Context, projectID uuid.UUID) ([]ApprovedCase, error) {
	rows, err := s.db.Queries().ListApprovedTestCases(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list approved test cases: %w", err)
	}

	out := make([]ApprovedCase, 0, len(rows))
	for _, row := range rows {
		testCase := toTestCase(dbgen.TestCase{
			ID:            row.ID,
			ProjectID:     row.ProjectID,
			RequirementID: row.RequirementID,
			Title:         row.Title,
			Preconditions: row.Preconditions,
			Steps:         row.Steps,
			Expected:      row.Expected,
			Priority:      row.Priority,
			Category:      row.Category,
			Status:        row.Status,
			Fingerprint:   row.Fingerprint,
			Endpoint:      row.Endpoint,
			SupersededBy:  row.SupersededBy,
			GeneratedBy:   row.GeneratedBy,
			CreatedBy:     row.CreatedBy,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		})

		out = append(out, ApprovedCase{
			TestCase: testCase,
			Method:   row.Method,
			Path:     row.EndpointPath,
		})
	}
	return out, nil
}
