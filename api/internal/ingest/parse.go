package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// Parse normalizes an artifact into the endpoint model, choosing the parser from
// the artifact kind and, where that is ambiguous, from the content.
//
// The kind recorded at upload is a claim by whoever uploaded it. It decides which
// parser is tried first; the content decides whether that was right, because a
// Postman collection uploaded as an OpenAPI specification should produce a useful
// message rather than a validation error about a missing `openapi` field.
func Parse(kind string, filename string, raw []byte) (Document, error) {
	switch Format(kind) {
	case FormatText:
		return ParseText(raw), nil

	case FormatPostman:
		return ParsePostman(raw)

	case FormatOpenAPI:
		if looksLikePostman(raw) {
			// Uploaded under the wrong kind. Parsing it correctly is friendlier than
			// refusing it, and the format is recorded on the result either way.
			return ParsePostman(raw)
		}
		return ParseOpenAPI(raw)

	default:
		return Document{}, apierr.Validation(
			fmt.Sprintf("%q files are not something this platform can parse yet.", kind),
			map[string]any{"field": "kind", "filename": filename})
	}
}

// ParseText wraps pasted requirements.
//
// No structural parse step: the text goes to the extract agent as it is, and the
// source reference is a character range rather than a line, because that is what a
// user's editor can highlight (BE-2.4).
func ParseText(raw []byte) Document {
	text := strings.TrimSpace(string(raw))

	title := "Pasted requirements"
	if first, _, found := strings.Cut(text, "\n"); found && len(first) < 120 {
		if trimmed := strings.TrimSpace(first); trimmed != "" {
			title = trimmed
		}
	}

	return Document{
		Format: FormatText,
		Title:  title,
		Text:   text,
	}
}

// looksLikePostman recognises a collection by its schema field, which every
// export carries.
func looksLikePostman(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}

	var probe struct {
		Info struct {
			Schema string `json:"schema"`
		} `json:"info"`
		OpenAPI string `json:"openapi"`
		Swagger string `json:"swagger"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return false
	}
	if probe.OpenAPI != "" || probe.Swagger != "" {
		return false
	}
	return strings.Contains(probe.Info.Schema, "getpostman.com")
}

// SourceRefFor renders a character range for pasted text.
func SourceRefFor(offset, length int) string {
	return fmt.Sprintf("chars %d-%d", offset, offset+length)
}
