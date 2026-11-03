package query

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
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
func Head(tx *sql.Tx) (epoch, head, maxRec int64) {
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
}

// Selected is one record read for the feed.
type Selected struct {
	ID     int64
	Ref    RecordRef
	Fields map[string]any
}

// LimitOf applies the default (1,000) and maximum (10,000) page size.
func LimitOf(n int) int { return limitOf(n) }

// Select reads up to Limit+1 records in id order (the caller trims and sets
// more).
func (e *Engine) Select(tx *sql.Tx, sel Selection) ([]Selected, *wire.Error) {
	b := newBuilder(e.Catalog)
	names := sel.Project
	if len(names) == 0 {
		names = []string{"type", "session", "time"}
	}
	cols := make([]colSpec, len(names))
	for i, n := range names {
		c, err := b.col(n)
		if err != nil {
			return nil, err
		}
		cols[i] = c
	}
	wsql, wargs, err := b.where(sel.Where)
	if err != nil {
		return nil, err
	}
	selects := []string{"r.id", "r.file_id", "r.gen", "r.off", "r.item"}
	for _, c := range cols {
		selects = append(selects, c.expr)
	}
	q := "SELECT " + strings.Join(selects, ",") + base(!sel.CurGenOnly) + " " + strings.Join(b.joins, " ") +
		" WHERE r.id>? AND r.id<=? AND (" + wsql + ") ORDER BY r.id LIMIT ?"
	// Statement text order: the field joins, the id bounds, the filter, the limit.
	args := append(append([]any{}, b.joinArgs...), sel.AfterID, sel.UpToID)
	args = append(append(args, wargs...), limitOf(sel.Limit)+1)
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
