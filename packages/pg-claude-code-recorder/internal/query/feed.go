package query

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// The feed's sequence space. A record's sequence is twice its id; a reset
// entry (a parse_resets row) sorts just before the first record id its
// re-parse or new generation inserted, at twice next_rec minus one. A cursor is
// "<epoch>:<sequence>", opaque to clients and totally ordered within an epoch.

// RecSeq is the sequence of record id.
func RecSeq(id int64) int64 { return 2 * id }

// ResetSeq is the sequence of a reset whose first following record id is next.
func ResetSeq(next int64) int64 { return 2*next - 1 }

// Cursor formats a cursor.
func Cursor(epoch, seq int64) string { return fmt.Sprintf("%d:%d", epoch, seq) }

// ParseCursor parses "<epoch>:<sequence>".
func ParseCursor(s string) (epoch, seq int64, ok bool) {
	e, q, found := strings.Cut(s, ":")
	if !found {
		return 0, 0, false
	}
	var err1, err2 error
	epoch, err1 = strconv.ParseInt(e, 10, 64)
	seq, err2 = strconv.ParseInt(q, 10, 64)
	return epoch, seq, err1 == nil && err2 == nil && epoch >= 0 && seq >= 0
}

// Head reads, in one transaction, the epoch, the head sequence (the greatest
// sequence committed) and the greatest record id.
func Head(tx store.Querier) (epoch, head, maxRec int64) {
	var s string
	if tx.QueryRow(`SELECT value FROM meta WHERE key='epoch'`).Scan(&s) == nil {
		epoch, _ = strconv.ParseInt(s, 10, 64)
	}
	_ = tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='records'),0)`).Scan(&maxRec)
	head = RecSeq(maxRec)
	var next int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(next_rec),0) FROM parse_resets`).Scan(&next)
	if next > 0 && ResetSeq(next) > head {
		head = ResetSeq(next)
	}
	return epoch, head, maxRec
}

// Selection is a bounded read of records by id for the feed and the snapshot.
type Selection struct {
	Where   *Filter
	Project []string
	// AfterID and UpToID bound the record ids (exclusive, inclusive).
	AfterID, UpToID int64
	Limit           int
	// CurGenOnly keeps only records of each file's current generation (the
	// snapshot); the feed delivers every generation it committed.
	CurGenOnly bool
	// sourceIdx is set by Select when the filter pins one source that is rare in
	// the id range, so the source index serves the page.
	sourceIdx bool
	// typePin, when set, adds r.type=? so the (source, type) index delivers the
	// page in id order with no sort (see Engine.selectByType).
	typePin *string
	// srcPin is the pinned source, set with typePin: the statement then names
	// both columns and reads records through records_source_type.
	srcPin string
}

// Selected is one record read for the feed.
type Selected struct {
	ID     int64
	Ref    RecordRef
	Fields map[string]any
}

// LimitOf applies the default (1,000) and maximum (10,000) page size.
func LimitOf(n int) int { return limitOf(n) }

// selectQuery compiles a Selection into its statement, arguments, field names
// and columns.
func (e *Engine) selectQuery(sel Selection) (string, []any, []string, []colSpec, *wire.Error) {
	b := e.builder()
	b.idWalk = true
	b.sourceIdx = sel.sourceIdx || sel.typePin != nil
	names := sel.Project
	if len(names) == 0 {
		names = []string{"type", "session", "time"}
	}
	cols := make([]colSpec, len(names))
	for i, n := range names {
		c, err := b.col(n)
		if err != nil {
			return "", nil, nil, nil, err
		}
		cols[i] = c
	}
	wsql, wargs, err := b.where(sel.Where)
	if err != nil {
		return "", nil, nil, nil, err
	}
	e.noteFilter("feed", b)
	selects := []string{"r.id", "r.file_id", "r.gen", "r.off", "r.item"}
	for _, c := range cols {
		selects = append(selects, c.expr)
	}
	from := base(!sel.CurGenOnly)
	if sel.typePin != nil {
		from = strings.Replace(from, "FROM records r", "FROM records r INDEXED BY records_source_type", 1)
	}
	q := "SELECT " + strings.Join(selects, ",") + from + " " + strings.Join(b.joins, " ") +
		" WHERE r.id>? AND r.id<=? AND (" + wsql + ")"
	// Statement text order: the field joins, the id bounds, the filter, the type pin, the limit.
	args := append(append([]any{}, b.joinArgs...), sel.AfterID, sel.UpToID)
	args = append(args, wargs...)
	if sel.typePin != nil {
		q += " AND r.source=? AND r.type=?"
		args = append(args, sel.srcPin, *sel.typePin)
	}
	q += " ORDER BY r.id LIMIT ?"
	args = append(args, limitOf(sel.Limit)+1)
	return q, args, names, cols, nil
}

// Select reads up to Limit+1 records in id order (the caller trims and sets
// more). The filter's source, type and item tests never pick an index, so the
// statement walks the id range and stops at its limit instead of reading every
// matching row of the table and sorting them.
func (e *Engine) Select(tx store.Querier, sel Selection) ([]Selected, *wire.Error) {
	if src, ok := pinnedSource(sel.Where); ok {
		if e.rareSource(tx, src, sel) {
			sel.sourceIdx = true
		} else if types, ok := e.sourceTypes(tx, src); ok {
			return e.selectByType(tx, sel, src, types)
		}
	}
	return e.selectRows(tx, sel)
}

// sourceTypesMax is the most distinct record types of a pinned source for which
// selectByType merges one id-ordered read per type.
const sourceTypesMax = 32

// sourceTypes lists the record types of src with one index seek per type (a
// loose scan of records_source_type, so its cost does not grow with the rows of
// the source). It reports false when the source has too many types or a seek
// fails.
func (e *Engine) sourceTypes(tx store.Querier, src string) ([]string, bool) {
	var types []string
	last := ""
	for len(types) <= sourceTypesMax {
		var t string
		err := tx.QueryRow(`SELECT type FROM records INDEXED BY records_source_type WHERE source=? AND type>? ORDER BY type LIMIT 1`, src, last).Scan(&t)
		if errors.Is(err, sql.ErrNoRows) {
			return types, len(types) > 0
		}
		if err != nil {
			return nil, false
		}
		types = append(types, t)
		last = t
	}
	return nil, false
}

// selectByType reads a page of a source that is too large for the rare-source
// sort: for each record type of the source, the (source, type) index delivers
// that type's rows in id order, so the first Limit+1 of each are read without
// sorting or walking the id range, and the merged rows are cut to Limit+1. The
// cost is bounded by the types times the page, not by the rows of the source.
func (e *Engine) selectByType(tx store.Querier, sel Selection, src string, types []string) ([]Selected, *wire.Error) {
	var out []Selected
	for i := range types {
		s := sel
		s.sourceIdx = true
		s.typePin, s.srcPin = &types[i], src
		rows, werr := e.selectRows(tx, s)
		if werr != nil {
			return nil, werr
		}
		out = append(out, rows...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if n := limitOf(sel.Limit) + 1; len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// selectRows runs the page statement of sel.
func (e *Engine) selectRows(tx store.Querier, sel Selection) ([]Selected, *wire.Error) {
	q, args, names, cols, werr := e.selectQuery(sel)
	if werr != nil {
		return nil, werr
	}
	rs, qerr := tx.Query(q, args...)
	if qerr != nil {
		return nil, wire.FromErr(qerr)
	}
	defer func() { _ = rs.Close() }()
	var out []Selected
	for rs.Next() {
		var h Selected
		var fid int64
		var item int
		dest := []any{&h.ID, &fid, &h.Ref.Gen, &h.Ref.Offset, &item}
		vals := make([]any, len(cols))
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rs.Scan(dest...); err != nil {
			return nil, wire.FromErr(err)
		}
		h.Ref.File, h.Ref.Item = FileRef(fid), item
		h.Fields = make(map[string]any, len(cols))
		for i, c := range cols {
			h.Fields[names[i]] = present(c.kind, vals[i])
		}
		out = append(out, h)
	}
	return out, nil
}

// rareSourceMax is the most records of a pinned source in the id range for
// which the page is read through the source index and sorted by id.
var rareSourceMax = 20000

// pinnedSource returns the source a filter requires of every match: a source
// equality leaf, or an all holding one.
func pinnedSource(f *Filter) (string, bool) {
	if f == nil {
		return "", false
	}
	if f.Field == "source" && f.Eq != nil {
		var v string
		if jsonString(f.Eq, &v) {
			return v, true
		}
		return "", false
	}
	for i := range f.All {
		if v, ok := pinnedSource(&f.All[i]); ok {
			return v, true
		}
	}
	return "", false
}

// rareSource reports whether at most rareSourceMax records of src lie in the
// selection's id range. The id walk of a feed page reads every record of the
// range to find the few of a rare source (an empty-cursor feed of the session
// registry read the whole retained table, about 20 s); the source index reads
// just those rows. The probe stops at the threshold, so it costs at most that
// many index entries whatever the source's size. An error answers false (walk).
func (e *Engine) rareSource(tx store.Querier, src string, sel Selection) bool {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM records INDEXED BY records_source_type WHERE source=? AND id>? AND id<=? LIMIT ?)`,
		src, sel.AfterID, sel.UpToID, rareSourceMax+1).Scan(&n)
	return err == nil && n <= rareSourceMax
}

// CouldMatch reports whether a filter could match some record of a file whose
// source is src and whose record types are types (empty: unknown). It answers
// from the source and type leaves only and is true wherever it cannot tell, so a
// reset reaches every feed that could hold a record of the file.
func CouldMatch(f *Filter, src string, types map[string]bool) bool {
	if f == nil {
		return true
	}
	switch {
	case len(f.All) > 0:
		for i := range f.All {
			if !CouldMatch(&f.All[i], src, types) {
				return false
			}
		}
		return true
	case len(f.Any) > 0:
		for i := range f.Any {
			if CouldMatch(&f.Any[i], src, types) {
				return true
			}
		}
		return false
	case f.Not != nil:
		return true
	}
	var want []string
	switch {
	case f.Eq != nil:
		var v string
		if jsonString(f.Eq, &v) {
			want = []string{v}
		} else {
			return true
		}
	case f.In != nil:
		for _, v := range f.In {
			if s, ok := v.(string); ok {
				want = append(want, s)
			} else {
				return true
			}
		}
	default:
		return true
	}
	switch f.Field {
	case "source":
		return contains(want, src)
	case "type":
		if len(types) == 0 {
			return true
		}
		for _, w := range want {
			if types[w] {
				return true
			}
		}
		return false
	}
	return true
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func jsonString(raw []byte, v *string) bool {
	return len(raw) > 1 && raw[0] == '"' && json.Unmarshal(raw, v) == nil
}

// ParseFileRef parses a file id as answers show it (f_12).
func ParseFileRef(s string) (int64, bool) {
	if !strings.HasPrefix(s, "f_") {
		return 0, false
	}
	n, err := strconv.ParseInt(s[2:], 10, 64)
	return n, err == nil && n > 0
}
