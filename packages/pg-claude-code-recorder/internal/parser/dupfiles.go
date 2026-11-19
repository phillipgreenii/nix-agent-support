package parser

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// Duplicate append-file rows.
//
// Before the device-change fix, every reboot gave an append file a new files
// row (same inode, birth time and path, a new device number) that re-cloned and
// re-parsed the whole file. Each row owns a full copy of the file's records at
// its current generation, and the query layer joins records to files on
// r.gen=files.cur_gen with no state filter, so a conversation, feed or search
// query returned every record once per row. RetireDuplicateAppendRows repairs
// the rows that are already in an index.db.

// DupSkip is a duplicate row the repair left alone, and why.
type DupSkip struct {
	File   int64  `json:"file"`
	Keeper int64  `json:"keeper"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// DupRepairReport is what one RetireDuplicateAppendRows pass did (or, with
// DryRun, would do). A second pass over a repaired database reports Retired 0
// and Repointed 0.
type DupRepairReport struct {
	DryRun bool `json:"dry_run"`
	// Groups is the number of (inode, birth time, path) identities held by more
	// than one row across devices.
	Groups int `json:"groups"`
	// Retired is the number of loser rows whose duplicate records were removed
	// or that were marked deleted in this pass.
	Retired int `json:"retired"`
	// AlreadyRetired is the number of loser rows that were already deleted and
	// hold no records at their current generation.
	AlreadyRetired int `json:"already_retired"`
	// Repointed is the number of records a loser held that the keeper did not
	// (for example the one-row-per-hash api-request-blob records) and that were
	// moved to the keeper instead of being deleted.
	Repointed int64 `json:"repointed"`
	// Cleared is the number of loser records removed because the keeper holds
	// the same (offset, item) of the same generation.
	Cleared int64     `json:"cleared"`
	Skipped []DupSkip `json:"skipped,omitempty"`
}

type dupRow struct {
	id          int64
	dev         int64
	state       string
	curGen      int
	openPath    bool
	copyOffset  int64
	parseOffset int64
	tailHash    int64
	tailLen     int64
	records     int64
}

// RetireDuplicateAppendRows collapses every group of append-file rows that
// share (inode, birth time, path) across different devices onto one keeper row.
//
// The keeper is the row the recorder keeps appending to and re-stamps on its
// next sighting: not deleted first, then the row holding the path open, then
// the larger copy at its current generation, then the lower id. A keeper that
// holds the open path matters: the copier's device-change handling finds a row
// by its open path, so retiring that row instead would make the next sweep
// create a third row.
//
// A loser (another device, same inode and birth time) is retired only when its
// current generation equals the keeper's, the keeper's parse covers the loser's
// whole copy, and the tail hashes agree when the copies are the same length.
// Every loser record the keeper lacks (matched on offset and item) is moved to
// the keeper, not deleted; the records both rows hold are the same bytes parsed
// twice, and the loser's copy is deleted from the index. The loser is then
// tombstoned in the journal (state deleted, paths closed), so it is never
// continued or revived. Generations, copies, archive ranges and older
// generations' records of the loser are left untouched.
//
// Each group is one transaction, and the pass is safe to repeat: a retired loser
// is deleted and holds no records, so a repeat does nothing. A crash between
// the transaction and the journal record leaves a loser with no records that
// the next pass tombstones.
//
// The caller MUST hold the store exclusively (the recorder daemon stopped):
// the pass rewrites derived tables the daemon's parser also writes. dryRun
// reports what would change without writing.
func (p *Parser) RetireDuplicateAppendRows(dryRun bool) (DupRepairReport, error) {
	p.run.Lock()
	defer p.run.Unlock()
	ctx := context.Background()
	rep := DupRepairReport{DryRun: dryRun}
	type key struct {
		ino, birth int64
		path       string
	}
	var keys []key
	err := p.cfg.Store.WithWrite("", func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT f.ino, f.birth_ns, p.path FROM files f JOIN paths p ON p.file_id=f.id
WHERE f.mode='append' GROUP BY f.ino, f.birth_ns, p.path
HAVING COUNT(DISTINCT f.id)>1 AND COUNT(DISTINCT f.dev)>1
ORDER BY MIN(f.id)`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.ino, &k.birth, &k.path); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return rows.Err()
	})
	if err != nil {
		return rep, err
	}
	rep.Groups = len(keys)
	for _, k := range keys {
		var tombs []int64
		err := p.cfg.Store.WithWrite("", func(tx *sql.Tx) error {
			var e error
			tombs, e = p.retireGroup(ctx, tx, k.ino, k.birth, k.path, dryRun, &rep)
			return e
		})
		if err != nil {
			return rep, fmt.Errorf("duplicate rows of %s: %w", k.path, err)
		}
		if dryRun {
			continue
		}
		for _, id := range tombs {
			if err := p.cfg.Store.Commit(&store.Rec{T: store.TTomb, At: p.cfg.Clock.Now().UnixNano(), File: id, Path: k.path}); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// retireGroup handles one identity inside tx and returns the loser rows that
// still need a tombstone.
func (p *Parser) retireGroup(ctx context.Context, tx *sql.Tx, ino, birth int64, path string, dryRun bool, rep *DupRepairReport) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT f.id, f.dev, f.state, f.cur_gen,
  EXISTS(SELECT 1 FROM paths q WHERE q.file_id=f.id AND q.path=?3 AND q.closed_ns IS NULL),
  COALESCE(g.copy_offset,0), COALESCE(g.parse_offset,0), COALESCE(g.tail_hash,0), COALESCE(g.tail_len,0),
  (SELECT COUNT(*) FROM records r WHERE r.file_id=f.id AND r.gen=f.cur_gen)
FROM files f LEFT JOIN generations g ON g.file_id=f.id AND g.gen=f.cur_gen
WHERE f.mode='append' AND f.ino=?1 AND f.birth_ns=?2
  AND EXISTS(SELECT 1 FROM paths q WHERE q.file_id=f.id AND q.path=?3)`, ino, birth, path)
	if err != nil {
		return nil, err
	}
	var grp []dupRow
	for rows.Next() {
		var r dupRow
		if err := rows.Scan(&r.id, &r.dev, &r.state, &r.curGen, &r.openPath, &r.copyOffset, &r.parseOffset, &r.tailHash, &r.tailLen, &r.records); err != nil {
			_ = rows.Close()
			return nil, err
		}
		grp = append(grp, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(grp) < 2 {
		return nil, nil
	}
	sort.SliceStable(grp, func(i, j int) bool {
		a, b := grp[i], grp[j]
		if (a.state != "deleted") != (b.state != "deleted") {
			return a.state != "deleted"
		}
		if a.openPath != b.openPath {
			return a.openPath
		}
		if a.copyOffset != b.copyOffset {
			return a.copyOffset > b.copyOffset
		}
		return a.id < b.id
	})
	keeper := grp[0]
	var tombs []int64
	for _, l := range grp[1:] {
		if l.dev == keeper.dev {
			continue // not the reboot duplicate; out of scope
		}
		skip := func(why string) {
			rep.Skipped = append(rep.Skipped, DupSkip{File: l.id, Keeper: keeper.id, Path: path, Reason: why})
		}
		if l.state == "deleted" && l.records == 0 {
			rep.AlreadyRetired++
			continue
		}
		switch {
		case keeper.state == "deleted":
			skip("every row of the identity is deleted")
			continue
		case l.curGen != keeper.curGen:
			skip(fmt.Sprintf("current generation differs (%d, keeper %d)", l.curGen, keeper.curGen))
			continue
		case keeper.parseOffset < l.copyOffset:
			skip("keeper's parse does not cover the loser's copy")
			continue
		case keeper.copyOffset == l.copyOffset && keeper.tailLen == l.tailLen && keeper.tailHash != l.tailHash:
			skip("same copy length but different tail hash")
			continue
		}
		rep.Retired++
		if l.state != "deleted" {
			tombs = append(tombs, l.id)
		}
		if l.records == 0 {
			continue
		}
		const orphan = `r.file_id=?1 AND r.gen=?2 AND NOT EXISTS(SELECT 1 FROM records k WHERE k.file_id=?3 AND k.gen=?2 AND k.off=r.off AND k.item=r.item)`
		var orphans int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records r WHERE `+orphan, l.id, l.curGen, keeper.id).Scan(&orphans); err != nil {
			return nil, err
		}
		rep.Repointed += orphans
		rep.Cleared += l.records - orphans
		if dryRun {
			continue
		}
		if orphans > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_files(session_ref,file_id) SELECT DISTINCT r.session_ref, ?3 FROM records r WHERE `+orphan, l.id, l.curGen, keeper.id); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE records AS r SET file_id=?3 WHERE `+orphan, l.id, l.curGen, keeper.id); err != nil {
				return nil, err
			}
		}
		ps := newPS(tx)
		err := p.clearGen(ps, l.id, l.curGen) //nolint:contextcheck // the parser's generation clear takes no context; it runs inside this transaction
		ps.Close()
		if err != nil {
			return nil, err
		}
		ps = newPS(tx)
		err = recomputeFileFact(ps, keeper.id) //nolint:contextcheck // same context-free parser helper, inside this transaction
		ps.Close()
		if err != nil {
			return nil, err
		}
	}
	if dryRun {
		return nil, nil
	}
	return tombs, nil
}
