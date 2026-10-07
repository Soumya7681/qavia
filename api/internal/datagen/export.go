package datagen

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Export (BE-8.5, F-10.5).
//
// Three formats, all streamed: a hundred thousand rows must not be assembled in memory
// before the first byte leaves, because the whole point of generating data is that it
// is cheap enough to ask for a lot of it.
//
// The SQL writer is the one with a real decision in it. Generated data is going into
// somebody's database, so every value is quoted by this code rather than interpolated
// — a generated string containing an apostrophe would otherwise end the statement, and
// the injection set from BE-8.3 contains values designed to do exactly that. The
// output also says, in a comment at the top, that it is test data and which seed
// produced it, because a file of INSERTs found later with no provenance is a file
// nobody dares run or delete.

// Format is an export format.
type Format string

const (
	FormatJSON Format = "json"
	FormatCSV  Format = "csv"
	FormatSQL  Format = "sql"
)

// Valid reports whether the format is one this platform writes.
func (f Format) Valid() bool {
	switch f {
	case FormatJSON, FormatCSV, FormatSQL:
		return true
	default:
		return false
	}
}

// ContentType is what to serve the export as.
func (f Format) ContentType() string {
	switch f {
	case FormatJSON:
		return "application/json"
	case FormatCSV:
		return "text/csv; charset=utf-8"
	case FormatSQL:
		return "application/sql; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Extension is the filename suffix.
func (f Format) Extension() string { return string(f) }

// ExportOptions describe one export.
type ExportOptions struct {
	Format Format

	// Table names the SQL target. Ignored by the other formats.
	Table string

	// Seed is written into the SQL header so a file found six months later can be
	// regenerated rather than guessed at.
	Seed uint64
}

// Write streams records in the requested format.
func Write(out io.Writer, records []Record, options ExportOptions) error {
	switch options.Format {
	case FormatJSON:
		return writeJSON(out, records)
	case FormatCSV:
		return writeCSV(out, records)
	case FormatSQL:
		return writeSQL(out, records, options)
	default:
		return fmt.Errorf("datagen: %q is not a format this platform writes", options.Format)
	}
}

// writeJSON streams an array, one record at a time.
//
// Written by hand rather than with json.Marshal on the whole slice, because marshalling
// the slice builds the entire document in memory first: the streaming shape is the
// requirement, and it costs two lines to honour it.
func writeJSON(out io.Writer, records []Record) error {
	if _, err := io.WriteString(out, "[\n"); err != nil {
		return err
	}

	encoder := json.NewEncoder(out)
	for index, record := range records {
		if index > 0 {
			if _, err := io.WriteString(out, ",\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(out, "  "); err != nil {
			return err
		}
		if err := encoder.Encode(record.Map()); err != nil {
			return fmt.Errorf("write a JSON record: %w", err)
		}
	}

	_, err := io.WriteString(out, "]\n")
	return err
}

// writeCSV streams a header and one row per record.
//
// The columns are the union of every record's fields, in a stable order, because an
// invalid set omits a required field in one case and a CSV with a different column
// count per row is a CSV nothing can read.
func writeCSV(out io.Writer, records []Record) error {
	columns := unionColumns(records)

	writer := csv.NewWriter(out)
	if err := writer.Write(columns); err != nil {
		return fmt.Errorf("write the CSV header: %w", err)
	}

	for _, record := range records {
		row := make([]string, 0, len(columns))
		for _, column := range columns {
			value, present := record.Value(column)
			if !present {
				// An omitted field is an empty cell rather than the string "null": one of
				// those imports as missing and the other as the text "null".
				row = append(row, "")
				continue
			}
			row = append(row, cell(value))
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("write a CSV row: %w", err)
		}
	}

	writer.Flush()
	return writer.Error()
}

// writeSQL streams INSERT statements.
func writeSQL(out io.Writer, records []Record, options ExportOptions) error {
	table := sanitiseIdentifier(options.Table)
	if table == "" {
		table = "test_data"
	}

	columns := unionColumns(records)
	if len(columns) == 0 {
		return nil
	}

	header := fmt.Sprintf(
		"-- Qavia generated test data. Not real data: every value here was produced by a\n"+
			"-- seeded generator, and payment card numbers come only from published test ranges.\n"+
			"-- Seed %d, generated %s. The same seed reproduces this file exactly.\n"+
			"-- Safe to delete; unsafe to treat as anybody's records.\n\n",
		options.Seed, time.Now().UTC().Format(time.RFC3339))
	if _, err := io.WriteString(out, header); err != nil {
		return err
	}

	quotedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		quotedColumns = append(quotedColumns, quoteIdentifier(column))
	}

	for _, record := range records {
		values := make([]string, 0, len(columns))
		for _, column := range columns {
			value, present := record.Value(column)
			if !present {
				values = append(values, "NULL")
				continue
			}
			values = append(values, literal(value))
		}

		statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);\n",
			quoteIdentifier(table), strings.Join(quotedColumns, ", "), strings.Join(values, ", "))
		if _, err := io.WriteString(out, statement); err != nil {
			return err
		}
	}
	return nil
}

// unionColumns is every field name across the records, in a stable order.
func unionColumns(records []Record) []string {
	seen := map[string]bool{}
	var columns []string

	for _, record := range records {
		for _, field := range record.Fields {
			if !seen[field.Name] {
				seen[field.Name] = true
				columns = append(columns, field.Name)
			}
		}
	}

	// Sorted, because the first record's order is not a contract when later records
	// carry different fields, and a CSV whose columns move between runs breaks every
	// diff taken against it.
	sort.Strings(columns)
	return columns
}

// cell renders a value for CSV.
func cell(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		// A nested object or list. JSON in one cell, which is what a spreadsheet and a
		// loader both cope with; the alternative is flattening, and a flattened column
		// name is a column name nobody's schema has.
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

// literal renders a value as a SQL literal.
//
// Quoted here, by this code, for every string. Nothing is interpolated raw: the
// injection set exists precisely to produce values that would end a statement early,
// and generated data ending up as executable SQL in a client's database is the worst
// thing this module could do.
func literal(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case bool:
		return strconv.FormatBool(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return quoteLiteral(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "NULL"
		}
		return quoteLiteral(string(encoded))
	}
}

// quoteLiteral renders a string as a SQL string literal.
//
// Doubling the apostrophe is the standard escape and works on every engine this data
// might land in. A backslash-escaped form would be MySQL-specific and wrong on
// Postgres with standard_conforming_strings on, which is the default.
func quoteLiteral(value string) string {
	// A null byte cannot appear in a text literal on Postgres at all, and the injection
	// set contains one. Dropped rather than escaped, with the rest of the value intact,
	// because the alternative is a file that fails to import at row 4000.
	value = strings.ReplaceAll(value, "\x00", "")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// quoteIdentifier renders a column or table name safely.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(sanitiseIdentifier(name), `"`, `""`) + `"`
}

// sanitiseIdentifier reduces a name to something safe to quote.
//
// A caller-supplied table name reaches this, so it is filtered rather than trusted:
// quoting alone would let a name containing a quote and a semicolon carry a second
// statement.
func sanitiseIdentifier(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		case r == '.':
			// A schema-qualified name is common and harmless once every other character
			// is filtered.
			return r
		default:
			return -1
		}
	}, strings.TrimSpace(name))

	if len(cleaned) > 63 {
		// Postgres truncates identifiers at 63 bytes anyway, and truncating here means
		// the file says what the database will do.
		cleaned = cleaned[:63]
	}
	return cleaned
}
