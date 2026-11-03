// Package query answers the recorder's query and search operations over the
// parsed records: the filter language, grouping and aggregation, links, and
// word and substring search.
package query

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Filter is the JSON filter: a leaf {"field": F, <operator>: V} or a
// combination (all, any, not).
type Filter struct {
	Field    string          `json:"field,omitempty"`
	Eq       json.RawMessage `json:"eq,omitempty"`
	In       []any           `json:"in,omitempty"`
	Range    *Range          `json:"range,omitempty"`
	Under    string          `json:"under,omitempty"`
	Exists   *bool           `json:"exists,omitempty"`
	Contains string          `json:"contains,omitempty"`
	Match    string          `json:"match,omitempty"`
	All      []Filter        `json:"all,omitempty"`
	Any      []Filter        `json:"any,omitempty"`
	Not      *Filter         `json:"not,omitempty"`
}

// Range bounds a value: From inclusive and To exclusive (Gte, Gt, Lte, Lt are
// the explicit forms).
type Range struct {
	From any `json:"from,omitempty"`
	To   any `json:"to,omitempty"`
	Gte  any `json:"gte,omitempty"`
	Gt   any `json:"gt,omitempty"`
	Lte  any `json:"lte,omitempty"`
	Lt   any `json:"lt,omitempty"`
}

type builder struct {
	cat      map[string]source.Kind
	aliases  map[string]string
	joins    []string
	joinArgs []any
	err      *wire.Error
}

func newBuilder(cat map[string]source.Kind) *builder {
	return &builder{cat: cat, aliases: map[string]string{}}
}

func badf(format string, a ...any) *wire.Error {
	return wire.Errf(wire.ErrBadRequest, fmt.Sprintf(format, a...))
}

// resolve maps a query field name to its stored name and kind. A name that no
// source declares is retried under "message." (the example requests write
// model for message.model).
func (b *builder) resolve(name string) (string, source.Kind, *wire.Error) {
	if name == "session_id" {
		name = "session" // the answers' key for the session field
	}
	switch name {
	case "source", "type":
		return name, source.KindKeyword, nil
	case "session":
		return name, source.KindID, nil
	case "time":
		return name, source.KindTime, nil
	case "file", "gen", "offset", "item":
		return name, source.KindInt, nil
	}
	if k, ok := b.cat[name]; ok {
		return name, k, nil
	}
	if k, ok := b.cat["message."+name]; ok {
		return "message." + name, k, nil
	}
	return "", "", badf("unknown field %q", name)
}

// expr returns the SQL expression for a field's value and its kind.
func (b *builder) expr(name string) (string, source.Kind, *wire.Error) {
	stored, kind, err := b.resolve(name)
	if err != nil {
		return "", "", err
	}
	switch stored {
	case "source":
		return "r.source", kind, nil
	case "type":
		return "r.type", kind, nil
	case "session":
		return "r.session", kind, nil
	case "time":
		return "r.time_ns", kind, nil
	case "file":
		return "r.file_id", kind, nil
	case "gen":
		return "r.gen", kind, nil
	case "offset":
		return "r.off", kind, nil
	case "item":
		return "r.item", kind, nil
	}
	if kind.Searchable() {
		return "", kind, badf("field %q is %s: use exists, match or the search operation", name, kind)
	}
	a := b.alias(stored)
	switch kind {
	case source.KindInt, source.KindBool, source.KindTime:
		return a + ".n", kind, nil
	case source.KindFloat:
		return a + ".f", kind, nil
	}
	return a + ".s", kind, nil
}

func (b *builder) alias(stored string) string {
	if a, ok := b.aliases[stored]; ok {
		return a
	}
	a := fmt.Sprintf("f%d", len(b.aliases))
	b.aliases[stored] = a
	b.joins = append(b.joins, fmt.Sprintf("LEFT JOIN fields %s ON %s.rec_id=r.id AND %s.name_id=(SELECT id FROM field_names WHERE name=?)", a, a, a))
	b.joinArgs = append(b.joinArgs, stored)
	return a
}

func conv(kind source.Kind, v any) (any, *wire.Error) {
	switch kind {
	case source.KindTime:
		switch x := v.(type) {
		case string:
			t, ok := source.ParseTime(x)
			if !ok {
				return nil, badf("%q is not an RFC 3339 time", x)
			}
			return t.UnixNano(), nil
		case float64:
			return int64(x), nil
		}
	case source.KindInt:
		if x, ok := v.(float64); ok {
			return int64(x), nil
		}
	case source.KindFloat:
		if x, ok := v.(float64); ok {
			return x, nil
		}
	case source.KindBool:
		if x, ok := v.(bool); ok {
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
	default:
		if x, ok := v.(string); ok {
			return x, nil
		}
	}
	return nil, badf("value %v does not fit a %s field", v, kind)
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// where compiles a filter into a SQL condition and its arguments.
func (b *builder) where(f *Filter) (string, []any, *wire.Error) {
	if f == nil {
		return "1", nil, nil
	}
	switch {
	case len(f.All) > 0 || len(f.Any) > 0:
		list, op := f.All, " AND "
		if len(f.All) == 0 {
			list, op = f.Any, " OR "
		}
		var parts []string
		var args []any
		for i := range list {
			s, a, err := b.where(&list[i])
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, "("+s+")")
			args = append(args, a...)
		}
		return strings.Join(parts, op), args, nil
	case f.Not != nil:
		s, a, err := b.where(f.Not)
		if err != nil {
			return "", nil, err
		}
		return "NOT (" + s + ")", a, nil
	case f.Field == "":
		return "", nil, badf("a filter needs field and an operator, or all, any or not")
	}
	if f.Exists != nil {
		return b.exists(f.Field, *f.Exists)
	}
	if f.Match != "" {
		stored, kind, err := b.resolve(f.Field)
		if err != nil {
			return "", nil, err
		}
		if kind != source.KindText {
			return "", nil, badf("match needs a text field; %q is %s", f.Field, kind)
		}
		q, ok := wordQuery(f.Match)
		if !ok {
			return "", nil, badf("match %q has no words", f.Match)
		}
		return `EXISTS(SELECT 1 FROM texts t WHERE t.rec_id=r.id AND t.field=? AND t.id IN (SELECT rowid FROM fts_word WHERE fts_word MATCH ?))`,
			[]any{stored, q}, nil
	}
	e, kind, err := b.expr(f.Field)
	if err != nil {
		return "", nil, err
	}
	switch {
	case f.Eq != nil:
		var v any
		if json.Unmarshal(f.Eq, &v) != nil {
			return "", nil, badf("eq of %q is not JSON", f.Field)
		}
		cv, err := conv(kind, v)
		if err != nil {
			return "", nil, err
		}
		return e + "=?", []any{cv}, nil
	case f.In != nil:
		if len(f.In) == 0 {
			return "0", nil, nil
		}
		ph := make([]string, len(f.In))
		args := make([]any, len(f.In))
		for i, v := range f.In {
			cv, err := conv(kind, v)
			if err != nil {
				return "", nil, err
			}
			ph[i], args[i] = "?", cv
		}
		return e + " IN (" + strings.Join(ph, ",") + ")", args, nil
	case f.Range != nil:
		var parts []string
		var args []any
		for _, c := range []struct {
			op string
			v  any
		}{{">=", f.Range.From}, {"<", f.Range.To}, {">=", f.Range.Gte}, {">", f.Range.Gt}, {"<=", f.Range.Lte}, {"<", f.Range.Lt}} {
			if c.v == nil {
				continue
			}
			cv, err := conv(kind, c.v)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, e+c.op+"?")
			args = append(args, cv)
		}
		if len(parts) == 0 {
			return "", nil, badf("range of %q has no bounds", f.Field)
		}
		return strings.Join(parts, " AND "), args, nil
	case f.Under != "":
		if kind != source.KindPath {
			return "", nil, badf("under needs a path field; %q is %s", f.Field, kind)
		}
		p := strings.TrimRight(f.Under, "/")
		return "(" + e + "=? OR " + e + ` LIKE ? ESCAPE '\')`, []any{p, likeEscape(p) + "/%"}, nil
	case f.Contains != "":
		switch kind {
		case source.KindID, source.KindKeyword, source.KindPath:
			return "instr(" + e + ",?)>0", []any{f.Contains}, nil
		}
		return "", nil, badf("contains needs an id, keyword or path field; use search for %q", f.Field)
	}
	return "", nil, badf("filter on %q has no operator", f.Field)
}

func (b *builder) exists(field string, want bool) (string, []any, *wire.Error) {
	stored, kind, err := b.resolve(field)
	if err != nil {
		return "", nil, err
	}
	var cond string
	var args []any
	switch stored {
	case "source", "type", "session", "time", "file", "gen", "offset", "item":
		cond = "1"
	default:
		if kind.Searchable() {
			cond, args = "EXISTS(SELECT 1 FROM texts t WHERE t.rec_id=r.id AND t.field=?)", []any{stored}
		} else {
			cond = b.alias(stored) + ".rec_id IS NOT NULL"
		}
	}
	if !want {
		cond = "NOT (" + cond + ")"
	}
	return cond, args, nil
}

// wordQuery builds an FTS5 query from user text: words are quoted tokens
// joined by AND (a text wrapped in double quotes is one phrase), so quote, *,
// NEAR and the other FTS5 syntax are neutralized.
func wordQuery(q string) (string, bool) {
	q = strings.TrimSpace(q)
	phrase := len(q) >= 2 && strings.HasPrefix(q, `"`) && strings.HasSuffix(q, `"`)
	toks := tokens(q)
	if len(toks) == 0 {
		return "", false
	}
	if phrase {
		return `"` + strings.Join(toks, " ") + `"`, true
	}
	for i, t := range toks {
		toks[i] = `"` + t + `"`
	}
	return strings.Join(toks, " AND "), true
}

func isWordRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
}

func tokens(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if isWordRune(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func rfc(ns int64) string {
	if ns == 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}
