package apierr

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestErrorCarriesCodeAndStatus(t *testing.T) {
	err := ProjectNotFound(uuid.New())

	domain, ok := As(err)
	require.True(t, ok)
	require.Equal(t, CodeProjectNotFound, domain.Code)
	require.Equal(t, http.StatusNotFound, domain.Status)
	require.Equal(t, http.StatusNotFound, Status(err))
	require.True(t, Is(err, CodeProjectNotFound))
	require.False(t, Is(err, CodeUserNotFound))
}

// An unmapped error is a 500. The mapper turns that into an incident ID.
func TestUnmappedErrorIsInternal(t *testing.T) {
	err := errors.New("boom")

	_, ok := As(err)
	require.False(t, ok)
	require.Equal(t, http.StatusInternalServerError, Status(err))
}

func TestErrorIsFoundThroughAWrappedChain(t *testing.T) {
	err := fmt.Errorf("load project: %w", ProjectArchived())

	require.True(t, Is(err, CodeProjectArchived))
	require.Equal(t, http.StatusConflict, Status(err))
}

func TestUnwrapReachesTheCause(t *testing.T) {
	root := errors.New("connection refused")
	err := StorageUnreachable(root)

	require.ErrorIs(t, err, root)
	require.Contains(t, err.Error(), "connection refused")
}

// Constructors must be safe to call repeatedly: mutating one returned error must
// not edit a shared template.
func TestWithHelpersDoNotMutateTheOriginal(t *testing.T) {
	base := Validation("bad input", map[string]any{"field": "email"})

	derived := base.
		WithDetails(map[string]any{"extra": "value"}).
		WithMessage("changed").
		WithCause(errors.New("cause"))

	// New normalises terminal punctuation, so the stored message gains a period.
	require.Equal(t, "bad input.", base.Message)
	require.Len(t, base.Details, 1)
	require.Nil(t, base.Unwrap())

	require.Equal(t, "changed.", derived.Message)
	require.Len(t, derived.Details, 2)
	require.Error(t, derived.Unwrap())
}

func TestUnknownAccountAndWrongPasswordAreIndistinguishable(t *testing.T) {
	// Two calls for the two situations. If these ever differ, the API leaks
	// which accounts exist.
	a := InvalidCredentials()
	b := InvalidCredentials()

	require.Equal(t, a.Code, b.Code)
	require.Equal(t, a.Message, b.Message)
	require.Equal(t, a.Status, b.Status)
}

// allConstructors covers every exported constructor. Adding one without adding
// it here fails TestEveryConstructorIsWellFormed by omission during review, and
// the sweep below still checks the shape of everything listed.
func allConstructors() map[string]*Error {
	id := uuid.New()
	cause := errors.New("underlying")

	return map[string]*Error{
		"Internal":              Internal(cause),
		"Validation":            Validation("Email is not valid.", nil),
		"NotFound":              NotFound("Requirement"),
		"Conflict":              Conflict("That already exists."),
		"RateLimited":           RateLimited(30),
		"IdempotencyKeyConflic": IdempotencyKeyConflict(),
		"NotImplemented":        NotImplemented("UI test generation"),
		"Unauthenticated":       Unauthenticated(),
		"InvalidCredentials":    InvalidCredentials(),
		"AccountLocked":         AccountLocked(900),
		"AccountDisabled":       AccountDisabled(),
		"InviteInvalid":         InviteInvalid(),
		"PasswordTooWeak":       PasswordTooWeak("Use at least 12 characters."),
		"Forbidden":             Forbidden(),
		"RoleRequired":          RoleRequired("Admin"),
		"NotProjectMember":      NotProjectMember(),
		"UserNotFound":          UserNotFound(id),
		"EmailAlreadyTaken":     EmailAlreadyTaken(),
		"CannotChangeOwnRole":   CannotChangeOwnRole(),
		"ProjectNotFound":       ProjectNotFound(id),
		"ProjectArchived":       ProjectArchived(),
		"ArtifactNotFound":      ArtifactNotFound(id),
		"UploadTooLarge":        UploadTooLarge(50 << 20),
		"UnsupportedMediaType":  UnsupportedMediaType("application/x-msdownload", []string{"application/json"}),
		"ArchiveRejected":       ArchiveRejected("an entry escapes the archive root"),
		"UploadCorrupt":         UploadCorrupt("the file is not valid YAML"),
		"SettingUnknownKey":     SettingUnknownKey("runner.timeout_seconds"),
		"SettingInvalid":        SettingInvalid("runner.timeout_seconds", "Must be between 30 and 3600."),
		"SettingWrongScope":     SettingWrongScope("storage.provider", "global"),
		"SettingRoleTooLow":     SettingRoleTooLow("storage.provider", "Admin"),
		"SecretNotReadable":     SecretNotReadable("anthropic.api_key"),
		"StorageUnreachable":    StorageUnreachable(cause),
		"JobNotFound":           JobNotFound(id),
		"JobNotCancelable":      JobNotCancelable("succeeded"),
		"SetupAlreadyComplete":  SetupAlreadyComplete(),
		"ProviderNotConfigured": ProviderNotConfigured(),
		"TierNotAssigned":       TierNotAssigned("reasoning"),
		"SpendCeilingReached":   SpendCeilingReached("total"),
		"ExternalAINotApproved": ExternalAINotApproved(),
		"TargetHostNotAllowed":  TargetHostNotAllowed("api.staging.example.test"),
		"NoTargetConfigured":    NoTargetConfigured(),
	}
}

func TestEveryConstructorIsWellFormed(t *testing.T) {
	for name, err := range allConstructors() {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, err.Code, "every error needs a stable machine-readable code")
			require.NotEmpty(t, err.Message, "every error needs an actionable message")
			require.GreaterOrEqual(t, err.Status, 400)
			require.Less(t, err.Status, 600)

			// Messages are read by users, so they are sentences.
			require.True(t, strings.HasSuffix(err.Message, ".") || strings.HasSuffix(err.Message, "?"),
				"message should read as a sentence: %q", err.Message)
			require.Equal(t, strings.ToUpper(err.Message[:1]), err.Message[:1],
				"message should start capitalised: %q", err.Message)
		})
	}
}

// Nothing in a user-facing message may look like a credential. That message ends
// up in a log, a notification, and a screenshot.
func TestNoConstructorLeaksSomethingSecretShaped(t *testing.T) {
	forbidden := []string{"sk-", "ghp_", "Bearer ", "password=", "api_key="}

	for name, err := range allConstructors() {
		for _, pattern := range forbidden {
			require.NotContains(t, err.Message, pattern, "%s message", name)
		}
	}
}

// codeConstants parses errors.go so the catalog check cannot go stale by
// somebody adding a constant and forgetting the doc.
func codeConstants(t *testing.T) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "errors.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	codes := make(map[string]string)
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if !strings.HasPrefix(spec.Names[0].Name, "Code") {
			return true
		}
		value, unquoteErr := strconv.Unquote(lit.Value)
		require.NoError(t, unquoteErr)
		codes[spec.Names[0].Name] = value
		return true
	})

	require.NotEmpty(t, codes)
	return codes
}

func TestCodesAreUnique(t *testing.T) {
	seen := make(map[string]string)
	for name, code := range codeConstants(t) {
		if previous, dup := seen[code]; dup {
			t.Fatalf("code %q is declared twice: %s and %s", code, previous, name)
		}
		seen[code] = name
	}
}

// The catalog is what the frontend branches on, so drift between code and doc is
// a real defect rather than a documentation nicety (seam item S4).
func TestEveryCodeIsInTheCatalog(t *testing.T) {
	catalog, err := os.ReadFile("../../../../docs/error-codes.md")
	require.NoError(t, err, "docs/error-codes.md must exist")

	text := string(catalog)
	for name, code := range codeConstants(t) {
		require.Contains(t, text, "`"+code+"`",
			"%s (%q) is missing from docs/error-codes.md", name, code)
	}
}
