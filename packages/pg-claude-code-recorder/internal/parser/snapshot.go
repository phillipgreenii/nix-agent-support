package parser

import (
	"database/sql"
	"io"
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// mark records, in the transaction that commits records, the highest record id
// together with the commit time. The feed's retention floor is read from these
// marks (a cursor older than feed.retention of commit time is expired). Marks
// older than the retention, but the newest of them, are pruned.
func (p *Parser) mark(tx *pstx) error {
	now := p.cfg.Clock.Now().UnixNano()
	var maxID int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='records'),0)`).Scan(&maxID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO feed_marks(rec_id,at_ns) VALUES(?,?)`, maxID, now); err != nil {
		return err
	}
	cut := now - p.cfg.FeedRetention.Nanoseconds()
	_, err := tx.Exec(`DELETE FROM feed_marks WHERE at_ns < ?1 AND rec_id < (SELECT MAX(rec_id) FROM feed_marks WHERE at_ns < ?1)`, cut)
	return err
}

// genReset records the reset entry of a new generation (gen above 1) in the
// transaction that commits its first records, so a feed delivers "replace every
// record of this file and generation" ahead of them.
func (p *Parser) genReset(tx *pstx, f store.File, src source.Source, g store.Generation, start int64) error {
	if start != 0 || g.Gen <= 1 {
		return nil
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM parse_resets WHERE file_id=? AND gen=?`, f.ID, g.Gen).Scan(&n); err != nil || n > 0 {
		return err
	}
	return p.insertReset(tx, f, src, g.Gen)
}

func (p *Parser) insertReset(tx *pstx, f store.File, src source.Source, gen int) error {
	var next int64
	_ = tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='records'),0)+1`).Scan(&next)
	_, err := tx.Exec(`INSERT INTO parse_resets(file_id,gen,parser,at_ns,next_rec) VALUES(?,?,?,?,?)`,
		f.ID, gen, src.Name()+"@"+strconv.Itoa(src.Version()), p.cfg.Clock.Now().UnixNano(), next)
	return err
}

// parseSnapshots parses every stored version of a snapshot file that has no
// records yet (or that an older parser version produced, when reparse is true
// or the version differs) and the deletion records of its tombstones.
func (p *Parser) parseSnapshots(f store.File, src source.Source, reparse bool) (int, error) {
	rel, ok := p.relPath(f.ID)
	if !ok {
		return 0, nil
	}
	caps, ok := src.Match(rel)
	if !ok {
		return 0, nil
	}
	snaps, err := p.cfg.Store.Snapshots(f.ID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, sn := range snaps {
		var v int
		err := p.cfg.Store.Read().QueryRow(`SELECT version FROM parse_state WHERE file_id=? AND gen=?`, f.ID, sn.Version).Scan(&v)
		have := err == nil
		if err != nil && err != sql.ErrNoRows {
			return total, err
		}
		if have && v == src.Version() && !reparse {
			continue
		}
		n, err := p.parseVersion(f, src, caps, sn, have)
		total += n
		if err != nil {
			return total, err
		}
	}
	if ts, ok := src.(source.Tombstoner); ok && len(snaps) > 0 {
		n, err := p.parseTombstones(f, src, ts, caps, snaps[len(snaps)-1])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (p *Parser) reparseSnapshots(f store.File) error {
	src, ok := p.bySrc[f.Source]
	if !ok {
		return nil
	}
	p.run.Lock()
	defer p.run.Unlock()
	_, err := p.parseSnapshots(f, src, true)
	return err
}

func (p *Parser) parseVersion(f store.File, src source.Source, caps source.Captures, sn store.Snapshot, replace bool) (int, error) {
	if replace {
		// Replacing deletes by (file_id, gen): that needs the deferred indexes.
		if err := p.cfg.Store.EnsureIndexes(); err != nil {
			return 0, err
		}
	}
	fh, err := p.cfg.Store.OpenPointer(f.ID, sn.Version)
	if err != nil {
		return 0, err
	}
	raw, err := io.ReadAll(fh)
	_ = fh.Close()
	if err != nil {
		return 0, err
	}
	g := store.Generation{File: f.ID, Gen: sn.Version}
	recs, perr := src.Parse(caps, raw)
	count := 0
	err = p.cfg.Store.WithWrite("parse.before-commit", func(rawTx *sql.Tx) error {
		tx := newPS(rawTx)
		defer tx.Close()
		if replace {
			if err := p.clearGen(tx, f.ID, sn.Version); err != nil {
				return err
			}
			if err := p.insertReset(tx, f, src, sn.Version); err != nil {
				return err
			}
			p.cfg.Obs.Event("parser_reset", "file", strconv.FormatInt(f.ID, 10), "gen", strconv.Itoa(sn.Version), "source", src.Name())
		}
		if perr != nil {
			if err := p.bad(tx, f, g, 0, len(raw), perr.Error()); err != nil {
				return err
			}
		} else {
			if err := p.insert(tx, f, src, caps, g, 0, len(raw), recs, sn.SeenNS); err != nil {
				return err
			}
			count = len(recs)
		}
		if _, err := tx.Exec(`INSERT INTO parse_state(file_id,gen,version) VALUES(?,?,?) ON CONFLICT(file_id,gen) DO UPDATE SET version=excluded.version`,
			f.ID, sn.Version, src.Version()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
			return err
		}
		if err := p.flushSessions(tx); err != nil {
			return err
		}
		return p.mark(tx)
	})
	return count, err
}

// parseTombstones writes a deletion record for each tombstone of the file that
// has none, under the file's latest stored version.
func (p *Parser) parseTombstones(f store.File, src source.Source, ts source.Tombstoner, caps source.Captures, last store.Snapshot) (int, error) {
	ats, err := p.cfg.Store.Tombstones(f.ID)
	if err != nil {
		return 0, err
	}
	if len(ats) > 0 {
		// The look-up by key below needs records_ident (deferred in a bulk rebuild).
		if err := p.cfg.Store.EnsureIndexes(); err != nil {
			return 0, err
		}
	}
	var lastRaw []byte
	if fh, err := p.cfg.Store.OpenPointer(f.ID, last.Version); err == nil {
		lastRaw, _ = io.ReadAll(fh)
		_ = fh.Close()
	}
	count := 0
	for _, at := range ats {
		var n int
		if err := p.cfg.Store.Read().QueryRow(`SELECT COUNT(*) FROM records WHERE file_id=? AND gen=? AND off=? AND item=?`,
			f.ID, last.Version, at, source.ItemTombstone).Scan(&n); err != nil {
			return count, err
		}
		if n > 0 {
			continue
		}
		rec, ok := ts.Tombstone(caps, lastRaw, time.Unix(0, at))
		if !ok {
			continue
		}
		err := p.cfg.Store.WithWrite("parse.before-commit", func(rawTx *sql.Tx) error {
			tx := newPS(rawTx)
			defer tx.Close()
			// off carries the deletion time so each deletion is its own record.
			if err := p.insert(tx, f, src, caps, store.Generation{File: f.ID, Gen: last.Version}, at, 0, []source.Record{rec}, at); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('parse_seq','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT)`); err != nil {
				return err
			}
			if err := p.flushSessions(tx); err != nil {
				return err
			}
			return p.mark(tx)
		})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
