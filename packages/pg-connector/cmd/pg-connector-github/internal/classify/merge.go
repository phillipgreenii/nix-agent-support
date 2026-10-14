package classify

import "encoding/json"

// Merge combines two results of the same entity, earlier then later, the way the
// settle barrier does: kinds are unioned (in canonical order) and, per field
// name, the earliest before and the latest after are kept. A field whose after
// equals its before again is dropped: a value changed and changed back is no
// change. Two values are equal when their compacted JSON is.
//
// Merge is associative over a chain of results (each later before is the
// earlier after, which every classifier output of one entity satisfies).
func Merge(earlier, later Result) Result {
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
	return Result{Kinds: orderKinds(kinds), Fields: normalize(fs)}
}

// queryField is the name of the field that carries a membership row's query.
func queryField(name string) string { return "query:" + name }

// Membership is the Result of an entered_query or left_query row for the named
// query: the kind and one field query:<name>, whose After is the query name (a
// JSON string) for entered_query and whose Before is for left_query. Any other
// kind yields the zero Result.
func Membership(kind, query string) Result {
	name := json.RawMessage(jsonOf(query))
	switch kind {
	case KindEnteredQuery:
		return Result{Kinds: []string{kind}, Fields: []Field{{Name: queryField(query), After: name}}}
	case KindLeftQuery:
		return Result{Kinds: []string{kind}, Fields: []Field{{Name: queryField(query), Before: name}}}
	}
	return Result{}
}

// Removed is the Result of a tombstone: the removed kind and one field
// query:<name> per query that listed the entity, with Before the query name.
// Duplicate names collapse to one field.
func Removed(queries []string) Result {
	var fs []Field
	for _, q := range queries {
		fs = append(fs, Field{Name: queryField(q), Before: jsonOf(q)})
	}
	return Result{Kinds: []string{KindRemoved}, Fields: normalizeDistinct(fs)}
}

// normalizeDistinct is normalize with repeated names collapsed to the first.
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
	return normalize(out)
}
