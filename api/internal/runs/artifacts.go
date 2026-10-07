package runs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// The artifact channel (BE-7.5).
//
// A failing UI test is worth a video, a trace, and a screenshot, and none of them can
// be copied out of the container: the workspace is a tmpfs mount, Docker cannot copy
// a file out of one, and the workspace is a tmpfs precisely because the container's
// root filesystem is read-only. So the image writes a manifest beside its report,
// with each file base64 encoded, and it arrives through the same stdout channel the
// report does.
//
// The manifest names what it dropped as well as what it carries. A cap is necessary —
// a suite where forty tests failed would otherwise push a gigabyte through a pipe —
// and "there is no video" is a different fact from "the video was 40 MB", which is the
// difference between a bug in the platform and a limit doing its job.

// ArtifactsPath is where an image writes its artifact manifest, relative to the
// workspace. Optional: an image that captures nothing simply does not write it.
const ArtifactsPath = ".qavia/artifacts.json"

// ArtifactManifest is what an image captured for a run.
type ArtifactManifest struct {
	Schema  string            `json:"schema"`
	Items   []ArtifactItem    `json:"items"`
	Dropped []DroppedArtifact `json:"dropped,omitempty"`
}

// ArtifactItem is one recording, still encoded.
type ArtifactItem struct {
	// Test and Attempt are how a recording finds its result row. The name is the same
	// one the report used, because both come from the same reporter output.
	Test    string `json:"test"`
	Attempt int    `json:"attempt"`

	// Kind is video, trace, or screenshot.
	Kind string `json:"kind"`

	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Bytes       int64  `json:"bytes"`

	Base64 string `json:"base64"`
}

// DroppedArtifact is a recording the image chose not to send, and why.
type DroppedArtifact struct {
	Test    string `json:"test"`
	Attempt int    `json:"attempt"`
	Kind    string `json:"kind"`
	Why     string `json:"why"`
}

// Decode returns the recording's bytes.
func (a ArtifactItem) Decode() ([]byte, error) {
	content, err := base64.StdEncoding.DecodeString(a.Base64)
	if err != nil {
		return nil, fmt.Errorf("the %s for %q could not be decoded: %w", a.Kind, a.Test, err)
	}
	return content, nil
}

// ParseArtifacts decodes an artifact manifest.
//
// An unreadable manifest is not a failed run. The results are what a run is for, and
// losing the evidence for one of them is worth a log line rather than throwing away
// the suite's outcome — the same rule the log upload follows.
func ParseArtifacts(raw []byte) (ArtifactManifest, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return ArtifactManifest{}, nil
	}

	var manifest ArtifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ArtifactManifest{}, fmt.Errorf("the artifact manifest could not be read: %w", err)
	}

	if manifest.Schema != "qavia.artifacts/1" {
		return ArtifactManifest{}, fmt.Errorf(
			"the runner reported artifact schema %q, and this platform reads qavia.artifacts/1",
			manifest.Schema)
	}
	return manifest, nil
}

// ArtifactKind names the three things a browser records. Kept as constants because
// they are also column names on the result row, and a typo in one of those is a
// silently missing video.
const (
	ArtifactVideo      = "video"
	ArtifactTrace      = "trace"
	ArtifactScreenshot = "screenshot"
)
