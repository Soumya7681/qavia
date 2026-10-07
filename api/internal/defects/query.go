package defects

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Filter narrows a project's defects.
//
// Five optional filters is why this query is built rather than generated: as
// thirty-two sqlc queries it would be unreadable, and one generated query with
// COALESCE tricks would be unindexable (backend-standards.md 9). Every value is a
// placeholder; nothing is concatenated.
type Filter struct {
	Status     Status
	Severity   Severity
	AssigneeID *uuid.UUID
	TestCaseID *uuid.UUID

	// Unassigned filters for defects nobody owns, which is a different question from
	// "assigned to nobody in particular" and cannot be expressed by AssigneeID.
	Unassigned bool

	// IncludeDuplicates is off by default: a list showing every duplicate of the same
	// bug is a list that hides the bugs.
	IncludeDuplicates bool

	Limit  int
	Cursor string
}

// Page is one page of defects plus the cursor for the next.
type Page struct {
	Items      []Defect
	NextCursor string
}

// defectColumns is the explicit column list. SELECT * is a review failure.
var defectColumns = []string{
	"id", "project_id", "run_result_id", "test_case_id", "requirement_id", "analysis_id",
	"title", "description", "severity", "status", "assignee_id", "duplicate_of",
	"root_cause_key", "external_ref", "created_by", "created_at", "updated_at",
	"resolved_at", "test_name",
}

// List returns a filtered page, newest first.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, filter Filter) (Page, error) {
	limit := paging.ClampLimit(filter.Limit)

	query := squirrel.
		Select(defectColumns...).
		From("defects").
		Where(squirrel.Eq{"project_id": projectID}).
		OrderBy("created_at DESC", "id DESC").
		Limit(uint64(limit + 1)). //nolint:gosec // Clamped above.
		PlaceholderFormat(squirrel.Dollar)

	if filter.Status != "" {
		if !filter.Status.Valid() {
			return Page{}, apierr.Validation(
				fmt.Sprintf("Status %q is not one this platform uses.", filter.Status),
				map[string]any{"field": "status"})
		}
		query = query.Where(squirrel.Eq{"status": string(filter.Status)})
	}
	if filter.Severity != "" {
		if !filter.Severity.Valid() {
			return Page{}, apierr.Validation(
				fmt.Sprintf("Severity %q is not one this platform uses.", filter.Severity),
				map[string]any{"field": "severity"})
		}
		query = query.Where(squirrel.Eq{"severity": string(filter.Severity)})
	}
	if filter.AssigneeID != nil {
		query = query.Where(squirrel.Eq{"assignee_id": *filter.AssigneeID})
	}
	if filter.Unassigned {
		query = query.Where("assignee_id IS NULL")
	}
	if filter.TestCaseID != nil {
		query = query.Where(squirrel.Eq{"test_case_id": *filter.TestCaseID})
	}
	if !filter.IncludeDuplicates {
		query = query.Where("duplicate_of IS NULL")
	}

	if filter.Cursor != "" {
		at, err := paging.DecodeTime(filter.Cursor)
		if err != nil {
			return Page{}, err
		}
		query = query.Where(squirrel.Lt{"created_at": at})
	}

	sql, args, err := query.ToSql()
	if err != nil {
		return Page{}, apierr.Internal(fmt.Errorf("build the defect query: %w", err))
	}

	rows, err := s.db.Pool().Query(ctx, sql, args...)
	if err != nil {
		return Page{}, apierr.Internal(fmt.Errorf("list defects: %w", err))
	}
	defer rows.Close()

	page := Page{Items: make([]Defect, 0, limit)}
	for rows.Next() {
		var row dbgen.Defect
		if err := rows.Scan(
			&row.ID, &row.ProjectID, &row.RunResultID, &row.TestCaseID, &row.RequirementID,
			&row.AnalysisID, &row.Title, &row.Description, &row.Severity, &row.Status,
			&row.AssigneeID, &row.DuplicateOf, &row.RootCauseKey, &row.ExternalRef,
			&row.CreatedBy, &row.CreatedAt, &row.UpdatedAt, &row.ResolvedAt, &row.TestName,
		); err != nil {
			return Page{}, apierr.Internal(fmt.Errorf("scan a defect: %w", err))
		}

		if len(page.Items) == limit {
			// The extra row only answers "is there another page"; it is not returned.
			page.NextCursor = paging.EncodeTime(page.Items[limit-1].CreatedAt)
			break
		}

		defect, err := toDefect(row)
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, defect)
	}
	if err := rows.Err(); err != nil {
		return Page{}, apierr.Internal(fmt.Errorf("read defects: %w", err))
	}

	return page, nil
}

// Comments returns a defect's thread, oldest first.
func (s *Service) Comments(
	ctx context.Context,
	defectID uuid.UUID,
	limit int,
	cursor string,
) ([]Comment, string, error) {
	size := paging.ClampLimit(limit)

	var after *time.Time
	if cursor != "" {
		at, err := paging.DecodeTime(cursor)
		if err != nil {
			return nil, "", err
		}
		after = &at
	}

	rows, err := s.db.Queries().ListDefectComments(ctx, dbgen.ListDefectCommentsParams{
		DefectID: defectID,
		After:    after,
		PageSize: int32(size + 1), //nolint:gosec // Clamped above.
	})
	if err != nil {
		return nil, "", apierr.Internal(fmt.Errorf("list comments: %w", err))
	}

	comments := make([]Comment, 0, min(len(rows), size))
	next := ""
	for index, row := range rows {
		if index == size {
			next = paging.EncodeTime(comments[size-1].CreatedAt)
			break
		}
		comments = append(comments, Comment{
			ID:        row.ID,
			DefectID:  row.DefectID,
			AuthorID:  row.AuthorID,
			Body:      row.Body,
			System:    row.System,
			CreatedAt: row.CreatedAt,
		})
	}
	return comments, next, nil
}

// decodeExternalRef reads the jsonb column. Empty rather than nil, so a caller does
// not have to distinguish "no tracker" from "no column".
func decodeExternalRef(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}

	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode the external reference: %w", err)
	}
	return decoded, nil
}
