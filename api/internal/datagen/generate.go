package datagen

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"

	"github.com/hyscaler/qavia/api/internal/ingest"
)

// Generation (BE-8.1, F-10.1).
//
// One seeded faker per request, walked over the specification's own schema. The seed
// is the feature: a run that fails on record 63 can be reproduced exactly, and a
// fixture committed by a developer regenerates identically on somebody else's machine.
//
// What the walk honours, in this order, because each is more specific than the last:
// an enum (the value must be one of these), a pattern (it must match this), a format
// (this is an email, a date, a UUID), the field's name (a field called `city` should
// look like a city), and finally its type. A generator that ignored the enum and
// produced a plausible-looking string would produce a hundred records that all fail
// validation, which is a hundred records testing the validator.

// Record is one generated object, ordered so CSV columns are stable.
type Record struct {
	// Fields in the order the schema declared them, which is also the order a CSV
	// header and a SQL insert use. A map alone would reorder on every run and break
	// the byte-identical promise.
	Fields []Field
}

// Field is one generated value.
type Field struct {
	Name  string
	Value any

	// Violates names the constraint a deliberately invalid value breaks, empty for a
	// valid one. It is what makes an invalid set reviewable rather than a pile of
	// junk (BE-8.3.2).
	Violates string
}

// Value returns one field's value by name.
func (r Record) Value(name string) (any, bool) {
	for _, field := range r.Fields {
		if field.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

// Map renders the record as an object for JSON output.
func (r Record) Map() map[string]any {
	out := make(map[string]any, len(r.Fields))
	for _, field := range r.Fields {
		out[field.Name] = field.Value
	}
	return out
}

// Columns are the field names in schema order.
func (r Record) Columns() []string {
	names := make([]string, 0, len(r.Fields))
	for _, field := range r.Fields {
		names = append(names, field.Name)
	}
	return names
}

// Request is what to generate.
type Request struct {
	Schema ingest.Schema

	// Count is how many records. Bounded by the caller: this is free per record, but
	// not free per gigabyte of response.
	Count int

	// Seed makes the result reproducible. Zero means "pick one and tell the caller",
	// because a caller who did not choose a seed still needs to know which one they
	// got in order to ask for the same data again.
	Seed uint64

	// Locale shapes the values that have a country: an address, a phone number, a tax
	// identifier. Empty means the faker's default.
	Locale string
}

// Result is a generated set with the seed that produced it.
type Result struct {
	Seed    uint64
	Records []Record

	// Warnings are schemas the walk could not do much with: an object with no
	// properties, a `$ref` that was truncated. Said rather than silently producing
	// empty records, because an empty record looks like a bug in the endpoint.
	Warnings []string
}

// Generator produces records from a schema.
type Generator struct {
	// locale shapes country-specific values. Held on the generator rather than passed
	// per field, because a record with an Indian address and a US phone number is a
	// record nobody would have entered.
	locale string
}

func NewGenerator() *Generator { return &Generator{} }

// Generate produces Count records from the schema.
func (g *Generator) Generate(request Request) (Result, error) {
	if request.Count <= 0 {
		request.Count = 1
	}
	if request.Count > MaxRecords {
		return Result{}, fmt.Errorf("datagen: %d records is more than the %d this platform generates in one request",
			request.Count, MaxRecords)
	}

	seed := request.Seed
	if seed == 0 {
		// Derived from the clock once, then reported back, so the caller can ask for the
		// same data again. A fixed default would make every unseeded request produce the
		// same hundred rows, which reads as a bug the first time somebody notices.
		seed = uint64(time.Now().UnixNano()) //nolint:gosec // Not a security decision: a reproducibility handle.
	}

	faker := gofakeit.New(seed)
	generator := &Generator{locale: request.Locale}

	result := Result{Seed: seed}
	for index := 0; index < request.Count; index++ {
		record, warnings := generator.record(faker, request.Schema)
		result.Records = append(result.Records, record)

		if index == 0 {
			// Warnings are about the schema, not the record, so the first pass says
			// everything there is to say and repeating it a hundred times says nothing.
			result.Warnings = warnings
		}
	}
	return result, nil
}

// MaxRecords bounds one request. Generation is cheap; serialising a million records
// into one HTTP response is not.
const MaxRecords = 10_000

// record generates one object.
func (g *Generator) record(faker *gofakeit.Faker, schema ingest.Schema) (Record, []string) {
	var warnings []string

	if schema.Type == "array" && schema.Items != nil {
		// A schema describing a list of objects generates one of its elements per
		// record: a hundred records of "an array of users" is what nobody asked for.
		return g.record(faker, *schema.Items)
	}

	if len(schema.Properties) == 0 {
		// A scalar schema, or an object nobody described. Either way there is one value
		// to produce, and calling the column `value` is more honest than inventing a
		// name from the endpoint's path.
		if schema.Type != "" && schema.Type != "object" {
			return Record{Fields: []Field{{Name: "value", Value: g.value(faker, "value", schema)}}}, nil
		}
		return Record{}, []string{"the schema describes an object with no properties, so there is nothing to generate"}
	}

	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}

	fields := make([]Field, 0, len(schema.Properties))
	for _, name := range sortedKeys(schema.Properties) {
		property := schema.Properties[name]

		if property.Ref != "" && property.Type == "" && len(property.Properties) == 0 {
			// The parse truncated a cycle here. Named in the warnings rather than
			// guessed at: a fabricated nested object would look like data.
			warnings = append(warnings,
				fmt.Sprintf("%s references %s, which the parse did not expand, so it is generated as null",
					name, property.Ref))
			fields = append(fields, Field{Name: name, Value: nil})
			continue
		}

		fields = append(fields, Field{Name: name, Value: g.value(faker, name, property)})
	}

	return Record{Fields: fields}, warnings
}

// value produces one field's value.
//
// The order of the checks is the whole of the logic, and each step is more specific
// than the one after it.
func (g *Generator) value(faker *gofakeit.Faker, name string, schema ingest.Schema) any {
	// A payment card is decided before anything else, because the alternative — a
	// faker's Luhn-valid number in a real issuer range — is a number that may belong to
	// somebody (BE-8.4).
	if isCardField(name) {
		return faker.RandomString(AllowedCardNumbers)
	}

	if len(schema.Enum) > 0 {
		// The enum is the constraint. Anything else fails validation, so there is
		// nothing to be clever about.
		return faker.RandomString(schema.Enum)
	}

	if schema.Pattern != "" {
		if generated := g.fromPattern(faker, schema); generated != "" {
			return generated
		}
	}

	switch strings.ToLower(schema.Type) {
	case "integer":
		return g.integer(faker, schema)
	case "number":
		return g.number(faker, schema)
	case "boolean":
		return faker.Bool()
	case "array":
		return g.array(faker, name, schema)
	case "object":
		nested, _ := g.record(faker, schema)
		return nested.Map()
	case "null":
		return nil
	}

	return g.text(faker, name, schema)
}

// fromPattern generates a string matching the schema's regular expression.
//
// Best effort by design: the faker's regex generator covers the common shapes — a
// fixed-length code, a character class, an alternation — and not the whole of PCRE. An
// unsupported pattern falls through to the type-based value rather than failing the
// request, because a pattern this platform cannot honour is a field somebody has to
// look at, not a reason to produce nothing.
func (g *Generator) fromPattern(faker *gofakeit.Faker, schema ingest.Schema) string {
	defer func() {
		// The generator panics on a pattern it cannot parse. Recovered here rather than
		// left to kill the request: one unusual field must not cost the other ninety.
		//nolint:errcheck // The value is deliberately discarded: an unparseable pattern
		// falls through to the type-based value, and there is nobody to report it to.
		recover()
	}()

	generated := faker.Regex(schema.Pattern)
	if generated == "" {
		return ""
	}
	return clampLength(generated, schema)
}

// integer respects the schema's range.
func (g *Generator) integer(faker *gofakeit.Faker, schema ingest.Schema) int64 {
	low, high := int64(0), int64(10_000)
	if schema.Minimum != nil {
		low = int64(math.Ceil(*schema.Minimum))
	}
	if schema.Maximum != nil {
		high = int64(math.Floor(*schema.Maximum))
	}
	if high < low {
		// A schema whose maximum is below its minimum describes nothing. The minimum is
		// the more likely intent, and a generated record has to have a value.
		high = low
	}
	if low == high {
		return low
	}
	return int64(faker.Number(int(low), int(high)))
}

// number respects the schema's range, rounded to something a person would type.
func (g *Generator) number(faker *gofakeit.Faker, schema ingest.Schema) float64 {
	low, high := 0.0, 10_000.0
	if schema.Minimum != nil {
		low = *schema.Minimum
	}
	if schema.Maximum != nil {
		high = *schema.Maximum
	}
	if high < low {
		high = low
	}
	if low == high {
		return low
	}

	// Two decimals, because a generated price with fourteen of them is a value no
	// client ever sends and a comparison nobody can debug.
	return math.Round(faker.Float64Range(low, high)*100) / 100
}

// array generates a list, bounded by the schema's own length constraints.
func (g *Generator) array(faker *gofakeit.Faker, name string, schema ingest.Schema) []any {
	if schema.Items == nil {
		return []any{}
	}

	low, high := 1, 3
	if schema.MinLength != nil {
		low = *schema.MinLength
	}
	if schema.MaxLength != nil {
		high = *schema.MaxLength
	}
	if high < low {
		high = low
	}
	if high > maxArrayItems {
		high = maxArrayItems
	}
	if low > high {
		low = high
	}

	count := low
	if high > low {
		count = faker.IntRange(low, high)
	}

	items := make([]any, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, g.value(faker, name, *schema.Items))
	}
	return items
}

// maxArrayItems bounds a generated list. A schema with maxItems of ten thousand
// describes a limit, not an expectation.
const maxArrayItems = 10

// text produces a string, using the format first and the field's name second.
//
// The name matters more than it looks: a schema is usually `type: string` and nothing
// else, and a hundred records where every field is "consequatur" are records nobody
// can read while debugging. A field called `city` that holds a city name is the
// difference between a fixture somebody trusts and one they replace by hand.
func (g *Generator) text(faker *gofakeit.Faker, name string, schema ingest.Schema) string {
	if generated := g.byFormat(faker, schema.Format); generated != "" {
		return clampLength(generated, schema)
	}
	if generated := g.byName(faker, name); generated != "" {
		return clampLength(generated, schema)
	}

	// Nothing recognisable. A short phrase rather than a random character soup,
	// because a human reads these while working out why a test failed.
	return clampLength(faker.Sentence(3), schema)
}

// byFormat handles the OpenAPI formats that mean something.
func (g *Generator) byFormat(faker *gofakeit.Faker, format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "email", "idn-email":
		return faker.Email()
	case "uuid":
		return faker.UUID()
	case "date":
		return faker.PastDate().UTC().Format("2006-01-02")
	case "date-time":
		return faker.PastDate().UTC().Format(time.RFC3339)
	case "time":
		return faker.PastDate().UTC().Format("15:04:05")
	case "uri", "url", "uri-reference", "iri":
		return faker.URL()
	case "hostname", "idn-hostname":
		return faker.DomainName()
	case "ipv4":
		return faker.IPv4Address()
	case "ipv6":
		return faker.IPv6Address()
	case "byte":
		return faker.LetterN(16)
	case "password":
		// Not a real password policy: a generated one, so a fixture never carries
		// something somebody actually uses.
		return faker.Password(true, true, true, false, false, 16)
	case "phone", "tel":
		return g.phone(faker)
	default:
		return ""
	}
}

// byName reads the field's name, which is usually the only clue a schema gives.
func (g *Generator) byName(faker *gofakeit.Faker, name string) string {
	normalised := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(name))

	switch {
	case strings.Contains(normalised, "email"):
		return faker.Email()
	case strings.HasSuffix(normalised, "id") && len(normalised) <= 12:
		return faker.UUID()
	case strings.Contains(normalised, "firstname"), normalised == "given":
		return faker.FirstName()
	case strings.Contains(normalised, "lastname"), strings.Contains(normalised, "surname"):
		return faker.LastName()
	case strings.Contains(normalised, "fullname"), normalised == "name":
		return faker.Name()
	case strings.Contains(normalised, "company"), strings.Contains(normalised, "organisation"),
		strings.Contains(normalised, "organization"):
		return faker.Company()
	case strings.Contains(normalised, "phone"), strings.Contains(normalised, "mobile"):
		return g.phone(faker)
	case strings.Contains(normalised, "city"), strings.Contains(normalised, "town"):
		return g.city(faker)
	case strings.Contains(normalised, "state"), strings.Contains(normalised, "province"):
		return g.state(faker)
	case strings.Contains(normalised, "country"):
		return g.country(faker)
	case normalised == "zip", strings.Contains(normalised, "postcode"),
		strings.Contains(normalised, "postalcode"), strings.Contains(normalised, "pincode"):
		return g.postcode(faker)
	case strings.Contains(normalised, "address"), strings.Contains(normalised, "street"):
		return g.street(faker)
	case strings.Contains(normalised, "url"), strings.Contains(normalised, "website"):
		return faker.URL()
	case strings.Contains(normalised, "currency"):
		return faker.CurrencyShort()
	case strings.Contains(normalised, "description"), strings.Contains(normalised, "notes"),
		strings.Contains(normalised, "comment"):
		return faker.Sentence(8)
	case strings.Contains(normalised, "title"), strings.Contains(normalised, "subject"):
		return faker.Sentence(4)
	case strings.Contains(normalised, "username"), normalised == "login", normalised == "handle":
		return faker.Username()
	case strings.Contains(normalised, "status"), strings.Contains(normalised, "state"):
		// A schema that constrains status uses an enum, and this branch only runs when
		// it does not: a small fixed set beats a sentence.
		return faker.RandomString([]string{"active", "pending", "inactive"})
	case strings.Contains(normalised, "cvv"), strings.Contains(normalised, "cvc"):
		return faker.Numerify("###")
	case strings.Contains(normalised, "gstin"):
		return g.gstin(faker)
	case strings.Contains(normalised, "pan") && len(normalised) > 3:
		// Not a card number: `pan` alone is handled as one, and `panNumber` in an Indian
		// context is a tax identifier.
		return g.indianPAN(faker)
	default:
		return ""
	}
}

// clampLength keeps a value inside the schema's length bounds.
//
// Truncation rather than regeneration: a value that is too long is too long whatever
// the generator tries next, and a field with maxLength 3 is not going to hold a city
// name however many times it is asked.
func clampLength(value string, schema ingest.Schema) string {
	if schema.MaxLength != nil && *schema.MaxLength > 0 && len(value) > *schema.MaxLength {
		value = value[:*schema.MaxLength]
	}
	if schema.MinLength != nil && len(value) < *schema.MinLength {
		value += strings.Repeat("x", *schema.MinLength-len(value))
	}
	return value
}

// sortedKeys gives the walk a stable order.
//
// The parsed schema holds properties in a map, so iteration order is deliberately
// randomised by the runtime. Sorting is what makes a seed reproduce a record: without
// it, the same seed draws the same values in a different order and produces different
// data every run.
func sortedKeys(properties map[string]ingest.Schema) []string {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
