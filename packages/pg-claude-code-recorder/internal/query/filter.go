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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
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
	// used counts, per stored field name, the filter leaves that named it.
	used map[string]int
	// claudeDir is the directory the recorded paths live under; it is what
	// project and file_session are read back from (see pathFact). Empty means
	// no path derivation: those two fields are then read from fields only.
	claudeDir string
	pathJoin  bool
	// sessionRef reads the session through records.session_ref and the sessions
	// table instead of records.session (see Engine.SessionRef).
	sessionRef bool
	// typed reads the fields a source declares with record: or ext: storage
	// (storage) from their typed columns instead of from fields rows; set only
	// for an index.db in the new layout (Engine.TypedStorage).
	typed   bool
	storage map[string]source.Storage
	// idWalk keeps the planner from choosing an index for the low-selectivity
	// record columns (source, type, item) in a filter. A feed page reads records
	// in id order and stops at its limit, so the cheap plan walks the id range;
	// an index on such a column makes the planner read every matching row and
	// sort them (the page then costs the whole table, not the page).
	idWalk bool
}

// sessionIDExpr reads a record's natural session id through its session_ref;
// sessionRefOf is the reverse, from an id placeholder to the session_ref it names.
const (
	sessionIDExpr  = "(SELECT s.session_id FROM sessions s WHERE s.session_ref=r.session_ref)"
	sessionRefOfID = "(SELECT session_ref FROM sessions WHERE session_id=?)"
)

func newBuilder(cat map[string]source.Kind) *builder {
	return &builder{cat: cat, aliases: map[string]string{}, used: map[string]int{}}
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
		if b.sessionRef {
			return sessionIDExpr, kind, nil
		}
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
	if t, ok := b.typedField(stored, kind); ok {
		return t.read, kind, nil
	}
	a := b.alias(stored)
	if d, ok := b.pathFact(stored); ok {
		return "COALESCE(" + a + ".s," + d + ")", kind, nil
	}
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
	if stored, _, rerr := b.resolve(f.Field); rerr == nil {
		b.used[stored]++
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
	// With session_ref an equality or membership test on the session compares
	// the integer reference (records_session_ref_source_time) instead of
	// reading the id of every candidate row.
	byRef := false
	if stored, _, rerr := b.resolve(f.Field); rerr == nil && stored == "session" && b.sessionRef {
		byRef = true
	}
	// A typed field compares through its column (ext) or its interned id
	// (record); a raw value in a fields row (the mismatch fallback) is read by
	// select, group, order and exists but never satisfies a typed comparison.
	var tf typedField
	var isTyped bool
	if stored, _, rerr := b.resolve(f.Field); rerr == nil && !kind.Searchable() {
		tf, isTyped = b.typedField(stored, kind)
	}
	if isTyped && tf.ext {
		e = tf.typed
	}
	if b.idWalk && !byRef {
		if stored, _, rerr := b.resolve(f.Field); rerr == nil && (stored == "source" || stored == "type" || stored == "item") {
			e = "+" + e // unary plus: an expression, so no index can serve it
		}
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
		if byRef {
			return "r.session_ref=" + sessionRefOfID, []any{cv}, nil
		}
		if isTyped && !tf.ext {
			return tf.idCol + "=(SELECT id FROM " + tf.table + " WHERE v=?)", []any{cv}, nil
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
		if byRef {
			return "r.session_ref IN (SELECT session_ref FROM sessions WHERE session_id IN (" + strings.Join(ph, ",") + "))", args, nil
		}
		if isTyped && !tf.ext {
			return tf.idCol + " IN (SELECT id FROM " + tf.table + " WHERE v IN (" + strings.Join(ph, ",") + "))", args, nil
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
		} else if t, ok := b.typedField(stored, kind); ok {
			cond = t.present
		} else if d, ok := b.pathFact(stored); ok {
			cond = "COALESCE(" + b.alias(stored) + ".s," + d + ") IS NOT NULL"
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

// Path facts. A record of a source whose file sits under projects/<project>/
// carries the project and the file's session as facts of the file's path, not
// of the line, so the parser stores no fields row for them and they are read
// back here from paths (the file's own path under the Claude directory, chosen
// the way the parser chose it: the first current path by name, else the oldest
// closed one). A stored row, when there is one (the history source's project is
// a JSON value; a database written before these rows were dropped still holds
// them), wins over the path.
const (
	projectsPrefix = "projects/"
	// The suffixes are the file name endings a source strips to get the
	// session of a file directly under the project directory.
	suffixStatus  = ".status.jsonl"
	suffixMode    = ".session-mode.json"
	suffixCompact = ".compaction.json"
	suffixJSONL   = ".jsonl"
)

// pathFact returns the SQL expression that derives the stored field name from
// the record's file path, or false when the field has no path derivation (or no
// Claude directory is known).
func (b *builder) pathFact(stored string) (string, bool) {
	if b.claudeDir == "" || (stored != "project" && stored != "file_session") {
		return "", false
	}
	b.addPathJoin()
	root := len(strings.TrimRight(b.claudeDir, "/")) + 2 // 1-based start of the relative path
	// after is the text following "projects/"; it starts with the project.
	after := fmt.Sprintf("substr(pp.path,%d)", root+len(projectsPrefix))
	in := fmt.Sprintf("substr(pp.path,%d,%d)='%s' AND instr(%s,'/')>1", root, len(projectsPrefix), projectsPrefix, after)
	if stored == "project" {
		return fmt.Sprintf("CASE WHEN %s THEN substr(%s,1,instr(%s,'/')-1) END", in, after, after), true
	}
	// seg is the text starting at the session segment.
	seg := fmt.Sprintf("substr(%s,instr(%s,'/')+1)", after, after)
	base := seg
	for _, suf := range []string{suffixJSONL, suffixCompact, suffixMode, suffixStatus} {
		base = fmt.Sprintf("CASE WHEN substr(%s,-%d)='%s' THEN substr(%s,1,length(%s)-%d) ELSE %s END", seg, len(suf), suf, seg, seg, len(suf), base)
	}
	return fmt.Sprintf("CASE WHEN %s THEN NULLIF(CASE WHEN instr(%s,'/')>0 THEN substr(%s,1,instr(%s,'/')-1) ELSE %s END,'') END",
		in, seg, seg, seg, base), true
}

// addPathJoin joins paths once as pp: the file's path under the Claude
// directory, the first current one by name, else the first closed one.
func (b *builder) addPathJoin() {
	if b.pathJoin {
		return
	}
	b.pathJoin = true
	b.joins = append(b.joins, `LEFT JOIN paths pp ON pp.rowid=(SELECT p.rowid FROM paths p WHERE p.file_id=r.file_id AND substr(p.path,1,?)=? `+
		`ORDER BY p.closed_ns IS NOT NULL, CASE WHEN p.closed_ns IS NULL THEN p.path END, p.first_seen_ns, p.path LIMIT 1)`)
	pre := strings.TrimRight(b.claudeDir, "/") + "/"
	b.joinArgs = append(b.joinArgs, len(pre), pre)
}

// lookupTables maps the records lookup-id columns to their lookup tables.
var lookupTables = map[string]string{
	"cwd_id":        "lk_cwd",
	"branch_id":     "lk_branch",
	"version_id":    "lk_version",
	"entrypoint_id": "lk_entrypoint",
	"user_type_id":  "lk_user_type",
}

// typedField is how a field declared with record: or ext: storage is read in
// the new layout.
type typedField struct {
	// read is the value expression: the typed value, else (the mismatch rule) the
	// raw value of a fields row, so a value that did not convert is never hidden.
	read string
	// typed is the typed value alone (the lookup value or the ext column).
	typed string
	// present is true when the field has a value of either kind.
	present string
	// ext is true for an extension-table column; otherwise the field is an
	// interned record column: idCol is the records column and table its lookup.
	ext   bool
	idCol string
	table string
}

// typedField returns the typed read of the stored field name when this index.db
// is in the new layout and the field declares record: or ext: storage. The
// joins it needs are added once per field (the lookup table or extension table
// on its key, and the generic fields row for the fallback).
func (b *builder) typedField(stored string, kind source.Kind) (typedField, bool) {
	if !b.typed {
		return typedField{}, false
	}
	st, ok := b.storage[stored]
	if !ok {
		return typedField{}, false
	}
	var t typedField
	switch st.Kind {
	case source.StoreRecord:
		tbl, ok := lookupTables[st.Column]
		if !ok {
			return typedField{}, false
		}
		a := b.joinOnce("lk:"+tbl, "LEFT JOIN "+tbl+" %s ON %[1]s.id=r."+st.Column)
		t = typedField{typed: a + ".v", idCol: "r." + st.Column, table: tbl}
	case source.StoreExt:
		et, ok := store.LookupExt(st.Table)
		if !ok {
			return typedField{}, false
		}
		if _, ok := et.Column(st.Column); !ok {
			return typedField{}, false
		}
		a := b.joinOnce("ext:"+et.Table(), "LEFT JOIN "+et.Table()+" %s ON %[1]s.rec_id=r.id")
		t = typedField{typed: a + "." + st.Column, ext: true}
	default:
		return typedField{}, false
	}
	f := b.alias(stored)
	var raw string
	switch kind {
	case source.KindInt, source.KindBool, source.KindTime:
		raw = f + ".n," + f + ".s"
	case source.KindFloat:
		raw = f + ".f," + f + ".s"
	default:
		raw = f + ".s"
	}
	t.read = "COALESCE(" + t.typed + "," + raw + ")"
	t.present = "(" + t.typed + " IS NOT NULL OR " + f + ".rec_id IS NOT NULL)"
	return t, true
}

// joinOnce adds the join (format has one %s for the alias) once per key and
// returns its alias. These joins take no arguments, so they never disturb
// joinArgs.
func (b *builder) joinOnce(key, format string) string {
	if a, ok := b.aliases[key]; ok {
		return a
	}
	a := fmt.Sprintf("f%d", len(b.aliases))
	b.aliases[key] = a
	b.joins = append(b.joins, fmt.Sprintf(format, a))
	return a
}
