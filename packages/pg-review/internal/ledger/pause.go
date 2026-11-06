package ledger

import (
	"context"
	"database/sql"
	"errors"
)

// PauseRuns is the number of consecutive all-Rejected runs that pauses a lens.
const PauseRuns = 3

// RecordResume stores a resume record for a lens; it ends any pause.
func (t *Tx) RecordResume(ctx context.Context, lens string) error {
	seq, err := t.nextSeq(ctx)
	if err != nil {
		return err
	}
	_, err = t.exec(ctx, `INSERT INTO lens_resume(seq,lens,resumed_at) VALUES(?,?,?)`, seq, lens, fmtTime(t.now()))
	return err
}

// IsLensPaused reports whether a lens is paused: the last three runs of the
// lens that each filed at least one item (a run with an occurrence) each have
// every item they filed now Rejected, all three were created after the latest
// Adopted, Deferred or Reopen ruling on any item of the lens and after the
// latest resume record. A run that filed nothing is ignored and does not
// reset the count. Items link to runs through the occurrence's run.
func (l *Ledger) IsLensPaused(ctx context.Context, lens string) (bool, error) {
	var barrier int64
	err := l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(s),0) FROM (
		SELECT seq AS s FROM lens_resume WHERE lens=?
		UNION ALL
		SELECT d.seq FROM decision d JOIN topic t ON t.topic_id=d.topic_id
		  WHERE t.lens=? AND d.ruling IN ('Adopted','Deferred','Reopen'))`, lens, lens).Scan(&barrier)
	if err != nil {
		return false, err
	}
	rows, err := l.db.QueryContext(ctx, `SELECT r.id, r.seq FROM run r
		WHERE r.lens=? AND EXISTS (SELECT 1 FROM occurrence o WHERE o.run_id=r.id)
		ORDER BY r.seq DESC LIMIT ?`, lens, PauseRuns)
	if err != nil {
		return false, err
	}
	type rr struct{ id, seq int64 }
	var runs []rr
	for rows.Next() {
		var x rr
		if err := rows.Scan(&x.id, &x.seq); err != nil {
			_ = rows.Close()
			return false, err
		}
		runs = append(runs, x)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if len(runs) < PauseRuns {
		return false, nil
	}
	for _, x := range runs {
		if x.seq <= barrier {
			return false, nil
		}
		var notRejected int
		err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM occurrence o JOIN topic t ON t.topic_id=o.topic_id
			WHERE o.run_id=? AND t.state<>'Rejected'`, x.id).Scan(&notRejected)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if notRejected > 0 {
			return false, nil
		}
	}
	return true, nil
}
