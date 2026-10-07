package generation

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/testfiles"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects naming, for the export README and the Postman collection title. One
// method, declared here, rather than taking the projects service whole.
type ProjectNames interface {
	Name(ctx context.Context, projectID uuid.UUID) (string, error)
}

func (h *Handler) ListTestFiles(
	ctx context.Context,
	request api.ListTestFilesRequestObject,
) (api.ListTestFilesResponseObject, error) {
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)

	var framework testfiles.Framework
	if request.Params.Framework != nil {
		framework = testfiles.Framework(*request.Params.Framework)
	}

	page, err := h.files.List(ctx, request.ProjectID, framework, limit, cursor)
	if err != nil {
		return nil, err
	}

	summary, err := h.files.Summary(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	body := api.TestFilePage{Items: make([]api.TestFile, 0, len(page.Items))}
	for _, file := range page.Items {
		body.Items = append(body.Items, toAPITestFile(file))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}

	totals := make([]api.TestFileSummary, 0, len(summary))
	for _, item := range summary {
		totals = append(totals, api.TestFileSummary{
			Framework: api.TestFramework(item.Framework),
			Files:     item.Files,
			Bytes:     item.Bytes,
		})
	}
	body.Summary = &totals

	return api.ListTestFiles200JSONResponse(body), nil
}

func (h *Handler) GetTestFile(
	ctx context.Context,
	request api.GetTestFileRequestObject,
) (api.GetTestFileResponseObject, error) {
	file, err := h.authorizedFile(ctx, request.TestFileID)
	if err != nil {
		return nil, err
	}

	base := toAPITestFile(file)
	return api.GetTestFile200JSONResponse(api.TestFileContent{
		Id:             base.Id,
		ProjectId:      base.ProjectId,
		Framework:      base.Framework,
		Path:           base.Path,
		Content:        file.Content,
		TestCaseIds:    base.TestCaseIds,
		GeneratedBy:    base.GeneratedBy,
		ValidatedAt:    base.ValidatedAt,
		ValidationNote: base.ValidationNote,
		SizeBytes:      base.SizeBytes,
		GeneratedAt:    base.GeneratedAt,
		UpdatedAt:      base.UpdatedAt,
	}), nil
}

func (h *Handler) DownloadTestFile(
	ctx context.Context,
	request api.DownloadTestFileRequestObject,
) (api.DownloadTestFileResponseObject, error) {
	file, err := h.authorizedFile(ctx, request.TestFileID)
	if err != nil {
		return nil, err
	}
	return &downloadedFile{file: file}, nil
}

func (h *Handler) ListFilesForTestCase(
	ctx context.Context,
	request api.ListFilesForTestCaseRequestObject,
) (api.ListFilesForTestCaseResponseObject, error) {
	testCase, err := h.authorizedCase(ctx, request.TestCaseID)
	if err != nil {
		return nil, err
	}

	files, err := h.files.ForCase(ctx, testCase.ProjectID, testCase.ID)
	if err != nil {
		return nil, err
	}

	body := api.TestFileList{Items: make([]api.TestFile, 0, len(files))}
	for _, file := range files {
		body.Items = append(body.Items, toAPITestFile(file))
	}
	return api.ListFilesForTestCase200JSONResponse(body), nil
}

func (h *Handler) ExportTestFiles(
	ctx context.Context,
	request api.ExportTestFilesRequestObject,
) (api.ExportTestFilesResponseObject, error) {
	count, err := h.files.Count(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, apierr.Conflict(
			"This project has no generated files yet. Approve test cases and generate code first.")
	}

	name, err := h.names.Name(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	var framework testfiles.Framework
	if request.Params.Framework != nil {
		framework = testfiles.Framework(*request.Params.Framework)
	}

	return &exportedSuite{
		ctx:       ctx,
		files:     h.files,
		projectID: request.ProjectID,
		name:      name,
		framework: framework,
	}, nil
}

func (h *Handler) ExportPostmanCollection(
	ctx context.Context,
	request api.ExportPostmanCollectionRequestObject,
) (api.ExportPostmanCollectionResponseObject, error) {
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	approved, err := h.deps.TestCases.Approved(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(approved) == 0 {
		return nil, apierr.NoApprovedCases()
	}

	name, err := h.names.Name(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	collection, covered, err := testfiles.BuildPostman(name, approved)
	if err != nil {
		return nil, err
	}

	file, err := h.files.Save(ctx, testfiles.SaveInput{
		ProjectID:   request.ProjectID,
		Framework:   testfiles.FrameworkPostman,
		Path:        "postman/" + collectionFilename(name),
		Content:     string(collection),
		TestCaseIDs: covered,
		// Deterministic code wrote this, not a model. Saying so keeps the
		// provenance column honest.
		GeneratedBy: "qavia (deterministic)",
	})
	if err != nil {
		return nil, err
	}
	return api.ExportPostmanCollection201JSONResponse(toAPITestFile(file)), nil
}

// authorizedFile loads a file and checks the caller belongs to its project.
//
// The route carries no project ID, so the membership middleware cannot help: the
// owning project is resolved and the same check applied.
func (h *Handler) authorizedFile(ctx context.Context, id uuid.UUID) (testfiles.File, error) {
	actor := httpx.MustCurrentUser(ctx)

	file, err := h.files.Get(ctx, id)
	if err != nil {
		return testfiles.File{}, err
	}
	if err := h.projects.EnsureMember(ctx, actor, file.ProjectID); err != nil {
		return testfiles.File{}, err
	}
	return file, nil
}

// downloadedFile writes one file's content with a filename the browser will use.
type downloadedFile struct {
	file testfiles.File
}

func (d *downloadedFile) VisitDownloadTestFileResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(d.file.Content)))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", baseName(d.file.Path)))
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write([]byte(d.file.Content)); err != nil {
		return fmt.Errorf("write test file %s: %w", d.file.ID, err)
	}
	return nil
}

// exportedSuite streams the zip straight to the client.
//
// Satisfying the response interface directly rather than using the generated
// application/zip response, which takes an io.Reader: building the archive into a
// buffer first would hold the whole suite in memory to avoid holding it in memory.
type exportedSuite struct {
	ctx       context.Context
	files     *testfiles.Service
	projectID uuid.UUID
	name      string
	framework testfiles.Framework
}

func (e *exportedSuite) VisitExportTestFilesResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", slugFilename(e.name)+"-tests.zip"))
	w.WriteHeader(http.StatusOK)

	if err := e.files.Export(e.ctx, w, e.projectID, e.name, e.framework); err != nil {
		// The status line is already sent, so the client sees a truncated archive
		// whatever happens. Returning it puts the reason in the log.
		return fmt.Errorf("export suite for %s: %w", e.projectID, err)
	}
	return nil
}

func toAPITestFile(file testfiles.File) api.TestFile {
	generatedBy := file.GeneratedBy
	note := file.ValidationNote
	updatedAt := file.UpdatedAt

	out := api.TestFile{
		Id:             file.ID,
		ProjectId:      file.ProjectID,
		Framework:      api.TestFramework(file.Framework),
		Path:           file.Path,
		TestCaseIds:    file.TestCaseIDs,
		GeneratedBy:    &generatedBy,
		ValidationNote: &note,
		SizeBytes:      file.SizeBytes,
		GeneratedAt:    file.GeneratedAt,
		UpdatedAt:      &updatedAt,
	}
	if file.ValidatedAt != nil {
		out.ValidatedAt.Set(*file.ValidatedAt)
	}
	if out.TestCaseIds == nil {
		out.TestCaseIds = []uuid.UUID{}
	}
	return out
}

func baseName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func collectionFilename(projectName string) string {
	return slugFilename(projectName) + ".postman_collection.json"
}

func slugFilename(name string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			if out.Len() > 0 && !strings.HasSuffix(out.String(), "-") {
				out.WriteRune('-')
			}
		}
	}

	trimmed := strings.Trim(out.String(), "-")
	if trimmed == "" {
		return "qavia"
	}
	return trimmed
}
