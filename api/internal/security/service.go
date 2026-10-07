package security

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service stores and reads security findings (BE-9.4).
//
// A finding is a failed run_result with security detail attached, so this service owns
// the detail and the run service owns the result. That split is what lets a finding be
// promoted to a defect through the path a failed test already uses (BE-9.4.2).
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// Finding is one probe that succeeded.
type Finding struct {
	ID          uuid.UUID
	RunResultID uuid.UUID
	RunID       uuid.UUID

	PayloadID string
	Category  string

	Endpoint  string
	Parameter string

	Severity     string
	Evidence     string
	Reproduction string
}

// StoreInput is one finding to record.
type StoreInput struct {
	RunResultID uuid.UUID
	RunID       uuid.UUID

	PayloadID string
	Category  string
	Endpoint  string
	Parameter string
	Severity  string

	Evidence     string
	Reproduction string
}

// Store records a finding against its probe's result.
func (s *Service) Store(ctx context.Context, input StoreInput) (Finding, error) {
	row, err := s.db.Queries().CreateSecurityFinding(ctx, dbgen.CreateSecurityFindingParams{
		RunResultID:  input.RunResultID,
		RunID:        input.RunID,
		PayloadID:    input.PayloadID,
		Category:     input.Category,
		Endpoint:     input.Endpoint,
		Parameter:    input.Parameter,
		Severity:     input.Severity,
		Evidence:     input.Evidence,
		Reproduction: input.Reproduction,
	})
	if err != nil {
		return Finding{}, apierr.Internal(fmt.Errorf("store a security finding: %w", err))
	}
	return toFinding(row), nil
}

// ForRun lists a run's findings, most severe first.
func (s *Service) ForRun(ctx context.Context, runID uuid.UUID) ([]Finding, error) {
	rows, err := s.db.Queries().ListSecurityFindings(ctx, runID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list security findings: %w", err))
	}

	findings := make([]Finding, 0, len(rows))
	for _, row := range rows {
		findings = append(findings, toFinding(row))
	}
	return findings, nil
}

// Get is one finding by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Finding, error) {
	row, err := s.db.Queries().GetSecurityFinding(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Finding{}, apierr.SecurityScanNotFound()
		}
		return Finding{}, apierr.Internal(fmt.Errorf("read a security finding: %w", err))
	}
	return toFinding(row), nil
}

func toFinding(row dbgen.SecurityFinding) Finding {
	return Finding{
		ID:           row.ID,
		RunResultID:  row.RunResultID,
		RunID:        row.RunID,
		PayloadID:    row.PayloadID,
		Category:     row.Category,
		Endpoint:     row.Endpoint,
		Parameter:    row.Parameter,
		Severity:     row.Severity,
		Evidence:     row.Evidence,
		Reproduction: row.Reproduction,
	}
}
