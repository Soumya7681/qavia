package artifacts

import (
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Kind is what an input is, which decides how it is parsed.
//
// The values match the check constraint on the table. Values grow with the
// phases, which is why the column is text with a check rather than an enum.
type Kind string

const (
	KindOpenAPI         Kind = "openapi"
	KindPostman         Kind = "postman"
	KindRequirementText Kind = "requirement_text"
	KindSourceArchive   Kind = "source_archive"
	KindSQLDump         Kind = "sql_dump"
	KindDocument        Kind = "document"
)

// Valid reports whether the kind is one the database will accept.
func (k Kind) Valid() bool {
	switch k {
	case KindOpenAPI, KindPostman, KindRequirementText, KindSourceArchive,
		KindSQLDump, KindDocument:
		return true
	default:
		return false
	}
}

// IsArchive reports whether a kind arrives compressed, and therefore has to pass
// the bomb checks before it is stored.
func (k Kind) IsArchive() bool { return k == KindSourceArchive }

// Artifact is an uploaded or connected input.
type Artifact struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Kind        Kind
	Filename    string
	StorageKey  string
	ContentType string
	SizeBytes   int64

	// SHA256 is the identity of the content. It is what makes an identical
	// re-upload free and a changed file a new version (FR-1.4).
	SHA256 []byte

	Version   int
	LineageID uuid.UUID

	UploadedBy *uuid.UUID
	CreatedAt  time.Time
}

// Hex renders the checksum for a response.
func (a Artifact) Hex() string { return hex.EncodeToString(a.SHA256) }

// Page is one page of artifacts plus the cursor for the next.
type Page struct {
	Items      []Artifact
	NextCursor string
}

// Upload is the result of storing a file.
type Upload struct {
	Artifact Artifact

	// Deduplicated is true when this exact content was already stored, so nothing
	// was written and no generation will be re-run.
	Deduplicated bool
}

func toArtifact(row dbgen.Artifact) Artifact {
	return Artifact{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		Kind:        Kind(row.Kind),
		Filename:    row.Filename,
		StorageKey:  row.StorageKey,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		SHA256:      row.Sha256,
		Version:     int(row.Version),
		LineageID:   row.LineageID,
		UploadedBy:  row.UploadedBy,
		CreatedAt:   row.CreatedAt,
	}
}

// ToAPI maps an artifact to its response shape. The storage key is deliberately
// absent: it describes where the platform put the file, which is not a client's
// business and would leak the bucket layout.
func ToAPI(a Artifact) api.Artifact {
	out := api.Artifact{
		Id:          a.ID,
		ProjectId:   a.ProjectID,
		Kind:        api.ArtifactKind(a.Kind),
		Filename:    a.Filename,
		ContentType: a.ContentType,
		SizeBytes:   a.SizeBytes,
		Sha256:      a.Hex(),
		Version:     a.Version,
		LineageId:   a.LineageID,
		CreatedAt:   a.CreatedAt,
	}
	if a.UploadedBy != nil {
		out.UploadedBy.Set(*a.UploadedBy)
	}
	return out
}

// ToAPIUpload maps an upload, which is an artifact plus whether it was new.
func ToAPIUpload(u Upload) api.Artifact {
	out := ToAPI(u.Artifact)
	deduplicated := u.Deduplicated
	out.Deduplicated = &deduplicated
	return out
}
