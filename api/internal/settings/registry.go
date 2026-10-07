// Package settings owns the registry, scoped resolution, cache, and secret
// handling (F-1.5, F-1.7).
//
// This is the most load-bearing package in the codebase, and the reason is
// economic: every setting is declared exactly once, and that single declaration
// drives the database write, the validation, the UI control, the permission check,
// and the restart warning. Adding a setting later is one Declare call, with no
// migration, no new screen, and no new form component. That is what makes UI-first
// configuration (requirements.md 5) affordable rather than a permanent tax.
//
// Two rules that exist to prevent specific failures:
//
//   - A duplicate key panics at boot. A failed start beats a silent override that
//     nobody notices until the wrong value takes effect.
//   - An undeclared key returns an error, never a zero value. A zero that quietly
//     means "off" is the exact failure this design prevents
//     (backend-standards.md 6).
package settings

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hyscaler/qavia/api/internal/role"
)

// Kind is the type of a setting value. It selects the UI control, the JSON Schema,
// and the typed accessor that may read it.
type Kind string

const (
	KindString     Kind = "string"
	KindInt        Kind = "int"
	KindNumber     Kind = "number"
	KindBool       Kind = "bool"
	KindEnum       Kind = "enum"
	KindStringList Kind = "string_list"
	KindDuration   Kind = "duration"
	KindObject     Kind = "object"

	// KindSecret is stored encrypted and never returned. The read path yields
	// {isSet, updatedAt, hint} (F-1.8).
	KindSecret Kind = "secret"
)

// Scope is where a value may be stored.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
)

// Entry is one setting's complete declaration.
type Entry struct {
	Key string

	// Category groups the entry on the settings screen. The UI renders groups from
	// this rather than from a hardcoded layout.
	Category string

	Label    string
	HelpText string

	Kind Kind

	// Default is the value used when no row exists at any scope. It is the last
	// step of resolution, and it is why an unset setting is never a zero value.
	Default any

	// Scopes lists where the setting may be written, strongest first. A setting
	// declared global-only cannot be overridden per project.
	Scopes []Scope

	// MinRole is the weakest role allowed to change it. User-scoped settings use
	// Viewer, because everyone may edit their own preferences.
	MinRole role.Role

	// RestartRequired badges the setting in the UI and warns on save. Almost
	// nothing needs this: it exists for values read once at boot, such as the
	// object-store driver.
	RestartRequired bool

	// Enum lists the permitted values for KindEnum.
	Enum []string

	// Min and Max bound numeric kinds. Nil means unbounded.
	Min *float64
	Max *float64

	// Schema overrides the generated JSON Schema for KindObject, where the shape is
	// specific to the setting.
	Schema map[string]any
}

// IsSecret reports whether the value is encrypted at rest and withheld on read.
func (e Entry) IsSecret() bool { return e.Kind == KindSecret }

// AllowsScope reports whether the setting may be written at the given scope.
func (e Entry) AllowsScope(scope Scope) bool { return slices.Contains(e.Scopes, scope) }

// Registry holds every declared setting.
//
// It is a type rather than only a package global so a test can build an isolated
// one, but production uses the single default registry that the declaration files
// populate.
type Registry struct {
	entries map[string]Entry
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]Entry)}
}

// defaultRegistry is populated by the Declare calls in the registry_*.go files.
var defaultRegistry = NewRegistry()

// Default returns the registry the application uses.
func Default() *Registry { return defaultRegistry }

// Declare adds a setting to the default registry.
//
// It panics on a bad declaration, and that is correct: these are called from init,
// so the alternative to a failed boot is a running process with a broken settings
// screen.
func Declare(entry Entry) {
	if err := defaultRegistry.Add(entry); err != nil {
		panic("settings: " + err.Error())
	}
}

// Add validates and registers one entry.
func (r *Registry) Add(entry Entry) error {
	if err := entry.validate(); err != nil {
		return err
	}
	if existing, duplicate := r.entries[entry.Key]; duplicate {
		return fmt.Errorf("duplicate setting key %q (already declared in category %q)",
			entry.Key, existing.Category)
	}
	r.entries[entry.Key] = entry
	return nil
}

// Lookup returns the entry for a key.
func (r *Registry) Lookup(key string) (Entry, bool) {
	entry, found := r.entries[key]
	return entry, found
}

// Entries returns every entry, sorted by category then key, so the API response and
// therefore the UI ordering is stable.
func (r *Registry) Entries() []Entry {
	out := slices.Collect(maps.Values(r.entries))
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Keys returns every declared key, sorted.
func (r *Registry) Keys() []string {
	keys := slices.Collect(maps.Keys(r.entries))
	sort.Strings(keys)
	return keys
}

// Len reports how many settings are declared.
func (r *Registry) Len() int { return len(r.entries) }

func (e Entry) validate() error {
	switch {
	case e.Key == "":
		return fmt.Errorf("setting has no key (label %q)", e.Label)
	case !strings.Contains(e.Key, "."):
		// Keys are namespaced so the screens group naturally and two features
		// cannot collide on a bare word like "timeout".
		return fmt.Errorf("setting key %q must be namespaced, for example runner.timeout_seconds", e.Key)
	case e.Label == "":
		return fmt.Errorf("setting %q has no label", e.Key)
	case e.Category == "":
		return fmt.Errorf("setting %q has no category", e.Key)
	case e.Kind == "":
		return fmt.Errorf("setting %q has no kind", e.Key)
	case len(e.Scopes) == 0:
		return fmt.Errorf("setting %q declares no scope", e.Key)
	case !e.MinRole.Valid():
		return fmt.Errorf("setting %q has an invalid minimum role %q", e.Key, e.MinRole)
	case e.Kind == KindEnum && len(e.Enum) == 0:
		return fmt.Errorf("setting %q is an enum with no values", e.Key)
	}

	for _, scope := range e.Scopes {
		if scope != ScopeGlobal && scope != ScopeProject && scope != ScopeUser {
			return fmt.Errorf("setting %q declares unknown scope %q", e.Key, scope)
		}
	}

	// A secret has no meaningful default, and shipping one would put a credential
	// in the source tree.
	if e.IsSecret() && e.Default != nil {
		return fmt.Errorf("setting %q is a secret and must not declare a default", e.Key)
	}

	// Every non-secret needs a default, because resolution ends at the default and
	// an absent one would mean a zero value after all.
	if !e.IsSecret() && e.Default == nil {
		return fmt.Errorf("setting %q has no default; resolution ends at the default, "+
			"so an absent one reintroduces the zero value this design prevents", e.Key)
	}

	if e.Default != nil {
		if err := e.validateValue(mustJSON(e.Default)); err != nil {
			return fmt.Errorf("setting %q has a default that fails its own validation: %w", e.Key, err)
		}
	}
	return nil
}

// DefaultJSON renders the declared default as it would be stored.
func (e Entry) DefaultJSON() (json.RawMessage, error) {
	if e.Default == nil {
		return nil, fmt.Errorf("setting %q has no default", e.Key)
	}
	encoded, err := json.Marshal(e.Default)
	if err != nil {
		return nil, fmt.Errorf("settings: encode default for %q: %w", e.Key, err)
	}
	return encoded, nil
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		// Only reachable from a declaration with an unmarshalable default, which is
		// a programming error caught at boot.
		panic(fmt.Sprintf("settings: default is not JSON serialisable: %v", err))
	}
	return encoded
}

// ParseDuration accepts the duration form used by duration settings.
func ParseDuration(value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("settings: %q is not a duration, for example 30s or 15m", value)
	}
	return parsed, nil
}
