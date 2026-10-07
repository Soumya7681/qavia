package testcases

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Masterminds/squirrel"
	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/paging"
)

// Filter narrows a project's test cases.
//
// Four optional filters is why this query is built rather than generated: as
// sixteen sqlc queries it would be unreadable, and one generated query with
// COALESCE tricks would be unindexable (backend-standards.md 9). Every value is a
// placeholder; nothing is concatenated.
type Filter struct {
	RequirementID *uuid.UUID
	Category      Category
	Priority      Priority
	Status        Status

	// Search matches the title. Case-insensitive, and a prefix rather than a
	// substring so the index can still be used as the table grows.
	Search string

	Limit  int
	Cursor string
}

// Page is one page of test cases plus the cursor for the next.
type Page struct {
	Items      []TestCase
	NextCursor string
}

// testCaseColumns is the explicit column list. SELECT * is a review failure.
var testCaseColumns = []string{
	"id", "project_id", "requirement_id", "title", "preconditions", "steps", "expected",
	"priority", "category", "status", "fingerprint", "superseded_by",
	"generated_by", "created_by", "created_at", "updated_at", "endpoint",
}

// List returns a filtered page, newest first.
//
// Superseded cases are excluded everywhere: a merged duplicate is history, not a
// row a user should have to filter out themselves.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, filter Filter) (Page, error) {
	limit := paging.ClampLimit(filter.Limit)

	query := squirrel.
		Select(testCaseColumns...).
		From("test_cases").
		Where(squirrel.Eq{"project_id": projectID}).
		Where("superseded_by IS NULL").
		OrderBy("created_at DESC", "id DESC").
		Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)

	if filter.RequirementID != nil {
		query = query.Where(squirrel.Eq{"requirement_id": *filter.RequirementID})
	}
	if filter.Category != "" {
		query = query.Where(squirrel.Eq{"category": string(filter.Category)})
	}
	if filter.Priority != "" {
		query = query.Where(squirrel.Eq{"priority": string(filter.Priority)})
	}
	if filter.Status != "" {
		query = query.Where(squirrel.Eq{"status": string(filter.Status)})
	}
	if filter.Search != "" {
		query = query.Where(squirrel.ILike{"title": filter.Search + "%"})
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
		return Page{}, fmt.Errorf("build test case query: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx, sql, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list test cases: %w", err)
	}
	defer rows.Close()

	page := Page{Items: make([]TestCase, 0, limit)}
	for rows.Next() {
		var (
			testCase TestCase
			steps    []byte
		)
		if err := rows.Scan(
			&testCase.ID, &testCase.ProjectID, &testCase.RequirementID, &testCase.Title,
			&testCase.Preconditions, &steps, &testCase.Expected,
			&testCase.Priority, &testCase.Category, &testCase.Status,
			&fingerprintSink{}, &testCase.SupersededBy, &testCase.GeneratedBy,
			&testCase.CreatedBy, &testCase.CreatedAt, &testCase.UpdatedAt, &testCase.Endpoint,
		); err != nil {
			return Page{}, fmt.Errorf("scan test case: %w", err)
		}

		if len(steps) > 0 {
			if err := json.Unmarshal(steps, &testCase.Steps); err != nil {
				return Page{}, fmt.Errorf("decode steps for %s: %w", testCase.ID, err)
			}
		}
		page.Items = append(page.Items, testCase)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("read test cases: %w", err)
	}

	if len(page.Items) > limit {
		page.NextCursor = paging.EncodeTime(page.Items[limit-1].CreatedAt)
		page.Items = page.Items[:limit]
	}
	return page, nil
}

// fingerprintSink discards the fingerprint column on the read path.
//
// It is selected because the column list is explicit and shared with the write
// path, and discarded because it is an implementation detail of deduplication:
// nothing above this package has any use for it.
type fingerprintSink struct{}

func (fingerprintSink) Scan(any) error { return nil }
