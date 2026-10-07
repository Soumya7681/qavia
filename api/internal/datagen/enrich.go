package datagen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/llm"
)

// AI assist, for the handful of fields where semantics matter (BE-8.2, F-10.2).
//
// The rule this file exists to enforce: **the bulk stays free**. A hundred records of
// twenty fields is two thousand values, and a model asked for all of them costs money
// per record to be occasionally wrong in ways nobody notices until an assertion fails.
// The faker produces those two thousand values instantly and identically every time.
//
// What a model is actually better at is narrow: a street address that reads like
// somewhere a person lives, a product description that matches the product, a tax
// identifier whose shape a validator will accept in a country the faker knows nothing
// about. So enrichment is **opt-in per field**, one call for the whole set rather than
// one per record, and the faker's value stays whenever the call fails, returns the
// wrong shape, or is not configured at all. A field the model did not fill is a field
// that still has a plausible value.

// Enricher rewrites named fields with values a model produced.
type Enricher interface {
	// Enrich mutates the records in place and returns anything worth telling the
	// caller. It never returns an error: the faker's values are already there, and
	// failing a generation because the optional half of it did not work would be
	// choosing the worse outcome.
	Enrich(ctx context.Context, input EnrichInput) []string
}

// EnrichInput is one enrichment pass.
type EnrichInput struct {
	ProjectID uuid.UUID

	// Records are the faker's output, mutated in place.
	Records []Record

	// Fields are the ones the caller opted in.
	Fields []string

	Schema ingest.Schema
	Locale string
}

// Gateway is the slice of the AI gateway this package needs.
type Gateway interface {
	TestData(ctx context.Context, call llm.AgentCall, input llm.TestDataInput) (llm.AgentResult, error)
}

// AIEnricher is the built-in enricher.
type AIEnricher struct {
	gateway Gateway
}

func NewAIEnricher(gateway Gateway) *AIEnricher { return &AIEnricher{gateway: gateway} }

// maxEnrichedRecords bounds one call.
//
// A model asked for realistic values for five hundred records produces a reply too
// large to be reliable and a bill nobody expected. Past this, the requested fields are
// filled for the first batch and the rest keep the faker's values, which is said out
// loud rather than left to be noticed.
const maxEnrichedRecords = 50

// maxEnrichedFields caps the opt-in. Somebody who selects every field has misunderstood
// what this is for, and the honest answer is to fill some of them and say so.
const maxEnrichedFields = 8

func (e *AIEnricher) Enrich(ctx context.Context, input EnrichInput) []string {
	fields := requestedFields(input.Fields, input.Schema)
	if len(fields) == 0 {
		return []string{"none of the named fields exist in this schema, so every value came from the faker"}
	}

	var notes []string
	if len(fields) > maxEnrichedFields {
		notes = append(notes, fmt.Sprintf(
			"%d fields were named and %d were enriched: the rest kept the faker's values, "+
				"because bulk generation is deliberately not an AI feature",
			len(fields), maxEnrichedFields))
		fields = fields[:maxEnrichedFields]
	}

	records := input.Records
	if len(records) > maxEnrichedRecords {
		notes = append(notes, fmt.Sprintf(
			"the first %d of %d records were enriched; the rest kept the faker's values",
			maxEnrichedRecords, len(records)))
		records = records[:maxEnrichedRecords]
	}

	result, err := e.gateway.TestData(ctx, llm.AgentCall{ProjectID: &input.ProjectID},
		llm.TestDataInput{
			Fields:  describeFields(fields, input.Schema),
			Count:   len(records),
			Locale:  input.Locale,
			Context: schemaSummary(input.Schema),
		})
	if err != nil {
		// The faker's values are already in the records. Reported as a note rather than
		// an error, because a generation that succeeded with slightly less realistic
		// addresses is a better outcome than no generation at all.
		slog.WarnContext(ctx, "field enrichment failed, keeping the faker's values",
			"project_id", input.ProjectID, "error", err)
		return append(notes,
			"the AI field pass failed, so every value came from the faker: "+err.Error())
	}

	values, err := decodeValues(result.Raw)
	if err != nil {
		return append(notes, "the AI field pass returned something unreadable, "+
			"so every value came from the faker: "+err.Error())
	}

	applied := 0
	for _, field := range fields {
		column, present := values[field]
		if !present {
			continue
		}

		for index := range records {
			if index >= len(column) {
				// Fewer values than records. The remaining records keep the faker's, which
				// is why enrichment is a rewrite of an already-complete set rather than the
				// only source of a value.
				break
			}
			records[index] = records[index].withField(field, column[index], "")
			applied++
		}
	}

	if applied == 0 {
		return append(notes, "the AI field pass returned no usable values, so the faker's stand")
	}
	return append(notes, fmt.Sprintf("%d value(s) across %d field(s) came from the model; "+
		"everything else came from the faker", applied, len(fields)))
}

// requestedFields keeps the named fields that actually exist, in schema order.
func requestedFields(named []string, schema ingest.Schema) []string {
	if schema.Type == "array" && schema.Items != nil {
		schema = *schema.Items
	}

	wanted := make(map[string]bool, len(named))
	for _, name := range named {
		wanted[strings.TrimSpace(name)] = true
	}

	var fields []string
	for _, name := range sortedKeys(schema.Properties) {
		if wanted[name] {
			fields = append(fields, name)
		}
	}
	return fields
}

// describeFields tells the model what each field is, from the schema rather than from
// the field's name alone: a description, a format, and a length bound are what turn
// "write something realistic" into a value that fits the column.
func describeFields(fields []string, schema ingest.Schema) []map[string]any {
	if schema.Type == "array" && schema.Items != nil {
		schema = *schema.Items
	}

	described := make([]map[string]any, 0, len(fields))
	for _, name := range fields {
		property := schema.Properties[name]

		entry := map[string]any{"name": name}
		if property.Type != "" {
			entry["type"] = property.Type
		}
		if property.Format != "" {
			entry["format"] = property.Format
		}
		if property.Description != "" {
			entry["description"] = property.Description
		}
		if property.Pattern != "" {
			entry["pattern"] = property.Pattern
		}
		if property.MaxLength != nil {
			entry["maxLength"] = *property.MaxLength
		}
		described = append(described, entry)
	}
	return described
}

// schemaSummary is the cacheable context: what the object is, so a field called `line1`
// is understood as part of an address rather than as a line of text.
func schemaSummary(schema ingest.Schema) map[string]any {
	if schema.Type == "array" && schema.Items != nil {
		schema = *schema.Items
	}

	return map[string]any{
		"description": schema.Description,
		"fields":      sortedKeys(schema.Properties),
	}
}

// decodeValues reads the agent's reply: one list of values per field name.
func decodeValues(raw json.RawMessage) (map[string][]any, error) {
	var reply struct {
		Fields []struct {
			Name   string `json:"name"`
			Values []any  `json:"values"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("the reply could not be read: %w", err)
	}

	values := make(map[string][]any, len(reply.Fields))
	for _, field := range reply.Fields {
		if field.Name == "" || len(field.Values) == 0 {
			continue
		}
		values[field.Name] = field.Values
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("the reply named no fields")
	}
	return values, nil
}
