package settings

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/role"
)

func validEntry() Entry {
	return Entry{
		Key:      "example.timeout_seconds",
		Category: "Example",
		Label:    "Timeout",
		Kind:     KindInt,
		Default:  30,
		Min:      Bound(1),
		Max:      Bound(3600),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	}
}

// The whole registry is exercised at boot by init, so this asserts the real
// declarations are well formed rather than only a synthetic one.
func TestDefaultRegistryIsPopulatedAndValid(t *testing.T) {
	registry := Default()
	require.Positive(t, registry.Len())

	for _, entry := range registry.Entries() {
		require.NoError(t, entry.validate(), entry.Key)
	}
}

// Entries are sorted by category then key so the API response, and therefore the UI
// ordering, is stable rather than dependent on map iteration.
func TestEntriesAreStablyOrdered(t *testing.T) {
	first := Default().Entries()
	second := Default().Entries()

	require.Equal(t, first, second)
	for i := 1; i < len(first); i++ {
		previous, current := first[i-1], first[i]
		if previous.Category == current.Category {
			require.Less(t, previous.Key, current.Key)
			continue
		}
		require.Less(t, previous.Category, current.Category)
	}
}

// A duplicate key is a failed boot rather than a silent override that nobody notices
// until the wrong value takes effect.
func TestDuplicateKeyIsRejected(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Add(validEntry()))

	err := registry.Add(validEntry())
	require.ErrorContains(t, err, "duplicate setting key")
}

func TestDeclarePanicsOnABadEntry(t *testing.T) {
	require.Panics(t, func() {
		Declare(Entry{Key: "no.label", Category: "X", Kind: KindBool, Default: false,
			Scopes: []Scope{ScopeGlobal}, MinRole: role.Admin})
	})
}

func TestEntryValidation(t *testing.T) {
	tests := map[string]struct {
		mutate    func(*Entry)
		wantInErr string
	}{
		"no key":             {func(e *Entry) { e.Key = "" }, "has no key"},
		"key not namespaced": {func(e *Entry) { e.Key = "timeout" }, "must be namespaced"},
		"no label":           {func(e *Entry) { e.Label = "" }, "has no label"},
		"no category":        {func(e *Entry) { e.Category = "" }, "has no category"},
		"no kind":            {func(e *Entry) { e.Kind = "" }, "has no kind"},
		"no scope":           {func(e *Entry) { e.Scopes = nil }, "declares no scope"},
		"unknown scope":      {func(e *Entry) { e.Scopes = []Scope{"tenant"} }, "unknown scope"},
		"bad role":           {func(e *Entry) { e.MinRole = "root" }, "invalid minimum role"},
		"enum with no values": {
			func(e *Entry) { e.Kind = KindEnum; e.Default = "a"; e.Enum = nil },
			"enum with no values",
		},
		// Resolution ends at the default, so an absent one puts the zero value back.
		"no default": {func(e *Entry) { e.Default = nil }, "has no default"},
		// Shipping a secret default would put a credential in the source tree.
		"secret with default": {
			func(e *Entry) { e.Kind = KindSecret; e.Default = "sk-ant-oops" },
			"must not declare a default",
		},
		// A default that fails the entry's own rules is a contradiction, and it would
		// surface as a mysterious validation error the first time somebody saved.
		"default out of bounds": {
			func(e *Entry) { e.Default = 99999 },
			"fails its own validation",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			entry := validEntry()
			tc.mutate(&entry)

			err := NewRegistry().Add(entry)
			require.ErrorContains(t, err, tc.wantInErr)
		})
	}
}

func TestLookupOfAnUndeclaredKey(t *testing.T) {
	_, found := Default().Lookup("nothing.declared_here")
	require.False(t, found)
}

// Every key is namespaced so two features cannot collide on a bare word, and the
// screens group naturally.
func TestEveryDeclaredKeyIsNamespaced(t *testing.T) {
	for _, key := range Default().Keys() {
		require.Contains(t, key, ".", key)
		require.Equal(t, strings.ToLower(key), key, "keys are lowercase: %s", key)
	}
}

// A secret must never carry a default, or the source tree ends up holding
// credentials.
func TestNoDeclaredSecretHasADefault(t *testing.T) {
	for _, entry := range Default().Entries() {
		if entry.IsSecret() {
			require.Nil(t, entry.Default, entry.Key)
		}
	}
}

// User-scoped entries use Viewer as their minimum because everyone edits their own
// preferences. The scope, not the role, is what stops one user editing another's.
func TestUserScopedSettingsAreEditableByEveryRole(t *testing.T) {
	for _, entry := range Default().Entries() {
		if entry.AllowsScope(ScopeUser) {
			require.True(t, role.Viewer.AtLeast(entry.MinRole),
				"%s is user-scoped, so a Viewer must be able to edit their own", entry.Key)
		}
	}
}

func TestSchemaIsGeneratedPerKind(t *testing.T) {
	tests := map[Kind]string{
		KindString:     "string",
		KindInt:        "integer",
		KindNumber:     "number",
		KindBool:       "boolean",
		KindEnum:       "string",
		KindStringList: "array",
		KindDuration:   "string",
		KindObject:     "object",
		KindSecret:     "string",
	}

	for kind, wantType := range tests {
		t.Run(string(kind), func(t *testing.T) {
			entry := Entry{Key: "x.y", Label: "X", Kind: kind, Enum: []string{"a"}}
			require.Equal(t, wantType, entry.JSONSchema()["type"])
		})
	}
}

func TestSchemaCarriesBoundsAndEnum(t *testing.T) {
	numeric := validEntry().JSONSchema()
	require.EqualValues(t, 1, numeric["minimum"])
	require.EqualValues(t, 3600, numeric["maximum"])

	enum := Entry{Key: "x.y", Label: "X", Kind: KindEnum, Enum: []string{"a", "b"}}
	require.Equal(t, []string{"a", "b"}, enum.JSONSchema()["enum"])
}

// A real registry entry's schema is what the frontend converts to its own validator,
// so it has to be complete enough to render a control from.
func TestDeclaredSchemasAreRenderable(t *testing.T) {
	for _, entry := range Default().Entries() {
		schema := entry.JSONSchema()
		require.NotEmpty(t, schema["type"], entry.Key)
		require.NotEmpty(t, schema["title"], entry.Key)

		encoded, err := json.Marshal(schema)
		require.NoError(t, err, entry.Key)
		require.NotEmpty(t, encoded)
	}
}

func TestValidateValue(t *testing.T) {
	tests := []struct {
		name      string
		entry     Entry
		value     string
		wantInErr string
	}{
		{"int ok", validEntry(), `60`, ""},
		{"int as text", validEntry(), `"60"`, "must be a whole number"},
		{"int fractional", validEntry(), `1.5`, "must be a whole number"},
		{"int below minimum", validEntry(), `0`, "must be between 1 and 3600"},
		{"int above maximum", validEntry(), `99999`, "must be between 1 and 3600"},
		{
			"bool ok",
			Entry{Key: "x.y", Kind: KindBool},
			`true`, "",
		},
		{
			"bool as text",
			Entry{Key: "x.y", Kind: KindBool},
			`"true"`, "must be true or false",
		},
		{
			"enum ok",
			Entry{Key: "x.y", Kind: KindEnum, Enum: []string{"local-disk", "s3"}},
			`"s3"`, "",
		},
		{
			"enum unknown value",
			Entry{Key: "x.y", Kind: KindEnum, Enum: []string{"local-disk", "s3"}},
			`"gcs"`, "must be one of",
		},
		{
			"duration ok",
			Entry{Key: "x.y", Kind: KindDuration},
			`"15m"`, "",
		},
		{
			"duration nonsense",
			Entry{Key: "x.y", Kind: KindDuration},
			`"fortnight"`, "must be a duration",
		},
		{
			"list ok",
			Entry{Key: "x.y", Kind: KindStringList},
			`["a","b"]`, "",
		},
		{
			"list of numbers",
			Entry{Key: "x.y", Kind: KindStringList},
			`[1,2]`, "must be a list of text values",
		},
		{
			"object ok",
			Entry{Key: "x.y", Kind: KindObject},
			`{"a":1}`, "",
		},
		{
			"object is a list",
			Entry{Key: "x.y", Kind: KindObject},
			`[]`, "must be an object",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.entry.validateValue(json.RawMessage(tc.value))
			if tc.wantInErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantInErr)
		})
	}
}

// Bound messages read as words a user can act on, not as float formatting.
func TestBoundMessagesAreReadable(t *testing.T) {
	lower := Entry{Key: "x.y", Kind: KindInt, Min: Bound(30)}
	require.ErrorContains(t, lower.validateValue(json.RawMessage(`10`)), "at least 30")

	upper := Entry{Key: "x.y", Kind: KindInt, Max: Bound(10)}
	require.ErrorContains(t, upper.validateValue(json.RawMessage(`11`)), "at most 10")
}

func TestScopeListRendersReadably(t *testing.T) {
	require.Equal(t, "global", scopeList([]Scope{ScopeGlobal}))
	require.Equal(t, "user or global", scopeList([]Scope{ScopeUser, ScopeGlobal}))
	require.Equal(t, "user, project or global",
		scopeList([]Scope{ScopeUser, ScopeProject, ScopeGlobal}))
}
