package parser

import (
	"context"
	"database/sql"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

const (
	blobSource    = "claude-transcript"
	blobType      = "api-request-blob"
	blobHashField = "blob_hash"
	// BlobPruneSlice is how many child records one PruneBlobChildren call removes
	// at most, so the write lock is held for a bounded time.
	BlobPruneSlice = 2000
)

// blobChildrenQuery lists child records (item >= 0) of stored api-request-blob
// lines: those written before a blob line became one opaque row. The line
// records drive the join through the (source, type) index and their children
// are found by the records_ident prefix, so a slice never scans the table.
const blobChildrenQuery = `SELECT r.id, r.session_ref FROM records l CROSS JOIN records r
ON r.file_id=l.file_id AND r.gen=l.gen AND r.off=l.off
WHERE l.source=?1 AND l.type=?2 AND l.item=-1 AND r.item>=0 LIMIT ?3`

// PruneBlobChildren removes at most limit stored child records of
// api-request-blob lines in one short write transaction and reports how many it
// removed; zero means none are left. The blob line records stay, and so does
// the copied payload they point at, so a blob can still be replayed. Call it
// until it returns zero (the daemon spaces the calls out; a test loops).
func (p *Parser) PruneBlobChildren(ctx context.Context, limit int) (removed int, err error) {
	if limit <= 0 {
		limit = BlobPruneSlice
	}
	st := p.cfg.Store
	err = st.WithWriteCtx(ctx, "", func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, blobChildrenQuery, blobSource, blobType, limit)
		if err != nil {
			return err
		}
		var ids []any
		refs := map[int64]bool{}
		for rows.Next() {
			var id, ref int64
			if err := rows.Scan(&id, &ref); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
			refs[ref] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		if len(ids) == 0 {
			return nil
		}
		st.NoteRowsRemoved()
		in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		wordOpaque, triStmt := ",'opaque'", ""
		if st.HasTrigram() {
			wordOpaque = ""
			triStmt = `DELETE FROM fts_tri WHERE rowid IN (SELECT id FROM texts WHERE kind='opaque' AND rec_id IN ` + in + `)`
		}
		stmts := []string{
			`DELETE FROM fts_word WHERE rowid IN (SELECT id FROM texts WHERE kind IN ('text'` + wordOpaque + `) AND rec_id IN ` + in + `)`,
			`DELETE FROM texts WHERE rec_id IN ` + in,
			`DELETE FROM fields WHERE rec_id IN ` + in,
			`DELETE FROM ask_questions WHERE rec_id IN ` + in,
			`DELETE FROM ask_answers WHERE rec_id IN ` + in,
			`DELETE FROM blob_hashes WHERE rec_id IN ` + in,
		}
		if triStmt != "" {
			stmts = append([]string{triStmt}, stmts...)
		}
		if st.HasExt() {
			for _, t := range store.ExtTables() {
				stmts = append(stmts, `DELETE FROM `+t.Table()+` WHERE rec_id IN `+in)
			}
		}
		stmts = append(stmts, `DELETE FROM records WHERE id IN `+in)
		for _, s := range stmts {
			if _, err := tx.ExecContext(ctx, s, ids...); err != nil {
				return err
			}
		}
		for ref := range refs {
			if _, err := tx.ExecContext(ctx, `UPDATE session_stats SET
  last_time_ns=COALESCE((SELECT MAX(time_ns) FROM records WHERE session_ref=?1),0),
  record_count=(SELECT COUNT(*) FROM records WHERE session_ref=?1) WHERE session_ref=?1`, ref); err != nil {
				return err
			}
		}
		removed = len(ids)
		return nil
	})
	return removed, err
}
