package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// maxSpecBytes bounds what is read into memory to parse.
//
// A specification is text, and one larger than this is not a specification
// somebody wrote: the upload cap already applies, and this is the second, tighter
// bound for the path that has to hold the whole document at once.
const maxSpecBytes = 32 << 20

// Artifacts is the slice of the artifacts service this package needs.
type Artifacts interface {
	StorageKeyFor(ctx context.Context, id uuid.UUID) (Artifact, error)
}

// Artifact is what ingest needs to know about an uploaded file. Declared here by
// the consumer, so this package does not depend on the artifacts domain type
// (backend-standards.md 3).
type Artifact struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	Kind       string
	Filename   string
	StorageKey string
}

// Service parses artifacts into the endpoint model and stores it.
type Service struct {
	db    *store.DB
	store objectstore.Store
}

func NewService(db *store.DB, objects objectstore.Store) *Service {
	return &Service{db: db, store: objects}
}

// Ingest parses one artifact and persists what it found.
//
// Idempotent by construction: every endpoint upserts on
// (artifact_id, method, path), so re-running produces the same rows rather than a
// second copy of the API (BE-2.5).
func (s *Service) Ingest(ctx context.Context, artifact Artifact, progress func(done, total int)) (Document, error) {
	raw, err := s.read(ctx, artifact.StorageKey)
	if err != nil {
		return Document{}, err
	}

	document, err := Parse(artifact.Kind, artifact.Filename, raw)
	if err != nil {
		// A parse failure is a clean stop with the line surfaced, not a retry loop:
		// the file will not become valid on the second attempt (BE-2.5).
		var parseErr *ParseError
		if errors.As(err, &parseErr) {
			return Document{}, apierr.SpecificationInvalid(parseErr.Error(), parseErr.Line)
		}
		return Document{}, err
	}

	for index, endpoint := range document.Endpoints {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		if err := s.persist(ctx, artifact, endpoint); err != nil {
			return Document{}, err
		}
		if progress != nil {
			progress(index+1, len(document.Endpoints))
		}
	}

	return document, nil
}

func (s *Service) persist(ctx context.Context, artifact Artifact, endpoint Endpoint) error {
	parameters, err := json.Marshal(endpoint.Parameters)
	if err != nil {
		return fmt.Errorf("encode parameters for %s: %w", endpoint.Key(), err)
	}
	request, err := json.Marshal(endpoint.Request)
	if err != nil {
		return fmt.Errorf("encode request for %s: %w", endpoint.Key(), err)
	}
	responses, err := json.Marshal(endpoint.Responses)
	if err != nil {
		return fmt.Errorf("encode responses for %s: %w", endpoint.Key(), err)
	}
	security, err := json.Marshal(endpoint.Security)
	if err != nil {
		return fmt.Errorf("encode security for %s: %w", endpoint.Key(), err)
	}

	if _, err := s.db.Queries().UpsertEndpoint(ctx, dbgen.UpsertEndpointParams{
		ProjectID:   artifact.ProjectID,
		ArtifactID:  artifact.ID,
		Method:      endpoint.Method,
		Path:        endpoint.Path,
		OperationID: endpoint.OperationID,
		Summary:     endpoint.Summary,
		Description: endpoint.Description,
		Parameters:  parameters,
		Request:     request,
		Responses:   responses,
		Security:    security,
		SourceRef:   endpoint.SourceRef,
	}); err != nil {
		return fmt.Errorf("store endpoint %s: %w", endpoint.Key(), err)
	}
	return nil
}

// Endpoints returns the stored model for a project, which is what the generation
// agents read as their cached prefix.
func (s *Service) Endpoints(ctx context.Context, projectID uuid.UUID, artifactID *uuid.UUID) ([]Endpoint, error) {
	rows, err := s.db.Queries().ListEndpoints(ctx, dbgen.ListEndpointsParams{
		ProjectID: projectID, ArtifactID: artifactID,
	})
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}

	endpoints := make([]Endpoint, 0, len(rows))
	for _, row := range rows {
		endpoint := Endpoint{
			Method:      row.Method,
			Path:        row.Path,
			OperationID: row.OperationID,
			Summary:     row.Summary,
			Description: row.Description,
			SourceRef:   row.SourceRef,
		}

		// A decode failure here is worth reporting rather than skipping: the
		// endpoint would silently lose its parameters, and the test cases designed
		// from it would look thin for no visible reason.
		if err := decodeInto(row.Parameters, &endpoint.Parameters); err != nil {
			return nil, fmt.Errorf("decode parameters for %s: %w", endpoint.Key(), err)
		}
		if err := decodeInto(row.Request, &endpoint.Request); err != nil {
			return nil, fmt.Errorf("decode request for %s: %w", endpoint.Key(), err)
		}
		if err := decodeInto(row.Responses, &endpoint.Responses); err != nil {
			return nil, fmt.Errorf("decode responses for %s: %w", endpoint.Key(), err)
		}
		if err := decodeInto(row.Security, &endpoint.Security); err != nil {
			return nil, fmt.Errorf("decode security for %s: %w", endpoint.Key(), err)
		}

		endpoints = append(endpoints, endpoint)
	}
	return endpoints, nil
}

// EndpointIDs maps "METHOD /path" to the stored row, so an extracted requirement
// can be linked to the operation it is about.
func (s *Service) EndpointIDs(
	ctx context.Context,
	projectID uuid.UUID,
	artifactID *uuid.UUID,
) (map[string]uuid.UUID, error) {
	rows, err := s.db.Queries().ListEndpoints(ctx, dbgen.ListEndpointsParams{
		ProjectID: projectID, ArtifactID: artifactID,
	})
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}

	ids := make(map[string]uuid.UUID, len(rows))
	for _, row := range rows {
		ids[strings.ToUpper(row.Method)+" "+row.Path] = row.ID
	}
	return ids, nil
}

// Count reports how many endpoints a project has, for the coverage number and the
// cost estimate.
func (s *Service) Count(ctx context.Context, projectID uuid.UUID) (int64, error) {
	count, err := s.db.Queries().CountEndpoints(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("count endpoints: %w", err)
	}
	return count, nil
}

func (s *Service) read(ctx context.Context, key string) ([]byte, error) {
	body, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, apierr.StorageUnreachable(err)
	}
	defer func() {
		if err := body.Close(); err != nil {
			_ = err // the read either succeeded or is already being reported
		}
	}()

	raw, err := io.ReadAll(io.LimitReader(body, maxSpecBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read specification: %w", err)
	}
	if len(raw) > maxSpecBytes {
		return nil, apierr.UploadTooLarge(maxSpecBytes)
	}
	return raw, nil
}

func decodeInto(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}
