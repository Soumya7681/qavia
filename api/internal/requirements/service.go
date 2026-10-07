// Package requirements owns what the extract agent understood from an input
// (F-4.1, F-4.2).
//
// A requirement is the unit test cases are designed against, and the unit
// coverage is reported over. Every one carries a source reference, because a
// requirement that cannot cite where it came from is the platform's opinion rather
// than the client's specification.
package requirements

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Kind is what sort of requirement this is.
type Kind string

const (
	KindFeature        Kind = "feature"
	KindBusinessRule   Kind = "business_rule"
	KindValidationRule Kind = "validation_rule"
	KindAuth           Kind = "auth"
	KindEdgeCase       Kind = "edge_case"
)

// Valid reports whether the kind is one the database will accept.
func (k Kind) Valid() bool {
	switch k {
	case KindFeature, KindBusinessRule, KindValidationRule, KindAuth, KindEdgeCase:
		return true
	default:
		return false
	}
}

// Requirement is one thing the input requires.
type Requirement struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	ArtifactID *uuid.UUID
	EndpointID *uuid.UUID

	Kind  Kind
	Title string
	Body  string

	// SourceRef points back into the uploaded file: "line:column" for a
	// specification, a character range for pasted text.
	SourceRef string

	// GeneratedBy names the provider and model that produced this, so output
	// quality traces back to what produced it (ai-architecture.md 3.5).
	GeneratedBy string

	// Endpoint is "METHOD /path" where the requirement belongs to one operation.
	// It is not a column: the design agent needs it in the prompt, and it is
	// recovered from the endpoint row rather than stored twice.
	Endpoint string

	CreatedAt time.Time
}

// Page is one page of requirements plus the cursor for the next.
type Page struct {
	Items      []Requirement
	NextCursor string
}

// Extracted is one requirement as the agent returned it, before it has an ID.
type Extracted struct {
	Kind      Kind   `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Endpoint  string `json:"endpoint"`
	SourceRef string `json:"source_ref"`
}

// ExtractionResult is the agent's whole answer.
type ExtractionResult struct {
	Requirements []Extracted `json:"requirements"`
}

// Service owns the requirements table.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Fingerprint identifies a requirement for deduplication.
//
// Kind, normalized title, and endpoint. The body is deliberately excluded: a model
// rewording the same rule on a second run must collide rather than produce a
// second requirement, and the body is exactly the part that rewords.
func Fingerprint(kind Kind, title, endpoint string) []byte {
	normalized := strings.Join([]string{
		string(kind),
		strings.ToLower(strings.Join(strings.Fields(title), " ")),
		strings.ToUpper(strings.TrimSpace(endpoint)),
	}, "|")

	sum := sha256.Sum256([]byte(normalized))
	return sum[:]
}

// SaveInput is one extraction's worth of requirements.
type SaveInput struct {
	ProjectID  uuid.UUID
	ArtifactID *uuid.UUID

	// EndpointIDs maps "METHOD /path" to the stored endpoint, so a requirement
	// links to the operation it is about.
	EndpointIDs map[string]uuid.UUID

	GeneratedBy string

	Items []Extracted
}

// Save persists an extraction.
//
// One transaction, and every row conflicts on its fingerprint, so re-running
// extract on an unchanged artifact writes nothing new. That is the idempotency
// requirement met by the database rather than by a check-then-write
// (backend-standards.md 8).
func (s *Service) Save(ctx context.Context, input SaveInput) ([]Requirement, error) {
	saved := make([]Requirement, 0, len(input.Items))

	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		for _, item := range input.Items {
			kind := item.Kind
			if !kind.Valid() {
				// An unknown kind is the model's mistake, not the user's. Filing it
				// as a feature keeps the requirement rather than losing it, and the
				// schema already constrains what may be stored.
				kind = KindFeature
			}

			title := strings.TrimSpace(item.Title)
			if title == "" {
				continue
			}

			params := dbgen.CreateRequirementParams{
				ProjectID:   input.ProjectID,
				ArtifactID:  input.ArtifactID,
				Kind:        dbgen.RequirementKind(kind),
				Title:       title,
				Body:        strings.TrimSpace(item.Body),
				SourceRef:   item.SourceRef,
				Fingerprint: Fingerprint(kind, title, item.Endpoint),
				GeneratedBy: input.GeneratedBy,
			}
			if endpointID, known := input.EndpointIDs[normalizeEndpoint(item.Endpoint)]; known {
				params.EndpointID = &endpointID
			}

			row, err := q.CreateRequirement(ctx, params)
			if err != nil {
				return fmt.Errorf("store requirement %q: %w", title, err)
			}

			requirement := toRequirement(row)
			requirement.Endpoint = item.Endpoint
			saved = append(saved, requirement)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// List returns a page of a project's requirements, newest first.
func (s *Service) List(
	ctx context.Context,
	projectID uuid.UUID,
	artifactID *uuid.UUID,
	limit int,
	cursor string,
) (Page, error) {
	limit = paging.ClampLimit(limit)

	params := dbgen.ListRequirementsParams{
		ProjectID:  projectID,
		ArtifactID: artifactID,
		PageSize:   int32(limit + 1),
	}
	if cursor != "" {
		at, err := paging.DecodeTime(cursor)
		if err != nil {
			return Page{}, err
		}
		params.Cursor = &at
	}

	rows, err := s.db.Queries().ListRequirements(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list requirements: %w", err)
	}

	page := Page{Items: make([]Requirement, 0, limit)}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = paging.EncodeTime(rows[i-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toRequirement(row))
	}
	return page, nil
}

// All returns every requirement in a project, for the generation fan-out.
//
// Unpaginated on purpose, and bounded by the fact that requirements come from one
// specification: the design stage needs all of them, and paging through them to
// hand each to a provider call would be ceremony.
func (s *Service) All(ctx context.Context, projectID uuid.UUID) ([]Requirement, error) {
	var (
		out    []Requirement
		cursor string
	)

	for {
		page, err := s.List(ctx, projectID, nil, paging.MaxLimit, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)

		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// Get loads one requirement.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Requirement, error) {
	row, err := s.db.Queries().GetRequirement(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return Requirement{}, apierr.NotFound("Requirement")
		}
		return Requirement{}, fmt.Errorf("load requirement %s: %w", id, err)
	}
	return toRequirement(row), nil
}

// Count reports how many requirements a project has.
func (s *Service) Count(ctx context.Context, projectID uuid.UUID) (int64, error) {
	count, err := s.db.Queries().CountRequirements(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("count requirements: %w", err)
	}
	return count, nil
}

// Coverage reports which requirements have test cases and which have none
// (F-12.2).
//
// The uncovered ones are listed rather than counted: "18 of 40 covered" tells a
// user there is a problem, and the list tells them where it is (BE-2.11).
type Coverage struct {
	Total     int64
	Covered   int64
	Uncovered []Requirement
}

func (s *Service) Coverage(ctx context.Context, projectID uuid.UUID) (Coverage, error) {
	rows, err := s.db.Queries().RequirementCoverage(ctx, projectID)
	if err != nil {
		return Coverage{}, fmt.Errorf("read requirement coverage: %w", err)
	}

	coverage := Coverage{Total: int64(len(rows))}
	for _, row := range rows {
		if row.TestCaseCount > 0 {
			coverage.Covered++
			continue
		}
		coverage.Uncovered = append(coverage.Uncovered, Requirement{
			ID:        row.ID,
			Kind:      Kind(row.Kind),
			Title:     row.Title,
			SourceRef: row.SourceRef,
		})
	}
	return coverage, nil
}

// ForPrompt renders a requirement as the agent's input.
//
// A named shape rather than the domain type: the field names in the prompt are
// part of the contract with the agent, and a renamed Go field must not silently
// change what the model is asked.
func ForPrompt(requirement Requirement) map[string]any {
	return map[string]any{
		"kind":     string(requirement.Kind),
		"title":    requirement.Title,
		"body":     requirement.Body,
		"endpoint": requirement.Endpoint,
	}
}

// DecodeExtraction reads the extract agent's answer.
func DecodeExtraction(raw json.RawMessage) (ExtractionResult, error) {
	var result ExtractionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ExtractionResult{}, fmt.Errorf("decode extraction: %w", err)
	}
	return result, nil
}

func toRequirement(row dbgen.Requirement) Requirement {
	return Requirement{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		ArtifactID:  row.ArtifactID,
		EndpointID:  row.EndpointID,
		Kind:        Kind(row.Kind),
		Title:       row.Title,
		Body:        row.Body,
		SourceRef:   row.SourceRef,
		GeneratedBy: row.GeneratedBy,
		CreatedAt:   row.CreatedAt,
	}
}

// normalizeEndpoint makes "get /tickets" and "GET  /tickets" the same key, since
// the model writes it by hand.
func normalizeEndpoint(endpoint string) string {
	fields := strings.Fields(strings.TrimSpace(endpoint))
	if len(fields) < 2 {
		return strings.ToUpper(strings.TrimSpace(endpoint))
	}
	return strings.ToUpper(fields[0]) + " " + fields[1]
}
