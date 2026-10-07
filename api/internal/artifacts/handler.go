package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// maxFieldBytes bounds a text field in the multipart body. A kind is a short
// enum value; anything larger is not a field somebody meant to send.
const maxFieldBytes = 1 << 10

// Handler implements the artifacts slice of the generated server interface.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// UploadArtifact streams the multipart body straight into the service.
//
// The parts are read in order and the file is never held whole in memory, which
// is why this reads the reader itself rather than calling ReadForm: ReadForm would
// buffer or spool the entire request before any check ran, including the size cap
// the checks exist to enforce.
func (h *Handler) UploadArtifact(
	ctx context.Context,
	request api.UploadArtifactRequestObject,
) (api.UploadArtifactResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := UploadInput{ProjectID: request.ProjectID}

	for {
		part, err := request.Body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apierr.UploadCorrupt("the upload was cut short")
		}

		switch part.FormName() {
		case "kind":
			value, err := io.ReadAll(io.LimitReader(part, maxFieldBytes))
			closeErr := part.Close()
			if err != nil || closeErr != nil {
				return nil, apierr.UploadCorrupt("the kind field could not be read")
			}
			input.Kind = Kind(value)

		case "file":
			if input.Kind == "" {
				// The service needs the kind to place the object, and the file is
				// streamed rather than buffered, so it cannot be held while a later
				// field arrives. Saying so beats an obscure failure.
				return nil, apierr.Validation(
					"Send the kind field before the file part.",
					map[string]any{"field": "kind"})
			}

			input.Filename = part.FileName()
			input.Body = part

			upload, err := h.service.Upload(ctx, actor, input)
			closeErr := part.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close upload part: %w", closeErr)
			}
			return api.UploadArtifact201JSONResponse(ToAPIUpload(upload)), nil

		default:
			if err := part.Close(); err != nil {
				return nil, fmt.Errorf("close unexpected part %q: %w", part.FormName(), err)
			}
		}
	}

	return nil, apierr.Validation("The upload had no file part.",
		map[string]any{"field": "file"})
}

func (h *Handler) ListArtifacts(
	ctx context.Context,
	request api.ListArtifactsRequestObject,
) (api.ListArtifactsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	var kind Kind
	if request.Params.Kind != nil {
		kind = Kind(*request.Params.Kind)
	}

	page, err := h.service.List(ctx, actor, request.ProjectID, kind, limit, cursor)
	if err != nil {
		return nil, err
	}

	body := api.ArtifactPage{Items: make([]api.Artifact, 0, len(page.Items))}
	for _, artifact := range page.Items {
		body.Items = append(body.Items, ToAPI(artifact))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListArtifacts200JSONResponse(body), nil
}

func (h *Handler) GetArtifact(
	ctx context.Context,
	request api.GetArtifactRequestObject,
) (api.GetArtifactResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	artifact, err := h.service.Get(ctx, actor, request.ArtifactID)
	if err != nil {
		return nil, err
	}
	return api.GetArtifact200JSONResponse(ToAPI(artifact)), nil
}

func (h *Handler) ListArtifactVersions(
	ctx context.Context,
	request api.ListArtifactVersionsRequestObject,
) (api.ListArtifactVersionsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	versions, err := h.service.Versions(ctx, actor, request.ArtifactID)
	if err != nil {
		return nil, err
	}

	body := api.ListArtifactVersions200JSONResponse{
		Items: make([]api.Artifact, 0, len(versions)),
	}
	for _, artifact := range versions {
		body.Items = append(body.Items, ToAPI(artifact))
	}
	return body, nil
}

func (h *Handler) DownloadArtifact(
	ctx context.Context,
	request api.DownloadArtifactRequestObject,
) (api.DownloadArtifactResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	artifact, signedURL, body, err := h.service.Download(ctx, actor, request.ArtifactID)
	if err != nil {
		return nil, err
	}
	if signedURL != "" {
		return api.DownloadArtifact302Response{
			Headers: api.DownloadArtifact302ResponseHeaders{Location: &signedURL},
		}, nil
	}
	return &streamedFile{artifact: artifact, body: body}, nil
}

func (h *Handler) DeleteArtifact(
	ctx context.Context,
	request api.DeleteArtifactRequestObject,
) (api.DeleteArtifactResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.service.Delete(ctx, actor, request.ArtifactID); err != nil {
		return nil, err
	}
	return api.DeleteArtifact204Response{}, nil
}

// streamedFile writes the stored bytes to the client and closes the source.
//
// The generated octet-stream response takes an io.Reader and never closes it,
// which would leak a file handle or an HTTP body per download. Satisfying the
// response interface directly keeps the close on the same path as the write.
type streamedFile struct {
	artifact Artifact
	body     io.ReadCloser
}

func (s *streamedFile) VisitDownloadArtifactResponse(w http.ResponseWriter) error {
	defer func() {
		if err := s.body.Close(); err != nil {
			slog.WarnContext(context.Background(), "close artifact body",
				"artifact_id", s.artifact.ID, "error", err)
		}
	}()

	contentType := s.artifact.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(s.artifact.SizeBytes, 10))
	// The filename is already reduced to one safe path segment on upload, and it is
	// quoted so a name with a space does not truncate the header value.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", s.artifact.Filename))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, s.body); err != nil {
		// The status line is already sent, so the client sees a truncated body
		// whatever happens here. Returning it puts the reason in the log.
		return fmt.Errorf("stream artifact %s: %w", s.artifact.ID, err)
	}
	return nil
}
