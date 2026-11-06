package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudetranscript"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Resolver reads values back from the copies (the database holds no file
// contents).
type Resolver interface {
	Value(file int64, gen int, off int64, ln int, srcName string, item int, field string) (string, error)
	Line(file int64, gen int, off int64, ln int) ([]byte, error)
}

// Engine answers query and search over one database.
type Engine struct {
	DB      *sql.DB
	Catalog map[string]source.Kind
	Res     Resolver
	// SubstringBudget is the bytes of copy text one substring search reads
	// before it answers partial; zero means DefaultSubstringBudget.
	SubstringBudget int64
	// Behind reports whether the recorder is catching up (answers are marked
	// degraded then).
	Behind func() bool
	// SessionRef reports whether every record carries a session_ref (the store's
	// SessionRefReady); when true the session is read through the sessions table
	// instead of records.session. Nil means false: a store that predates
	// session_ref keeps the text column as its only source.
	SessionRef func() bool
	// ClaudeDir is the directory the recorded paths live under. When set,
	// project and file_session are read back from the file's path; when empty
	// they are read from stored fields only.
	ClaudeDir string
	// Storage is the record: and ext: declaration of every catalogued field that
	// has one (sources.Storage); with TypedStorage it routes those fields to
	// their typed columns.
	Storage map[string]source.Storage
	// TypedStorage reports whether this index.db is in the new layout, where
	// record: and ext: fields live in their typed columns (the store's HasExt).
	// Nil means false: the fields table is the only source, as in the running
	// layout.
	TypedStorage func() bool
	// FilterFields, when set, is told after each request whose filter compiled
	// which catalogued field names the filter named and how many leaves each
	// had (op is query, search or feed). Names only: never values or text.
	FilterFields func(op string, fields map[string]int)
}

// noteFilter reports the compiled filter's field names to FilterFields.
func (e *Engine) noteFilter(op string, b *builder) {
	if e.FilterFields != nil && len(b.used) > 0 {
		e.FilterFields(op, b.used)
	}
}

// Agg is one aggregate.
type Agg struct {
	Fn       string `json:"fn"`
	Field    string `json:"field,omitempty"`
	Distinct string `json:"distinct,omitempty"`
	As       string `json:"as,omitempty"`
}

// Order is one ordering term.
type Order struct {
	By  string `json:"by"`
	Dir string `json:"dir,omitempty"`
}

// Request is the query operation's body.
type Request struct {
	Op        string   `json:"op,omitempty"`
	From      string   `json:"from,omitempty"`
	Where     *Filter  `json:"where,omitempty"`
	Group     []string `json:"group,omitempty"`
	Aggregate []Agg    `json:"aggregate,omitempty"`
	Select    []string `json:"select,omitempty"`
	Raw       bool     `json:"raw,omitempty"`
	// Follow resolves a link per row: parent (parent_uuid to uuid), result (a
	// tool_use to its tool_result) or call (a tool_result to its tool_use).
	// A link is resolved within the same file first.
	Follow string  `json:"follow,omitempty"`
	Order  []Order `json:"order,omitempty"`
	Limit  int     `json:"limit,omitempty"`
	// OldGenerations includes records of superseded generations.
	OldGenerations bool `json:"old_generations,omitempty"`
}

// RecordRef names a record by its position.
type RecordRef struct {
	File   string `json:"file"`
	Gen    int    `json:"gen"`
	Offset int64  `json:"offset"`
	Item   int    `json:"item"`
}

// Link is a followed record with the same columns as the row.
type Link struct {
	Record RecordRef `json:"record"`
	Row    []any     `json:"row"`
}

// Answer is the query answer.
type Answer struct {
	AsOf     string      `json:"as_of"`
	Columns  []string    `json:"columns"`
	Rows     [][]any     `json:"rows"`
	More     bool        `json:"more"`
	Degraded bool        `json:"degraded,omitempty"`
	Records  []RecordRef `json:"records,omitempty"`
	Links    [][]Link    `json:"links,omitempty"`
	Raw      []string    `json:"raw,omitempty"`
}

const (
	defaultLimit = 1000
	maxLimit     = 10000
)

// FileRef formats a file id the way answers show it.
func FileRef(id int64) string { return "f_" + strconv.FormatInt(id, 10) }

func limitOf(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func (e *Engine) asOf(tx store.Querier) string {
	epoch, head, _ := Head(tx)
	return Cursor(epoch, head)
}

// base joins records with files and applies the current-generation filter.
func base(old bool) string {
	s := " FROM records r JOIN files fl ON fl.id=r.file_id"
	if !old {
		s += " AND r.gen=fl.cur_gen"
	}
	return s
}

type colSpec struct {
	name string
	expr string
	kind source.Kind
}

func (b *builder) col(name string) (colSpec, *wire.Error) {
	e, k, err := b.expr(name)
	if err != nil {
		return colSpec{}, err
	}
	return colSpec{name: name, expr: e, kind: k}, nil
}

// Query runs a query request.
func (e *Engine) Query(ctx context.Context, req Request) (*Answer, *wire.Error) {
	if req.From != "" && req.From != "records" {
		return nil, badf("from %q: only records is available", req.From)
	}
	dbtx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = dbtx.Rollback() }()
	tx := store.BindCtx(ctx, dbtx) // every statement runs under the request ctx
	ans := &Answer{AsOf: e.asOf(tx), Rows: [][]any{}}
	if e.Behind != nil && e.Behind() {
		ans.Degraded = true
	}
	var werr *wire.Error
	if len(req.Group) > 0 || len(req.Aggregate) > 0 {
		werr = e.aggregate(tx, req, ans)
	} else {
		werr = e.rows(tx, req, ans)
	}
	if werr != nil {
		return nil, werr
	}
	return ans, nil
}

func (e *Engine) rows(tx store.Querier, req Request, ans *Answer) *wire.Error {
	b := e.builder()
	names := req.Select
	if len(names) == 0 {
		names = []string{"type", "session", "time"}
	}
	cols := make([]colSpec, len(names))
	for i, n := range names {
		c, err := b.col(n)
		if err != nil {
			return err
		}
		cols[i] = c
	}
	b.idWalk = b.idWalk || pinsSession(req.Where)
	wsql, wargs, err := b.where(req.Where)
	if err != nil {
		return err
	}
	e.noteFilter("query", b)
	var ord []string
	for _, o := range req.Order {
		c, err := b.col(o.By)
		if err != nil {
			return err
		}
		d := "ASC"
		if strings.EqualFold(o.Dir, "desc") {
			d = "DESC"
		}
		ord = append(ord, c.expr+" "+d)
	}
	ord = append(ord, "r.id")
	sel := []string{"r.id", "r.file_id", "r.gen", "r.off", "r.item", "r.len", "r.source"}
	for _, c := range cols {
		sel = append(sel, c.expr)
	}
	q := "SELECT " + strings.Join(sel, ",") + base(req.OldGenerations) + " " + strings.Join(b.joins, " ") +
		" WHERE " + wsql + " ORDER BY " + strings.Join(ord, ",") + " LIMIT ?"
	limit := limitOf(req.Limit)
	args := append(append(append([]any{}, b.joinArgs...), wargs...), limit+1)
	rs, qerr := tx.Query(q, args...)
	if qerr != nil {
		return wire.FromErr(qerr)
	}
	type hit struct {
		id       int64
		ref      RecordRef
		ln       int
		src      string
		row      []any
		file     int64
		rawBytes []byte
	}
	var hits []hit
	for rs.Next() {
		var h hit
		dest := make([]any, 0, 7+len(cols))
		var ref RecordRef
		var fid int64
		var item int
		dest = append(dest, &h.id, &fid, &ref.Gen, &ref.Offset, &item, &h.ln, &h.src)
		vals := make([]any, len(cols))
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rs.Scan(dest...); err != nil {
			_ = rs.Close()
			return wire.FromErr(err)
		}
		ref.File, ref.Item, h.file = FileRef(fid), item, fid
		h.ref = ref
		h.row = make([]any, len(cols))
		for i, c := range cols {
			h.row[i] = present(c.kind, vals[i])
		}
		hits = append(hits, h)
	}
	_ = rs.Close()
	if len(hits) > limit {
		ans.More = true
		hits = hits[:limit]
	}
	ans.Columns = names
	for _, h := range hits {
		ans.Rows = append(ans.Rows, h.row)
		ans.Records = append(ans.Records, h.ref)
		if req.Raw && e.Res != nil {
			raw, _ := e.Res.Line(h.file, h.ref.Gen, h.ref.Offset, h.ln)
			ans.Raw = append(ans.Raw, string(raw))
		}
	}
	if req.Follow != "" {
		ids := make([]int64, len(hits))
		for i, h := range hits {
			ids[i] = h.id
		}
		links, err := e.follow(tx, req, ids, hits0files(hits, func(h hit) int64 { return h.file }), cols)
		if err != nil {
			return err
		}
		ans.Links = links
	}
	return nil
}

func hits0files[T any](h []T, f func(T) int64) []int64 {
	out := make([]int64, len(h))
	for i := range h {
		out[i] = f(h[i])
	}
	return out
}

// present converts a scanned value to its answer form.
func present(kind source.Kind, v any) any {
	if v == nil {
		return nil
	}
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	switch kind {
	case source.KindTime:
		if n, ok := v.(int64); ok {
			return rfc(n)
		}
	case source.KindBool:
		if n, ok := v.(int64); ok {
			return n != 0
		}
	}
	return v
}

type linkSpec struct {
	from, to, fromType, toType string
	// bySession links on the record's session instead of a field; toTypes then
	// lists the target record types.
	bySession bool
	toTypes   []string
	// pathBody links a tool_result to its tool-results body by the saved-to
	// path in its text when no tool_use_id matches (and the reverse).
	pathBody bool
}

func linkOf(follow string) (linkSpec, bool) {
	switch follow {
	case "parent":
		return linkSpec{from: "parent_uuid", to: "uuid"}, true
	case "result":
		return linkSpec{from: "tool_use_id", to: "tool_use_id", fromType: "tool_use", toType: "tool_result", pathBody: true}, true
	case "call":
		return linkSpec{from: "tool_use_id", to: "tool_use_id", fromType: "tool_result", toType: "tool_use"}, true
	case "body":
		return linkSpec{from: "tool_use_id", to: "tool_use_id", fromType: "tool_result", toType: "tool_result_body", pathBody: true}, true
	case "subagents":
		return linkSpec{bySession: true, toTypes: []string{"subagent_meta"}}, true
	case "status":
		return linkSpec{bySession: true, toTypes: []string{"status"}}, true
	case "mode":
		return linkSpec{bySession: true, toTypes: []string{"session_mode"}}, true
	case "registry":
		return linkSpec{bySession: true, toTypes: []string{"registry", "registry_deleted"}}, true
	case "compaction":
		return linkSpec{bySession: true, toTypes: []string{"compaction"}}, true
	}
	return linkSpec{}, false
}

type tgt struct {
	id   int64
	file int64
	ref  RecordRef
}

func (e *Engine) scanTargets(tx store.Querier, q string, args []any) ([]tgt, *wire.Error) {
	rs, err := tx.Query(q, args...)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = rs.Close() }()
	var out []tgt
	for rs.Next() {
		var t tgt
		var item int
		var fid int64
		if err := rs.Scan(&t.id, &fid, &t.ref.Gen, &t.ref.Offset, &item); err != nil {
			return nil, wire.FromErr(err)
		}
		t.file, t.ref.File, t.ref.Item = fid, FileRef(fid), item
		out = append(out, t)
	}
	return out, nil
}

const targetHead = `SELECT r.id,r.file_id,r.gen,r.off,r.item FROM records r JOIN files fl ON fl.id=r.file_id AND r.gen=fl.cur_gen`

// linkTargets returns the records record id links to under ls (before the
// same-file preference).
func (e *Engine) linkTargets(tx store.Querier, ls linkSpec, id int64) ([]tgt, *wire.Error) {
	if ls.bySession {
		var session string
		var ref int64
		byRef := e.sessionRefReady()
		if byRef {
			_ = tx.QueryRow(`SELECT COALESCE((SELECT s.session_id FROM sessions s WHERE s.session_ref=r2.session_ref),''), r2.session_ref FROM records r2 WHERE r2.id=?`, id).Scan(&session, &ref)
		} else {
			_ = tx.QueryRow(`SELECT session FROM records WHERE id=?`, id).Scan(&session)
		}
		if session == "" {
			return nil, nil
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ls.toTypes)), ",")
		var args []any
		cond := ` WHERE r.session=?`
		if byRef {
			cond, args = ` WHERE r.session_ref=?`, []any{ref}
		} else {
			args = []any{session}
		}
		for _, t := range ls.toTypes {
			args = append(args, t)
		}
		return e.scanTargets(tx, targetHead+cond+` AND r.type IN (`+ph+`) ORDER BY r.id LIMIT `+strconv.Itoa(defaultLimit), args)
	}
	var key sql.NullString
	_ = tx.QueryRow(`SELECT s FROM fields WHERE rec_id=? AND name_id=(SELECT id FROM field_names WHERE name=?)`, id, ls.from).Scan(&key)
	var out []tgt
	if key.Valid && key.String != "" {
		q := targetHead + ` JOIN fields fu ON fu.rec_id=r.id WHERE fu.name_id=(SELECT id FROM field_names WHERE name=?) AND fu.s=? AND r.id<>?`
		args := []any{ls.to, key.String, id}
		if ls.toType != "" {
			q += ` AND r.type=?`
			args = append(args, ls.toType)
		} else {
			q += ` AND r.item=-1`
		}
		var err *wire.Error
		if out, err = e.scanTargets(tx, q+` ORDER BY r.id`, args); err != nil {
			return nil, err
		}
	}
	if len(out) > 0 || !ls.pathBody {
		return out, nil
	}
	return e.pathTargets(tx, ls, id)
}

// pathTargets links a tool_result to the tool-results body its text names, and a
// body without a tool_use_id back to the tool_result that names its path.
func (e *Engine) pathTargets(tx store.Querier, ls linkSpec, id int64) ([]tgt, *wire.Error) {
	var typ, src string
	var file, off, item, ln int64
	var gen int
	if tx.QueryRow(`SELECT type,source,file_id,gen,off,item,len FROM records WHERE id=?`, id).Scan(&typ, &src, &file, &gen, &off, &item, &ln) != nil {
		return nil, nil
	}
	if typ == "tool_result" && ls.toType == "tool_result_body" && e.Res != nil {
		text, err := e.Res.Value(file, gen, off, int(ln), src, int(item), "tool_result.body")
		if err != nil {
			return nil, nil
		}
		to := claudetranscript.SavedTo(text)
		if to == "" {
			return nil, nil
		}
		return e.scanTargets(tx, targetHead+` JOIN fields fu ON fu.rec_id=r.id WHERE fu.name_id=(SELECT id FROM field_names WHERE name='rel_path') AND fu.s=? AND r.type='tool_result_body' ORDER BY r.id`, []any{to})
	}
	if typ == "tool_result_body" && ls.toType == "tool_result" {
		var rel sql.NullString
		_ = tx.QueryRow(`SELECT s FROM fields WHERE rec_id=? AND name_id=(SELECT id FROM field_names WHERE name='rel_path')`, id).Scan(&rel)
		if !rel.Valid || rel.String == "" {
			return nil, nil
		}
		return e.scanTargets(tx, targetHead+` JOIN fields fu ON fu.rec_id=r.id WHERE fu.name_id=(SELECT id FROM field_names WHERE name='saved_to') AND fu.s=? AND r.type='tool_result' ORDER BY r.id`, []any{rel.String})
	}
	return nil, nil
}

func (e *Engine) follow(tx store.Querier, req Request, ids, files []int64, cols []colSpec) ([][]Link, *wire.Error) {
	ls, ok := linkOf(req.Follow)
	if !ok {
		return nil, badf("follow %q: use parent, result, call, body, subagents, status, mode, registry or compaction", req.Follow)
	}
	out := make([][]Link, len(ids))
	for i, id := range ids {
		out[i] = []Link{}
		all, err := e.linkTargets(tx, ls, id)
		if err != nil {
			return nil, err
		}
		var same []tgt
		for _, t := range all {
			if t.file == files[i] {
				same = append(same, t)
			}
		}
		use := same
		if len(use) == 0 || ls.bySession {
			use = all
		}
		for _, t := range use {
			row := make([]any, len(cols))
			for k, c := range cols {
				row[k] = e.fetch(tx, t.id, c)
			}
			out[i] = append(out[i], Link{Record: t.ref, Row: row})
		}
	}
	return out, nil
}

func (e *Engine) fetch(tx store.Querier, rec int64, c colSpec) any {
	b := e.builder()
	cs, err := b.col(c.name)
	if err != nil {
		return nil
	}
	q := "SELECT " + cs.expr + " FROM records r " + strings.Join(b.joins, " ") + " WHERE r.id=?"
	var v any
	if tx.QueryRow(q, append(b.joinArgs, rec)...).Scan(&v) != nil {
		return nil
	}
	return present(cs.kind, v)
}

type groupAcc struct {
	key    []any
	counts []int64
	sums   []float64
	isum   []int64
	mins   []any
	maxs   []any
	n      []int64
	seen   []map[string]bool
}

func (e *Engine) aggregate(tx store.Querier, req Request, ans *Answer) *wire.Error {
	b := e.builder()
	var sel []string
	idx := func(name string) (int, colSpec, *wire.Error) {
		c, err := b.col(name)
		if err != nil {
			return 0, c, err
		}
		sel = append(sel, c.expr)
		return len(sel) - 1, c, nil
	}
	gIdx := make([]int, len(req.Group))
	gCol := make([]colSpec, len(req.Group))
	for i, g := range req.Group {
		k, c, err := idx(g)
		if err != nil {
			return err
		}
		gIdx[i], gCol[i] = k, c
	}
	type aggPlan struct {
		fn, as     string
		fIdx, dIdx int
		fCol       colSpec
		hasF, hasD bool
	}
	plans := make([]aggPlan, len(req.Aggregate))
	for i, a := range req.Aggregate {
		p := aggPlan{fn: strings.ToLower(a.Fn), as: a.As}
		switch p.fn {
		case "count", "sum", "min", "max", "avg":
		default:
			return badf("aggregate fn %q: use count, sum, min, max or avg", a.Fn)
		}
		if a.Field != "" {
			k, c, err := idx(a.Field)
			if err != nil {
				return err
			}
			p.fIdx, p.fCol, p.hasF = k, c, true
		} else if p.fn != "count" {
			return badf("aggregate %s needs a field", p.fn)
		}
		if a.Distinct != "" {
			k, _, err := idx(a.Distinct)
			if err != nil {
				return err
			}
			p.dIdx, p.hasD = k, true
		}
		if p.as == "" {
			p.as = p.fn
			if a.Field != "" {
				p.as += "_" + strings.ReplaceAll(a.Field, ".", "_")
			}
		}
		plans[i] = p
	}
	wsql, wargs, err := b.where(req.Where)
	if err != nil {
		return err
	}
	e.noteFilter("query", b)
	if len(sel) == 0 {
		sel = []string{"r.id"}
	}
	q := "SELECT " + strings.Join(sel, ",") + base(req.OldGenerations) + " " + strings.Join(b.joins, " ") + " WHERE " + wsql
	rs, qerr := tx.Query(q, append(append([]any{}, b.joinArgs...), wargs...)...)
	if qerr != nil {
		return wire.FromErr(qerr)
	}
	groups := map[string]*groupAcc{}
	var order []string
	for rs.Next() {
		vals := make([]any, len(sel))
		dest := make([]any, len(sel))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rs.Scan(dest...); err != nil {
			_ = rs.Close()
			return wire.FromErr(err)
		}
		var kb strings.Builder
		for _, k := range gIdx {
			fmt.Fprintf(&kb, "%v\x00", vals[k])
		}
		key := kb.String()
		g := groups[key]
		if g == nil {
			g = &groupAcc{
				counts: make([]int64, len(plans)), sums: make([]float64, len(plans)), isum: make([]int64, len(plans)),
				mins: make([]any, len(plans)), maxs: make([]any, len(plans)), n: make([]int64, len(plans)), seen: make([]map[string]bool, len(plans)),
			}
			for i, c := range gCol {
				g.key = append(g.key, present(c.kind, vals[gIdx[i]]))
			}
			groups[key] = g
			order = append(order, key)
		}
		for i, p := range plans {
			if p.hasD && vals[p.dIdx] != nil {
				dk := fmt.Sprint(vals[p.dIdx])
				if g.seen[i] == nil {
					g.seen[i] = map[string]bool{}
				}
				if g.seen[i][dk] {
					continue
				}
				g.seen[i][dk] = true
			}
			if !p.hasF {
				g.counts[i]++
				continue
			}
			v := vals[p.fIdx]
			if v == nil {
				continue
			}
			g.counts[i]++
			switch x := v.(type) {
			case int64:
				g.isum[i] += x
				g.sums[i] += float64(x)
			case float64:
				g.sums[i] += x
			}
			g.n[i]++
			if g.mins[i] == nil || less(v, g.mins[i]) {
				g.mins[i] = v
			}
			if g.maxs[i] == nil || less(g.maxs[i], v) {
				g.maxs[i] = v
			}
		}
	}
	_ = rs.Close()
	ans.Columns = append([]string{}, req.Group...)
	for _, p := range plans {
		ans.Columns = append(ans.Columns, p.as)
	}
	if len(req.Group) == 0 && len(groups) == 0 {
		g := &groupAcc{
			counts: make([]int64, len(plans)), sums: make([]float64, len(plans)), isum: make([]int64, len(plans)),
			mins: make([]any, len(plans)), maxs: make([]any, len(plans)), n: make([]int64, len(plans)), seen: make([]map[string]bool, len(plans)),
		}
		groups[""], order = g, []string{""}
	}
	var rows [][]any
	for _, k := range order {
		g := groups[k]
		row := append([]any{}, g.key...)
		for i, p := range plans {
			row = append(row, result(p.fn, p.fCol, g, i))
		}
		rows = append(rows, row)
	}
	colIdx := map[string]int{}
	for i, c := range ans.Columns {
		colIdx[c] = i
	}
	for _, o := range req.Order {
		if _, ok := colIdx[o.By]; !ok {
			return badf("order by %q: not a column of this answer", o.By)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, o := range req.Order {
			c := colIdx[o.By]
			a, bb := rows[i][c], rows[j][c]
			if fmt.Sprint(a) == fmt.Sprint(bb) {
				continue
			}
			lt := less(a, bb)
			if strings.EqualFold(o.Dir, "desc") {
				return !lt
			}
			return lt
		}
		return false
	})
	limit := limitOf(req.Limit)
	if len(rows) > limit {
		ans.More = true
		rows = rows[:limit]
	}
	if rows == nil {
		rows = [][]any{}
	}
	ans.Rows = rows
	return nil
}

func result(fn string, fc colSpec, g *groupAcc, i int) any {
	switch fn {
	case "count":
		return g.counts[i]
	case "sum":
		if fc.kind == source.KindFloat {
			return g.sums[i]
		}
		return g.isum[i]
	case "avg":
		if g.n[i] == 0 {
			return nil
		}
		return g.sums[i] / float64(g.n[i])
	case "min":
		return present(fc.kind, g.mins[i])
	case "max":
		return present(fc.kind, g.maxs[i])
	}
	return nil
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

func less(a, b any) bool {
	if a == nil {
		return b != nil
	}
	if b == nil {
		return false
	}
	if x, ok := num(a); ok {
		if y, ok := num(b); ok {
			return x < y
		}
	}
	return fmt.Sprint(a) < fmt.Sprint(b)
}

// Decode parses a request body.
func Decode(body []byte, v any) *wire.Error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return badf("request: %v", err)
	}
	return nil
}

// builder returns a filter builder bound to the engine's catalog and Claude
// directory.
func (e *Engine) builder() *builder {
	b := newBuilder(e.Catalog)
	b.claudeDir = e.ClaudeDir
	b.sessionRef = e.sessionRefReady()
	b.typed = e.TypedStorage != nil && e.TypedStorage()
	b.storage = e.Storage
	return b
}

func (e *Engine) sessionRefReady() bool { return e.SessionRef != nil && e.SessionRef() }

// sessionCol is the SQL expression of a record's session id for a hit.
func (e *Engine) sessionCol() string {
	if e.sessionRefReady() {
		return sessionIDExpr
	}
	return "r.session"
}
