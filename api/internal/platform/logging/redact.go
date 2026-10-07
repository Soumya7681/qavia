package logging

import (
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
)

// Redacted replaces every value the handler considers sensitive. It is a fixed
// string rather than a length hint or a prefix, because a partial value is still
// a leak.
const Redacted = "[redacted]"

// sensitiveSubstrings are matched case-insensitively against attribute keys,
// struct field names, and map keys (requirements.md NFR-10, tech-stack.md 12).
//
// Substring matching over-redacts occasionally. That is the correct direction to
// be wrong in: an over-redacted log line is an inconvenience, a leaked provider
// key is an incident.
var sensitiveSubstrings = []string{
	"key",
	"token",
	"secret",
	"password",
	"passwd",
	"credential",
	"authorization",
}

// maxRedactDepth bounds the reflective walk. Deeply nested payloads are
// truncated rather than risking an unbounded traversal on a cyclic graph.
const maxRedactDepth = 12

// IsSensitiveKey reports whether a name should have its value redacted.
func IsSensitiveKey(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range sensitiveSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// redactAttr is the slog.HandlerOptions.ReplaceAttr hook.
//
// slog calls it for every attribute at every group depth, with values already
// resolved through slog.LogValuer, so a type that implements LogValue has
// already had its say by the time we see it.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	// A sensitive group name redacts everything beneath it. A "credentials"
	// group whose children are named "value" and "region" must not leak the
	// first one just because its own key looks innocent.
	for _, g := range groups {
		if IsSensitiveKey(g) {
			return slog.String(a.Key, Redacted)
		}
	}

	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}

	// Anything logged as a single opaque value still has to be walked: the
	// common leak is slog.Any("provider", provider) where provider carries an
	// APIKey field several levels down.
	if a.Value.Kind() == slog.KindAny {
		if v := a.Value.Any(); v != nil {
			if redacted, changed := redactValue(reflect.ValueOf(v), 0); changed {
				return slog.Any(a.Key, redacted)
			}
		}
	}

	return a
}

// redactValue returns a redacted copy of v and whether anything changed.
//
// When nothing in the type graph looks sensitive it returns changed=false and
// the original value is logged untouched, so ordinary log output keeps its
// natural shape and only risky payloads get rewritten.
func redactValue(v reflect.Value, depth int) (any, bool) {
	if !v.IsValid() {
		return nil, false
	}
	if depth > maxRedactDepth {
		return "[truncated: max depth]", true
	}
	if !typeMayCarrySecrets(v.Type(), make(map[reflect.Type]bool)) {
		return nil, false
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, false
		}
		return redactValue(v.Elem(), depth+1)

	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		t := v.Type()
		for i := range v.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			// The serialised name counts as well as the Go name. A field called
			// Blob with `json:"secret_material"` leaks under either name.
			if IsSensitiveKey(field.Name) || IsSensitiveKey(jsonName(field)) {
				out[field.Name] = Redacted
				continue
			}
			out[field.Name] = redactField(field.Name, v.Field(i), depth)
		}
		return out, true

	case reflect.Map:
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			name := fmt.Sprintf("%v", iter.Key().Interface())
			out[name] = redactField(name, iter.Value(), depth)
		}
		return out, true

	case reflect.Slice, reflect.Array:
		// Byte slices are usually raw key material or a request body. Neither
		// belongs in a log line.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return Redacted, true
		}
		out := make([]any, 0, v.Len())
		for i := range v.Len() {
			elem, changed := redactValue(v.Index(i), depth+1)
			if !changed {
				elem = safeInterface(v.Index(i))
			}
			out = append(out, elem)
		}
		return out, true

	default:
		return nil, false
	}
}

// jsonName returns the serialised field name, ignoring options such as
// ",omitempty".
func jsonName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" || tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}

// redactField applies the key rule to one struct field or map entry, then
// recurses.
func redactField(name string, v reflect.Value, depth int) any {
	if IsSensitiveKey(name) {
		return Redacted
	}
	if redacted, changed := redactValue(v, depth+1); changed {
		return redacted
	}
	return safeInterface(v)
}

// safeInterface reads a value without panicking on an unexported field.
func safeInterface(v reflect.Value) any {
	if !v.IsValid() || !v.CanInterface() {
		return nil
	}
	return v.Interface()
}

var secretTypeCache sync.Map // reflect.Type -> bool

// typeMayCarrySecrets reports whether any reachable field or key name in the
// type graph looks sensitive.
//
// This is the optimisation that keeps ordinary logging untouched: a Project or a
// TestCase has no sensitive field names anywhere, so it is logged exactly as a
// plain JSON handler would log it. Maps and interfaces are always treated as
// possible carriers, because their contents are only known at runtime.
func typeMayCarrySecrets(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if t == nil {
		return false
	}
	if cached, found := secretTypeCache.Load(t); found {
		if result, isBool := cached.(bool); isBool {
			return result
		}
	}
	if visiting[t] {
		// A cycle. Assume the worst rather than recursing forever.
		return true
	}
	visiting[t] = true

	result := computeMayCarrySecrets(t, visiting)

	delete(visiting, t)
	secretTypeCache.Store(t, result)
	return result
}

func computeMayCarrySecrets(t reflect.Type, visiting map[reflect.Type]bool) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Array:
		return typeMayCarrySecrets(t.Elem(), visiting)

	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return true // raw bytes, see redactValue
		}
		return typeMayCarrySecrets(t.Elem(), visiting)

	case reflect.Map, reflect.Interface:
		// Contents are dynamic, so the names cannot be checked ahead of time.
		return true

	case reflect.Struct:
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			if IsSensitiveKey(field.Name) || IsSensitiveKey(jsonName(field)) {
				return true
			}
			if typeMayCarrySecrets(field.Type, visiting) {
				return true
			}
		}
		return false

	default:
		return false
	}
}
