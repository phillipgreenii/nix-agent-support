package store

import (
	"strings"
)

// Extension tables (schema v3 slice 5).
//
// An extension table holds typed columns of a record that would otherwise be
// many rows of the generic fields table. The convention:
//
//   - the table is named ext_<name> and is STRICT;
//   - it is keyed by rec_id INTEGER PRIMARY KEY, the same integer as records.id,
//     so joining it to its record is one b-tree seek (one row per record at most);
//   - every other column is nullable: NULL means "absent", never zero;
//   - a source declares a field's place with the store tag ext:<name>.<column>
//     (see source.ParseStorage), and the table MUST be registered in extTables;
//   - it exists only in an index.db built with Options.ExtensionTables (the new
//     file layout), and the parser routes a declared field into it only then; an
//     index.db without the table keeps writing fields rows, so the running layout
//     behaves as before;
//   - a reparse of a file generation deletes the table's rows with the generation's
//     records (the parser's clearGen), and a rebuild writes them again by parsing the
//     copies; nothing else holds them, so the table never needs a separate repair.
//
// To add one, append an ExtTable to extTables and tag the source fields; a
// parser test checks that every declaration names a registered column of a
// compatible type.

// ExtColumn is one typed column of an extension table.
type ExtColumn struct {
	Name string
	// Type is the STRICT column type: INTEGER, REAL or TEXT.
	Type string
}

// ExtTable is a registered extension table. Name is the declaration name (the
// table before its ext_ prefix).
type ExtTable struct {
	Name    string
	Columns []ExtColumn
}

// Table is the SQL table name.
func (t ExtTable) Table() string { return "ext_" + t.Name }

// Column returns the named column.
func (t ExtTable) Column(name string) (ExtColumn, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return ExtColumn{}, false
}

// DDL is the CREATE TABLE statement of the table.
func (t ExtTable) DDL() string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS " + t.Table() + "(\n  rec_id INTEGER PRIMARY KEY")
	for _, c := range t.Columns {
		b.WriteString(",\n  " + c.Name + " " + c.Type)
	}
	b.WriteString("\n) STRICT;\n")
	return b.String()
}

// extTables is the registry. ext_assistant_usage holds the token counters and
// the three small strings of an assistant message's usage block.
var extTables = []ExtTable{
	{Name: "assistant_usage", Columns: []ExtColumn{
		{"input_tokens", "INTEGER"},
		{"output_tokens", "INTEGER"},
		{"cache_creation_input_tokens", "INTEGER"},
		{"cache_read_input_tokens", "INTEGER"},
		{"cache_1h_input_tokens", "INTEGER"},
		{"cache_5m_input_tokens", "INTEGER"},
		{"service_tier", "TEXT"},
		{"inference_geo", "TEXT"},
		{"speed", "TEXT"},
	}},
}

// ExtTables returns a copy of the registry.
func ExtTables() []ExtTable { return append([]ExtTable(nil), extTables...) }

// LookupExt returns the registered extension table with the declaration name.
func LookupExt(name string) (ExtTable, bool) {
	for _, t := range extTables {
		if t.Name == name {
			return t, true
		}
	}
	return ExtTable{}, false
}

func extDDL() string {
	var b strings.Builder
	for _, t := range extTables {
		b.WriteString(t.DDL())
	}
	return b.String()
}

// HasExt reports whether this index.db has the extension tables (the new file
// layout). Without them the parser stores every field in fields.
func (s *Store) HasExt() bool { return s.ext }
