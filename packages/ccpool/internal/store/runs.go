package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Run end sources recorded in session_runs.end_source.
const (
	RunEndClose  = "close"  // closeWithReason after a successful teardown
	RunEndHook   = "hook"   // the SessionEnd hook (`ccpool hook end`)
	RunEndReaper = "reaper" // reaper Pass 0 / launch-time stale-run cleanup
)

// Run is one launch-or-resume of a session. EndedAt is 0 while the run is open;
// EndReason on an OPEN run is a PENDING reason written before /exit.
type Run struct {
	ID        int64
	SessionID int64
	StartedAt int64
	EndedAt   int64 // 0 = open
	EndReason string
	EndSource string
}

// Open reports whether the run has not ended.
func (r Run) Open() bool { return r.EndedAt == 0 }

const runCols = `id, session_id, started_at, ended_at, end_reason, end_source`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var ended sql.NullInt64
	err := sc.Scan(&r.ID, &r.SessionID, &r.StartedAt, &ended, &r.EndReason, &r.EndSource)
	r.EndedAt = ended.Int64
	return r, err
}

// OpenRun starts a new run for externalID at the injected clock's now and
// returns its id. The caller MUST have ended any stale open run first (a
// session never has two open runs); see the session package's startRun.
func (s *Store) OpenRun(ctx context.Context, externalID string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO session_runs (session_id, started_at)
		 SELECT id, ? FROM sessions WHERE external_id = ?`,
		s.clock.Now().Unix(), externalID)
	if err != nil {
		return 0, fmt.Errorf("open run %q: %w", externalID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("open run: no row for %q", externalID)
	}
	return res.LastInsertId()
}

// SetRunPendingReason writes reason onto externalID's OPEN run (no-op when the
// session has none). It is how a ccpool-initiated close (including --purge)
// tells the SessionEnd hook "this end already has a reason": the pending reason
// wins over the hook's `exited` guess in FinalizeRun.
func (s *Store) SetRunPendingReason(ctx context.Context, externalID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE session_runs SET end_reason = ?
		 WHERE ended_at IS NULL AND session_id = (SELECT id FROM sessions WHERE external_id = ?)`,
		reason, externalID)
	if err != nil {
		return fmt.Errorf("set run pending reason %q: %w", externalID, err)
	}
	return nil
}

// FinalizeRun is THE one method that ends a run: a single conditional UPDATE
// guarded by ended_at IS NULL, so it is idempotent and safe to call from the
// lock-free SessionEnd hook. A pending end_reason wins over reason. Returns
// won=false (no error) when the run was already ended (or does not exist).
func (s *Store) FinalizeRun(ctx context.Context, runID int64, reason, source string, endedAt int64) (won bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE session_runs SET
			ended_at = ?,
			end_reason = CASE WHEN end_reason <> '' THEN end_reason ELSE ? END,
			end_source = ?
		 WHERE id = ? AND ended_at IS NULL`,
		endedAt, reason, source, runID)
	if err != nil {
		return false, fmt.Errorf("finalize run %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("finalize run %d: %w", runID, err)
	}
	return n > 0, nil
}

// OpenRunFor returns externalID's open run, if any.
func (s *Store) OpenRunFor(ctx context.Context, externalID string) (Run, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM session_runs
		 WHERE ended_at IS NULL AND session_id = (SELECT id FROM sessions WHERE external_id = ?)
		 ORDER BY id DESC LIMIT 1`, externalID)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("open run for %q: %w", externalID, err)
	}
	return r, true, nil
}

// RunsFor returns every run of externalID (open and ended, pending reasons
// included), oldest first. It is the read path for the metrics delete helper.
func (s *Store) RunsFor(ctx context.Context, externalID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM session_runs
		 WHERE session_id = (SELECT id FROM sessions WHERE external_id = ?) ORDER BY id ASC`, externalID)
	if err != nil {
		return nil, fmt.Errorf("runs for %q: %w", externalID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
