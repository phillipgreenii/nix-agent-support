package classify

import (
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Result is what a content change amounts to (the shared engine's type,
// re-exported).
type Result = engine.Result

// Field is one changed field of a row (the shared engine's type, re-exported).
type Field = engine.Field

// jsonOf encodes v; the values passed are strings, numbers, bools and small
// structs of those, which always encode.
func jsonOf(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("classify: marshal: " + err.Error())
	}
	return raw
}

// cutUTF8 returns s cut to at most n bytes, never inside a multi-byte rune. A
// limit of zero or less leaves s whole.
func cutUTF8(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// labelSet is labels as a sorted list without duplicates; nil and empty are
// the same (the empty list), so a set is compared and recorded in one form.
func labelSet(labels []string) []string {
	out := slices.Clone(labels)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// edgeSet is the direct edges as a sorted list without duplicates; nil and
// empty are the same.
func edgeSet(deps []schema.IssueDependency) []schema.IssueDependency {
	out := slices.Clone(deps)
	if out == nil {
		out = []schema.IssueDependency{}
	}
	slices.SortFunc(out, func(a, b schema.IssueDependency) int {
		switch {
		case a.ID != b.ID:
			if a.ID < b.ID {
				return -1
			}
			return 1
		case a.Type != b.Type:
			if a.Type < b.Type {
				return -1
			}
			return 1
		}
		return 0
	})
	return slices.Compact(out)
}

// readAnnotations are the keys of schema.Issue that annotate one read of an
// issue rather than describing it; they are never stored or compared.
var readAnnotations = map[string]bool{
	"served_from": true,
	"age_seconds": true,
	"groups":      true,
}

// issueValues is the JSON value of every key of an issue that takes part in a
// comparison, keyed by the schema.Issue JSON name. A key an issue leaves empty
// (omitted from its JSON) is absent from the map. Labels are always present, as
// the sorted set, so nil and empty labels do not differ.
func issueValues(i schema.Issue) map[string]json.RawMessage {
	raw := jsonOf(i)
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		panic("classify: unmarshal: " + err.Error())
	}
	for k := range readAnnotations {
		delete(m, k)
	}
	m["labels"] = jsonOf(labelSet(i.Labels))
	return m
}
