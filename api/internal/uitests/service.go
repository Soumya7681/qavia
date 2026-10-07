package uitests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service owns discovered flow graphs (BE-7.2.3).
//
// A graph is stored and reviewable before anything is generated from it, which is the
// reason it is a row rather than a value passed straight to the generator: a suite
// built from a wrong graph is a suite somebody debugs test by test, and the graph is
// where the mistake is cheap to see.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Stored is a graph as it was written.
type Stored struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Target   string
	AuthMode string

	// Document is the graph itself, kept raw so the API's mapper decides its shape and
	// this service does not learn it twice.
	Document json.RawMessage

	Pages int
	Flows int

	Steps    int
	CutShort bool

	// Artifacts are the video, trace, and screenshots this discovery recorded, as
	// object-store keys. A named type rather than a map, because jsonb stops at the
	// store boundary (backend-standards.md 9).
	Artifacts []Artifact

	ReviewedAt *time.Time
	ReviewedBy *uuid.UUID

	ModelName string
	JobID     *uuid.UUID
	CreatedAt time.Time
}

// Summary is one row of the history, without the document.
type Summary struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Target   string
	AuthMode string

	Pages int
	Flows int

	Steps    int
	CutShort bool

	ReviewedAt *time.Time

	ModelName string
	CreatedAt time.Time
}

// Artifact is one recording a discovery left behind.
type Artifact struct {
	Name string `json:"name"`

	// Key is where it lives in object storage. The bytes are never in the row: a
	// trace is megabytes, and a database is the wrong place to keep one (BE-7.5).
	Key string `json:"key"`

	Bytes       int64  `json:"bytes"`
	ContentType string `json:"contentType,omitempty"`
}

// StoreInput is a finished discovery.
type StoreInput struct {
	ProjectID uuid.UUID
	Graph     Graph
	Artifacts []Artifact
	ModelName string
	JobID     *uuid.UUID
}

// Store writes one discovery's graph.
func (s *Service) Store(ctx context.Context, input StoreInput) (Stored, error) {
	document, err := json.Marshal(input.Graph)
	if err != nil {
		return Stored{}, apierr.Internal(fmt.Errorf("encode the flow graph: %w", err))
	}

	recordings := input.Artifacts
	if recordings == nil {
		// A NOT NULL jsonb column takes `[]`, not `null`: a nil slice marshals to null
		// and the insert would be rejected for a discovery that simply recorded nothing.
		recordings = []Artifact{}
	}
	artifacts, err := json.Marshal(recordings)
	if err != nil {
		return Stored{}, apierr.Internal(fmt.Errorf("encode the discovery artifacts: %w", err))
	}

	row, err := s.db.Queries().CreateUIFlow(ctx, dbgen.CreateUIFlowParams{
		ProjectID: input.ProjectID,
		Target:    input.Graph.Target,
		AuthMode:  authModeOr(input.Graph.AuthMode),
		Document:  document,

		// Counted here so a list screen never parses a document to render a row.
		PageCount: int32(len(input.Graph.Pages)), //nolint:gosec // Bounded by the discovery budget.
		FlowCount: int32(len(input.Graph.Flows)), //nolint:gosec // Bounded by the discovery budget.

		Steps:     int32(input.Graph.Steps), //nolint:gosec // Bounded by the discovery budget.
		CutShort:  input.Graph.CutShort,
		Artifacts: artifacts,
		ModelName: input.ModelName,
		JobID:     input.JobID,
	})
	if err != nil {
		return Stored{}, apierr.Internal(fmt.Errorf("store the flow graph: %w", err))
	}
	return storedFrom(row), nil
}

// Latest is the newest graph for a project, and whether there is one.
//
// Absent is a normal state rather than an error: a project nobody has run a discovery
// against has no graph, which is the answer the UI needs in order to offer one.
func (s *Service) Latest(ctx context.Context, projectID uuid.UUID) (Stored, bool, error) {
	row, err := s.db.Queries().LatestUIFlow(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Stored{}, false, nil
		}
		return Stored{}, false, apierr.Internal(fmt.Errorf("read the flow graph: %w", err))
	}
	return storedFrom(row), true, nil
}

// Get is one graph by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Stored, error) {
	row, err := s.db.Queries().GetUIFlow(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Stored{}, apierr.FlowGraphNotFound()
		}
		return Stored{}, apierr.Internal(fmt.Errorf("read the flow graph: %w", err))
	}
	return storedFrom(row), nil
}

// List is a project's discovery history, newest first.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, pageSize int32) ([]Summary, error) {
	if pageSize <= 0 || pageSize > maxPageSize {
		pageSize = defaultPageSize
	}

	rows, err := s.db.Queries().ListUIFlows(ctx, dbgen.ListUIFlowsParams{
		ProjectID: projectID, PageSize: pageSize,
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list the flow graphs: %w", err))
	}

	summaries := make([]Summary, 0, len(rows))
	for _, row := range rows {
		summaries = append(summaries, Summary{
			ID:         row.ID,
			ProjectID:  row.ProjectID,
			Target:     row.Target,
			AuthMode:   row.AuthMode,
			Pages:      int(row.PageCount),
			Flows:      int(row.FlowCount),
			Steps:      int(row.Steps),
			CutShort:   row.CutShort,
			ReviewedAt: row.ReviewedAt,
			ModelName:  row.ModelName,
			CreatedAt:  row.CreatedAt,
		})
	}
	return summaries, nil
}

// MarkReviewed records that a person has read the graph.
//
// Generating from an unreviewed graph is still allowed: blocking it would mean an
// installation that wants the whole path automated cannot have it. What the flag buys
// is that "nobody looked at this" is visible rather than assumed (BE-7.2.3).
func (s *Service) MarkReviewed(ctx context.Context, id, userID uuid.UUID) (Stored, error) {
	row, err := s.db.Queries().MarkUIFlowReviewed(ctx, dbgen.MarkUIFlowReviewedParams{
		ID: id, ReviewedBy: &userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Stored{}, apierr.FlowGraphNotFound()
		}
		return Stored{}, apierr.Internal(fmt.Errorf("mark the flow graph reviewed: %w", err))
	}
	return storedFrom(row), nil
}

// Decode reads a stored document back into a graph.
//
// Used by generation, which needs the pages and flows rather than the bytes. A graph
// that cannot be decoded is an internal error rather than a validation failure: this
// platform wrote it.
func (s Stored) Decode() (Graph, error) {
	var graph Graph
	if err := json.Unmarshal(s.Document, &graph); err != nil {
		return Graph{}, apierr.Internal(fmt.Errorf("read the stored flow graph: %w", err))
	}
	return graph, nil
}

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func storedFrom(row dbgen.UiFlow) Stored {
	// A row whose artifacts do not decode is a row this platform wrote wrongly, and
	// the graph is still worth returning: the recordings are evidence about the
	// discovery, not the discovery.
	var artifacts []Artifact
	if len(row.Artifacts) > 0 {
		if err := json.Unmarshal(row.Artifacts, &artifacts); err != nil {
			artifacts = nil
		}
	}

	return Stored{
		ID:         row.ID,
		ProjectID:  row.ProjectID,
		Target:     row.Target,
		AuthMode:   row.AuthMode,
		Document:   row.Document,
		Pages:      int(row.PageCount),
		Flows:      int(row.FlowCount),
		Steps:      int(row.Steps),
		CutShort:   row.CutShort,
		Artifacts:  artifacts,
		ReviewedAt: row.ReviewedAt,
		ReviewedBy: row.ReviewedBy,
		ModelName:  row.ModelName,
		JobID:      row.JobID,
		CreatedAt:  row.CreatedAt,
	}
}

// authModeOr keeps the column honest: an empty mode is "none", and the difference
// matters when reading a graph with no authenticated pages.
func authModeOr(mode string) string {
	if mode == "" {
		return "none"
	}
	return mode
}
