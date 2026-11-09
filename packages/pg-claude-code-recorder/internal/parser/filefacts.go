package parser

import (
	"context"
	"database/sql"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// fileFactBatch is what one write transaction learned about files: per file the
// newest record time and the path captures. flushFileFacts writes it as one
// upsert per file just before the transaction commits, in the same transaction
// as the records, so the table is never ahead of or behind them; a transaction
// that rolls back drops it.
type fileFactBatch struct {
	by map[int64]*fileFactDelta
}

type fileFactDelta struct {
	last                      int64
	project, session, agentID string
}

func (tx *pstx) noteFileFact(file, tns int64, caps source.Captures) {
	if tx.ffs == nil {
		tx.ffs = &fileFactBatch{by: map[int64]*fileFactDelta{}}
	}
	d := tx.ffs.by[file]
	if d == nil {
		d = &fileFactDelta{last: tns, project: caps["project"], session: caps["session"], agentID: caps["agent"]}
		tx.ffs.by[file] = d
	}
	if tns > d.last {
		d.last = tns
	}
}

// nullable maps the empty capture to NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const upsertFileFact = `INSERT INTO file_facts(file_id,project,file_session,agent_id,last_time_ns) VALUES(?,?,?,?,?)
ON CONFLICT(file_id) DO UPDATE SET last_time_ns=MAX(last_time_ns,excluded.last_time_ns),
  project=COALESCE(excluded.project,project), file_session=COALESCE(excluded.file_session,file_session), agent_id=COALESCE(excluded.agent_id,agent_id)`

// flushFileFacts upserts file_facts for what the transaction wrote, in file
// order so two runs write the same sequence.
func (p *Parser) flushFileFacts(tx *pstx) error {
	if tx.ffs == nil {
		return nil
	}
	ids := make([]int64, 0, len(tx.ffs.by))
	for id := range tx.ffs.by {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d := tx.ffs.by[id]
		if _, err := tx.Exec(upsertFileFact, id, nullable(d.project), nullable(d.session), nullable(d.agentID), d.last); err != nil {
			return err
		}
	}
	tx.ffs = nil
	return nil
}

// recomputeFileFact sets a file's newest record time from its remaining
// records, after a generation's rows were deleted. The row stays (the path
// facts are the file's), with 0 when no record is left.
func recomputeFileFact(tx *pstx, file int64) error {
	_, err := tx.Exec(`UPDATE file_facts SET last_time_ns=COALESCE((SELECT MAX(time_ns) FROM records WHERE file_id=?1),0) WHERE file_id=?1`, file)
	return err
}

// BackfillFileFacts fills file_facts for a database that existed before the
// table, at most limit files per call, from the saved cursor. Each file is its
// own small write transaction; the newest record time is read from records
// first, outside the write lock (one file's records, never the whole table),
// and merged with MAX so a concurrent parse commit is never lowered. When the
// last file is done it marks the table complete. A call on a marked database
// does nothing and reports done.
func (p *Parser) BackfillFileFacts(ctx context.Context, limit int) (done bool, n int, err error) {
	st := p.cfg.Store
	if st.FileFactsComplete() {
		return true, 0, nil
	}
	ids, err := st.FileIDsAfter(ctx, st.FileFactsCursor(), limit)
	if err != nil {
		return false, 0, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return false, n, err
		}
		if err := p.backfillOne(ctx, id); err != nil {
			return false, n, err
		}
		n++
		if err := st.SetFileFactsCursor(id); err != nil {
			return false, n, err
		}
	}
	if len(ids) < limit {
		return true, n, st.MarkFileFactsComplete()
	}
	return false, n, nil
}

// factsOf is the path facts of a file: the captures its source takes from the
// file's path.
func (p *Parser) factsOf(file int64) (project, session, agentID string) {
	f, err := p.cfg.Store.File(file)
	if err != nil {
		return
	}
	src, ok := p.bySrc[f.Source]
	if !ok {
		return
	}
	rel, ok := p.relPath(file)
	if !ok {
		return
	}
	caps, ok := src.Match(rel)
	if !ok {
		return
	}
	return caps["project"], caps["session"], caps["agent"]
}

func (p *Parser) backfillOne(ctx context.Context, file int64) error {
	last, ok, err := p.cfg.Store.FileNewestRecordNS(ctx, file)
	if err != nil || !ok {
		return err
	}
	project, session, agentID := p.factsOf(file)
	return p.cfg.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(upsertFileFact, file, nullable(project), nullable(session), nullable(agentID), last)
		return err
	})
}

// FileFactDrift is one file whose stored newest record time differs from a
// recompute over records.
type FileFactDrift struct {
	File             int64
	Stored, Computed int64
	Missing          bool // no file_facts row although the file has records
}

// CheckFileFacts compares file_facts with a recompute for up to limit files
// after the given file id, and returns the drifts and the id to continue from
// (0 when the end of the files was reached). It reads one file's records at a
// time, so a pass over a large database is spread over many calls (the daemon
// checks a slice per tick, like the lag reconcile checks on a cadence). With
// repair set, each drifting file is set to the recomputed value. A file whose
// facts are not complete yet (backfill running) is skipped.
func (p *Parser) CheckFileFacts(ctx context.Context, after int64, limit int, repair bool) (drifts []FileFactDrift, next int64, err error) {
	st := p.cfg.Store
	if !st.FileFactsComplete() {
		return nil, 0, nil
	}
	ids, err := st.FileIDsAfter(ctx, after, limit)
	if err != nil {
		return nil, 0, err
	}
	for _, id := range ids {
		next = id
		computed, has, err := st.FileNewestRecordNS(ctx, id)
		if err != nil {
			return drifts, 0, err
		}
		stored, ok, err := st.FileFactLast(ctx, id)
		if err != nil {
			return drifts, 0, err
		}
		var d *FileFactDrift
		switch {
		case has && !ok:
			d = &FileFactDrift{File: id, Computed: computed, Missing: true}
		case ok && stored != computed && (has || stored != 0):
			d = &FileFactDrift{File: id, Stored: stored, Computed: computed}
		}
		if d == nil {
			continue
		}
		drifts = append(drifts, *d)
		if repair {
			project, session, agentID := p.factsOf(id)
			if err := st.WithWrite("", func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO file_facts(file_id,project,file_session,agent_id,last_time_ns) VALUES(?,?,?,?,?)
ON CONFLICT(file_id) DO UPDATE SET last_time_ns=excluded.last_time_ns`, id, nullable(project), nullable(session), nullable(agentID), computed)
				return err
			}); err != nil {
				return drifts, 0, err
			}
		}
	}
	if len(ids) < limit {
		next = 0
	}
	return drifts, next, nil
}
