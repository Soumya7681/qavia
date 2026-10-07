package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

// JSONSchema renders the entry's validation as JSON Schema.
//
// The same schema does two jobs: this package validates writes against it, and the
// web app converts it to Zod at runtime for the form. One declaration, two
// enforcement points, no drift (tech-stack.md 11).
func (e Entry) JSONSchema() map[string]any {
	if e.Schema != nil {
		return e.Schema
	}

	schema := map[string]any{"title": e.Label}
	if e.HelpText != "" {
		schema["description"] = e.HelpText
	}

	switch e.Kind {
	case KindString, KindSecret:
		schema["type"] = "string"
	case KindDuration:
		schema["type"] = "string"
		// Deliberately permissive: the exact grammar is checked in Go, and a regex
		// that disagreed with time.ParseDuration would be worse than none.
		schema["pattern"] = `^[0-9]+(ns|us|ms|s|m|h)([0-9]+(ns|us|ms|s|m|h))*$`
	case KindInt:
		schema["type"] = "integer"
		applyBounds(schema, e)
	case KindNumber:
		schema["type"] = "number"
		applyBounds(schema, e)
	case KindBool:
		schema["type"] = "boolean"
	case KindEnum:
		schema["type"] = "string"
		schema["enum"] = e.Enum
	case KindStringList:
		schema["type"] = "array"
		schema["items"] = map[string]any{"type": "string"}
	case KindObject:
		schema["type"] = "object"
	}

	return schema
}

func applyBounds(schema map[string]any, e Entry) {
	if e.Min != nil {
		schema["minimum"] = *e.Min
	}
	if e.Max != nil {
		schema["maximum"] = *e.Max
	}
}

// validateValue checks a stored value against the entry.
//
// This is hand-written rather than run through a JSON Schema library on purpose. The
// set of kinds is small and closed, the error messages have to name the rule that
// failed in words a user can act on, and a schema validator's messages do not.
func (e Entry) validateValue(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("value is not valid JSON")
	}

	switch e.Kind {
	case KindString, KindSecret:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("must be text")
		}

	case KindDuration:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be a duration such as 30s or 15m")
		}
		if _, err := ParseDuration(text); err != nil {
			return fmt.Errorf("must be a duration such as 30s or 15m")
		}

	case KindBool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("must be true or false")
		}

	case KindInt:
		number, err := asFloat(value)
		if err != nil {
			return fmt.Errorf("must be a whole number")
		}
		if number != math.Trunc(number) {
			return fmt.Errorf("must be a whole number")
		}
		return e.checkBounds(number)

	case KindNumber:
		number, err := asFloat(value)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		return e.checkBounds(number)

	case KindEnum:
		text, ok := value.(string)
		if !ok || !slices.Contains(e.Enum, text) {
			return fmt.Errorf("must be one of: %v", e.Enum)
		}

	case KindStringList:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("must be a list of text values")
		}
		for _, item := range items {
			if _, isString := item.(string); !isString {
				return fmt.Errorf("must be a list of text values")
			}
		}

	case KindObject:
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("must be an object")
		}
	}

	return nil
}

func (e Entry) checkBounds(value float64) error {
	switch {
	case e.Min != nil && e.Max != nil && (value < *e.Min || value > *e.Max):
		return fmt.Errorf("must be between %s and %s", trimNumber(*e.Min), trimNumber(*e.Max))
	case e.Min != nil && value < *e.Min:
		return fmt.Errorf("must be at least %s", trimNumber(*e.Min))
	case e.Max != nil && value > *e.Max:
		return fmt.Errorf("must be at most %s", trimNumber(*e.Max))
	default:
		return nil
	}
}

func asFloat(value any) (float64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("not a number")
	}
	return number.Float64()
}

// trimNumber renders a bound without a pointless decimal point, so a message reads
// "at least 30" rather than "at least 30.000000".
func trimNumber(value float64) string {
	if value == math.Trunc(value) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%g", value)
}

// Bound is a helper for declarations, since Go has no address-of for literals.
func Bound(value float64) *float64 { return &value }
