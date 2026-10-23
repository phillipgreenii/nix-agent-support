package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// QueryFieldPrefix starts the name of the field that names a membership row's
// query: one field query:<name> per query.
const QueryFieldPrefix = "query:"

// Result is what a content change amounts to: the change kinds it carries and
// the fields that changed, each with its before and after value.
//
// Kinds are unique and in the connector's canonical order; Fields are sorted
// by name, so a Result has one spelling and two equal changes compare equal. A
// change to a recorded field yields a Result even when it carries no kind
// (every content change is delivered); a change that moves only excluded
// fields yields the zero Result.
type Result struct {
	Kinds  []string
	Fields []Field
}

// Empty reports whether r has no kinds and no fields.
func (r Result) Empty() bool { return len(r.Kinds) == 0 && len(r.Fields) == 0 }

// Field is one changed field of a row. Before and After are JSON values; a
// JSON null, or a missing side, is the empty RawMessage.
type Field struct {
	Name   string          `json:"name"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// wireField is Field's JSON form: the same names, with an empty side spelled
// null (an empty RawMessage is not valid JSON on its own).
type wireField struct {
	Name   string          `json:"name"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// MarshalJSON writes a missing side as null.
func (f Field) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireField{Name: f.Name, Before: nullIfEmpty(f.Before), After: nullIfEmpty(f.After)})
}

// UnmarshalJSON reads a null or absent side as the empty RawMessage and keeps
// the other values in compact form.
func (f *Field) UnmarshalJSON(b []byte) error {
	var w wireField
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	f.Name, f.Before, f.After = w.Name, tidy(w.Before), tidy(w.After)
	return nil
}

func nullIfEmpty(r json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(r)) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// tidy returns r compacted, or nil when r is empty or the JSON null.
func tidy(r json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(r)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, t); err != nil {
		return slices.Clone(t)
	}
	return json.RawMessage(buf.Bytes())
}

// EncodeFields is the stored form of Result.Fields: a JSON array, [] for none.
func EncodeFields(fs []Field) (json.RawMessage, error) {
	if len(fs) == 0 {
		return json.RawMessage("[]"), nil
	}
	for _, f := range fs {
		for _, side := range []json.RawMessage{f.Before, f.After} {
			if len(bytes.TrimSpace(side)) > 0 && !json.Valid(side) {
				return nil, fmt.Errorf("engine: field %q holds a value that is not JSON", f.Name)
			}
		}
	}
	raw, err := json.Marshal(fs)
	if err != nil {
		return nil, fmt.Errorf("engine: encode fields: %w", err)
	}
	return raw, nil
}

// DecodeFields reads the stored form back. An empty or null input is no fields.
func DecodeFields(raw json.RawMessage) ([]Field, error) {
	if tidy(raw) == nil {
		return nil, nil
	}
	var fs []Field
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, fmt.Errorf("engine: decode fields: %w", err)
	}
	if fs == nil {
		fs = []Field{}
	}
	return fs, nil
}

// Normalize returns fs without the fields whose after equals their before,
// with compact values, sorted by name.
func Normalize(fs []Field) []Field {
	var out []Field
	for _, f := range fs {
		f.Before, f.After = tidy(f.Before), tidy(f.After)
		if bytes.Equal(f.Before, f.After) {
			continue
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b Field) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}

// OrderKinds returns the distinct kinds of kinds in the canonical order given.
// A name that is not in order sorts after those that are, alphabetically, so a
// caller's typo is kept rather than lost.
func OrderKinds(order, kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	rank := make(map[string]int, len(order))
	for i, k := range order {
		rank[k] = i
	}
	out := slices.Clone(kinds)
	slices.SortFunc(out, func(a, b string) int {
		ra, oka := rank[a]
		rb, okb := rank[b]
		switch {
		case oka && okb:
			return ra - rb
		case oka:
			return -1
		case okb:
			return 1
		}
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	})
	return slices.Compact(out)
}

// Merge combines two results of the same entity, earlier then later, the way
// the settle barrier does: kinds are unioned (in the order given) and, per
// field name, the earliest before and the latest after are kept. A field whose
// after equals its before again is dropped: a value changed and changed back is
// no change. Two values are equal when their compacted JSON is.
//
// Merge is associative over a chain of results (each later before is the
// earlier after, which every classifier output of one entity satisfies).
func Merge(order []string, earlier, later Result) Result {
	byName := map[string]Field{}
	for _, f := range earlier.Fields {
		byName[f.Name] = f
	}
	for _, f := range later.Fields {
		if e, ok := byName[f.Name]; ok {
			byName[f.Name] = Field{Name: f.Name, Before: e.Before, After: f.After}
		} else {
			byName[f.Name] = f
		}
	}
	fs := make([]Field, 0, len(byName))
	for _, f := range byName {
		fs = append(fs, f)
	}
	kinds := append(append([]string(nil), earlier.Kinds...), later.Kinds...)
	return Result{Kinds: OrderKinds(order, kinds), Fields: Normalize(fs)}
}

// queryName is a query name as the JSON string a membership field holds.
func queryName(query string) json.RawMessage {
	raw, err := json.Marshal(query)
	if err != nil {
		panic("engine: marshal: " + err.Error())
	}
	return raw
}

// MembershipResult is the Result of an arrival or departure row for the named
// query: the kind and one field query:<name>, whose After is the query name (a
// JSON string) for roles.EnteredQuery and whose Before is for roles.LeftQuery.
// Any other kind yields the zero Result.
func MembershipResult(roles registry.Roles, kind, query string) Result {
	name := queryName(query)
	switch {
	case kind == "":
		return Result{}
	case kind == roles.EnteredQuery:
		return Result{Kinds: []string{kind}, Fields: []Field{{Name: QueryFieldPrefix + query, After: name}}}
	case kind == roles.LeftQuery:
		return Result{Kinds: []string{kind}, Fields: []Field{{Name: QueryFieldPrefix + query, Before: name}}}
	}
	return Result{}
}

// RemovedResult is the Result of a tombstone: removedKind and one field
// query:<name> per query that listed the entity, with Before the query name.
// Duplicate names collapse to one field.
func RemovedResult(removedKind string, queries []string) Result {
	var fs []Field
	for _, q := range queries {
		fs = append(fs, Field{Name: QueryFieldPrefix + q, Before: queryName(q)})
	}
	return Result{Kinds: []string{removedKind}, Fields: normalizeDistinct(fs)}
}

// normalizeDistinct is Normalize with repeated names collapsed to the first.
func normalizeDistinct(fs []Field) []Field {
	seen := map[string]bool{}
	var out []Field
	for _, f := range fs {
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		out = append(out, f)
	}
	return Normalize(out)
}
