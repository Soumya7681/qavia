// Package testfiles owns generated test files (F-6.1, F-6.9, F-6.10, F-6.11).
//
// Thin on purpose. This phase is a layer over what phase 2 produced, and it stays
// small only because the test cases are good: the agent turns approved cases into
// a file, and everything else here is storage, browsing, and export.
package testfiles

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// maxFileBytes bounds one generated file.
//
// A test file larger than this is not a test file: it is a model that lost the
// thread, and storing it would put something unreviewable in front of a user.
const maxFileBytes = 512 << 10

// Framework is what a file is written for.
type Framework string

const (
	FrameworkSupertest  Framework = "supertest"
	FrameworkPostman    Framework = "postman"
	FrameworkJest       Framework = "jest"
	FrameworkVitest     Framework = "vitest"
	FrameworkPlaywright Framework = "playwright"
	FrameworkCypress    Framework = "cypress"
	FrameworkK6         Framework = "k6"
	FrameworkPytest     Framework = "pytest"
	FrameworkSecurity   Framework = "security"
)

// Valid reports whether the framework is one the database will accept.
func (f Framework) Valid() bool {
	switch f {
	case FrameworkSupertest, FrameworkPostman, FrameworkJest, FrameworkVitest,
		FrameworkPlaywright, FrameworkCypress, FrameworkK6, FrameworkPytest, FrameworkSecurity:
		return true
	default:
		return false
	}
}

// Implemented reports whether this phase can generate the framework yet.
//
// Only Supertest and Postman are delivered here. The rest are declared so the API
// and the UI agree about what exists, and asking for one returns a reason rather
// than nothing (F-3.12).
func (f Framework) Implemented() bool {
	return f == FrameworkSupertest || f == FrameworkPostman
}

// File is one generated file.
type File struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Framework Framework
	Path      string

	// Content is empty on the browsing path. A 300-file suite is not something to
	// load whole to draw a sidebar (BE-3.5).
	Content string

	TestCaseIDs []uuid.UUID
	GeneratedBy string

	// ValidatedAt is nil until static validation has run in the runner. Nil is not
	// the same as failed: it means nobody has checked yet (BE-3.4).
	ValidatedAt    *time.Time
	ValidationNote string

	SizeBytes   int
	GeneratedAt time.Time
	UpdatedAt   time.Time
}

// Page is one page of files plus the cursor for the next.
//
// The cursor is the path rather than a timestamp: the tree is ordered by path, and
// paging by anything else would make a file browser jump around.
type Page struct {
	Items      []File
	NextCursor string
}

// Service owns the test_files table.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// SaveInput is one generated file.
type SaveInput struct {
	ProjectID   uuid.UUID
	Framework   Framework
	Path        string
	Content     string
	TestCaseIDs []uuid.UUID
	GeneratedBy string
}

// Save stores a file, replacing whatever was at the same path.
//
// The path is normalized and checked here rather than trusted: a model wrote it,
// and "../../etc/cron.d/whatever" is a plausible thing for a model to produce when
// it has seen a repository layout (backend-standards.md 13).
func (s *Service) Save(ctx context.Context, input SaveInput) (File, error) {
	if !input.Framework.Valid() {
		return File{}, apierr.Validation(
			fmt.Sprintf("%q is not a framework this platform knows.", input.Framework),
			map[string]any{"field": "framework"})
	}

	cleaned, err := SafePath(input.Path)
	if err != nil {
		return File{}, err
	}
	if strings.TrimSpace(input.Content) == "" {
		return File{}, apierr.Validation("The generated file was empty.",
			map[string]any{"field": "content", "path": cleaned})
	}
	if len(input.Content) > maxFileBytes {
		return File{}, apierr.Validation(
			fmt.Sprintf("The generated file for %s is larger than %d KB, which is not a test file.",
				cleaned, maxFileBytes>>10),
			map[string]any{"field": "content", "path": cleaned})
	}

	// An empty slice rather than nil: the column is not null, and a file that covers no
	// stored test case is a normal thing — a generated unit test covers a function, not
	// a reviewed case (BE-6.5).
	cases := input.TestCaseIDs
	if cases == nil {
		cases = []uuid.UUID{}
	}

	row, err := s.db.Queries().UpsertTestFile(ctx, dbgen.UpsertTestFileParams{
		ProjectID:   input.ProjectID,
		Framework:   dbgen.TestFramework(input.Framework),
		Path:        cleaned,
		Content:     input.Content,
		TestCaseIds: cases,
		GeneratedBy: input.GeneratedBy,
		SizeBytes:   int32(len(input.Content)),
	})
	if err != nil {
		return File{}, fmt.Errorf("store test file %s: %w", cleaned, err)
	}
	return toFile(row), nil
}

// List returns a page of the tree, without content.
func (s *Service) List(
	ctx context.Context,
	projectID uuid.UUID,
	framework Framework,
	limit int,
	cursor string,
) (Page, error) {
	limit = paging.ClampLimit(limit)

	params := dbgen.ListTestFilesParams{ProjectID: projectID, PageSize: int32(limit + 1)}
	if framework != "" {
		value := dbgen.NullTestFramework{TestFramework: dbgen.TestFramework(framework), Valid: true}
		params.Framework = value
	}
	if cursor != "" {
		decoded, err := decodePathCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		params.Cursor = &decoded
	}

	rows, err := s.db.Queries().ListTestFiles(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list test files: %w", err)
	}

	page := Page{Items: make([]File, 0, limit)}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = encodePathCursor(rows[i-1].Path)
			break
		}
		page.Items = append(page.Items, File{
			ID:             row.ID,
			ProjectID:      row.ProjectID,
			Framework:      Framework(row.Framework),
			Path:           row.Path,
			TestCaseIDs:    row.TestCaseIds,
			GeneratedBy:    row.GeneratedBy,
			ValidatedAt:    row.ValidatedAt,
			ValidationNote: row.ValidationNote,
			SizeBytes:      int(row.SizeBytes),
			GeneratedAt:    row.GeneratedAt,
			UpdatedAt:      row.UpdatedAt,
		})
	}
	return page, nil
}

// Get loads one file with its content.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (File, error) {
	row, err := s.db.Queries().GetTestFile(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return File{}, apierr.NotFound("Test file")
		}
		return File{}, fmt.Errorf("load test file %s: %w", id, err)
	}
	return toFile(row), nil
}

// All returns the whole suite with content, for export.
func (s *Service) All(ctx context.Context, projectID uuid.UUID, framework Framework) ([]File, error) {
	params := dbgen.ListTestFilesForExportParams{ProjectID: projectID}
	if framework != "" {
		params.Framework = dbgen.NullTestFramework{
			TestFramework: dbgen.TestFramework(framework), Valid: true,
		}
	}

	rows, err := s.db.Queries().ListTestFilesForExport(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("read suite for export: %w", err)
	}

	files := make([]File, 0, len(rows))
	for _, row := range rows {
		files = append(files, toFile(row))
	}
	return files, nil
}

// ForCase answers "where is this case implemented", the traceability half of
// F-6.11.
func (s *Service) ForCase(ctx context.Context, projectID, caseID uuid.UUID) ([]File, error) {
	rows, err := s.db.Queries().FindTestFilesForCase(ctx,
		dbgen.FindTestFilesForCaseParams{ProjectID: projectID, Column2: caseID})
	if err != nil {
		return nil, fmt.Errorf("find files for case %s: %w", caseID, err)
	}

	files := make([]File, 0, len(rows))
	for _, row := range rows {
		files = append(files, File{
			ID:             row.ID,
			ProjectID:      row.ProjectID,
			Framework:      Framework(row.Framework),
			Path:           row.Path,
			TestCaseIDs:    row.TestCaseIds,
			GeneratedBy:    row.GeneratedBy,
			ValidatedAt:    row.ValidatedAt,
			ValidationNote: row.ValidationNote,
			SizeBytes:      int(row.SizeBytes),
			GeneratedAt:    row.GeneratedAt,
			UpdatedAt:      row.UpdatedAt,
		})
	}
	return files, nil
}

// RecordValidation stores what the runner's static check found (BE-3.4).
// MarkValidated records that the toolchain checked this file and found nothing.
//
// The distinction the three states carry (BE-3.4): a nil validated_at means nobody
// has checked, an empty note with a timestamp means checked and clean, and a note
// means checked and rejected. "Not checked" and "failed" must not look the same.
func (s *Service) MarkValidated(ctx context.Context, id uuid.UUID, note string) error {
	return s.RecordValidation(ctx, id, note)
}

// MarkRejected records the findings that rejected a file. The file is kept: a
// rejected file a reviewer can read beats a silent gap in the suite.
func (s *Service) MarkRejected(ctx context.Context, id uuid.UUID, note string) error {
	return s.RecordValidation(ctx, id, note)
}

func (s *Service) RecordValidation(ctx context.Context, id uuid.UUID, note string) error {
	if err := s.db.Queries().SetTestFileValidation(ctx, dbgen.SetTestFileValidationParams{
		ID: id, ValidationNote: note,
	}); err != nil {
		return fmt.Errorf("record validation for %s: %w", id, err)
	}
	return nil
}

// Summary is what the suite looks like per framework.
type Summary struct {
	Framework Framework
	Files     int64
	Bytes     int64
}

func (s *Service) Summary(ctx context.Context, projectID uuid.UUID) ([]Summary, error) {
	rows, err := s.db.Queries().CountTestFilesByFramework(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("summarise test files: %w", err)
	}

	out := make([]Summary, 0, len(rows))
	for _, row := range rows {
		out = append(out, Summary{
			Framework: Framework(row.Framework),
			Files:     row.Total,
			Bytes:     row.Bytes,
		})
	}
	return out, nil
}

// Count reports how many files a project has.
func (s *Service) Count(ctx context.Context, projectID uuid.UUID) (int64, error) {
	count, err := s.db.Queries().CountTestFiles(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("count test files: %w", err)
	}
	return count, nil
}

// SafePath normalizes a model-supplied path and refuses anything that escapes.
//
// Cleaned first, then checked: "tests/../../etc/passwd" only looks safe before
// cleaning, and a prefix check on an uncleaned path is not a control.
func SafePath(raw string) (string, error) {
	candidate := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	candidate = strings.TrimPrefix(candidate, "./")

	switch {
	case candidate == "":
		return "", apierr.Validation("The generated file had no path.",
			map[string]any{"field": "path"})
	case strings.HasPrefix(candidate, "/"):
		return "", pathRejected(raw, "it is absolute")
	case strings.ContainsRune(candidate, 0):
		return "", pathRejected(raw, "it contains a null byte")
	}

	cleaned := path.Clean(candidate)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", pathRejected(raw, "it climbs out of the suite")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", pathRejected(raw, "it climbs out of the suite")
		}
	}
	return cleaned, nil
}

func pathRejected(raw, reason string) error {
	return apierr.Validation(
		fmt.Sprintf("The generated file path %q was rejected: %s.", raw, reason),
		map[string]any{"field": "path"})
}

func toFile(row dbgen.TestFile) File {
	return File{
		ID:             row.ID,
		ProjectID:      row.ProjectID,
		Framework:      Framework(row.Framework),
		Path:           row.Path,
		Content:        row.Content,
		TestCaseIDs:    row.TestCaseIds,
		GeneratedBy:    row.GeneratedBy,
		ValidatedAt:    row.ValidatedAt,
		ValidationNote: row.ValidationNote,
		SizeBytes:      int(row.SizeBytes),
		GeneratedAt:    row.GeneratedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}
