package ledger

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Run states.
const (
	RunRunning         = "Running"
	RunAwaitingAnalyst = "AwaitingAnalyst"
	RunFiled           = "Filed"
	RunSkipped         = "Skipped"
	RunFailed          = "Failed"
)

// Run is one row of the run table.
type Run struct {
	ID                int64
	Seq               int64
	Lens              string
	Scope             string
	WindowFrom        string
	WindowTo          string
	RunKey            string
	ScheduleID        string
	Trigger           string
	State             string
	Analyst           string
	Profile           string
	Model             string
	PromptHash        string
	ElapsedMinutes    float64
	PID               int
	StartedAt         time.Time // process start time of PID
	CreatedAt         time.Time
	EndedAt           time.Time
	StagingPath       string
	EvidenceDir       string
	SessionID         string
	SkipReason        string
	QueryCount        int
	RepairCount       int
	ItemsFiled        int
	ItemsDeduplicated int
	ItemsSkipped      int
}

const runCols = `id,seq,lens,scope,window_from,window_to,run_key,schedule_id,trigger,state,analyst,profile,model,prompt_hash,elapsed_minutes,pid,started_at,created_at,ended_at,staging_path,evidence_dir,session_id,skip_reason,query_count,repair_count,items_filed,items_deduplicated,items_skipped`

type scanner interface{ Scan(...any) error }

func scanRun(s scanner) (Run, error) {
	var r Run
	var started, created, ended string
	err := s.Scan(&r.ID, &r.Seq, &r.Lens, &r.Scope, &r.WindowFrom, &r.WindowTo, &r.RunKey, &r.ScheduleID, &r.Trigger,
		&r.State, &r.Analyst, &r.Profile, &r.Model, &r.PromptHash, &r.ElapsedMinutes, &r.PID, &started, &created, &ended,
		&r.StagingPath, &r.EvidenceDir, &r.SessionID, &r.SkipReason, &r.QueryCount, &r.RepairCount, &r.ItemsFiled,
		&r.ItemsDeduplicated, &r.ItemsSkipped)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	r.StartedAt, r.CreatedAt, r.EndedAt = parseTime(started), parseTime(created), parseTime(ended)
	return r, err
}

// InsertRun creates a run row and returns it with its id and sequence. State
// defaults to Running. PID and StartedAt are stored in the same insert, so a
// reaper never sees a Running row without them.
func (t *Tx) InsertRun(ctx context.Context, r Run) (Run, error) {
	if r.State == "" {
		r.State = RunRunning
	}
	seq, err := t.nextSeq(ctx)
	if err != nil {
		return Run{}, err
	}
	res, err := t.exec(ctx, `INSERT INTO run(seq,lens,scope,window_from,window_to,run_key,schedule_id,trigger,state,analyst,profile,model,prompt_hash,elapsed_minutes,pid,started_at,created_at,ended_at,staging_path,evidence_dir,session_id,skip_reason,query_count,repair_count,items_filed,items_deduplicated,items_skipped)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		seq, r.Lens, r.Scope, r.WindowFrom, r.WindowTo, r.RunKey, r.ScheduleID, r.Trigger, r.State, r.Analyst, r.Profile,
		r.Model, r.PromptHash, r.ElapsedMinutes, r.PID, fmtTime(r.StartedAt), fmtTime(t.now()), fmtTime(r.EndedAt),
		r.StagingPath, r.EvidenceDir, r.SessionID, r.SkipReason, r.QueryCount, r.RepairCount, r.ItemsFiled,
		r.ItemsDeduplicated, r.ItemsSkipped)
	if err != nil {
		return Run{}, err
	}
	id, _ := res.LastInsertId()
	return t.GetRun(ctx, id)
}

// UpdateRun rewrites every mutable column of the run with r.ID.
func (t *Tx) UpdateRun(ctx context.Context, r Run) error {
	res, err := t.exec(ctx, `UPDATE run SET window_from=?,window_to=?,run_key=?,schedule_id=?,trigger=?,state=?,analyst=?,profile=?,model=?,prompt_hash=?,elapsed_minutes=?,pid=?,started_at=?,ended_at=?,staging_path=?,evidence_dir=?,session_id=?,skip_reason=?,query_count=?,repair_count=?,items_filed=?,items_deduplicated=?,items_skipped=? WHERE id=?`,
		r.WindowFrom, r.WindowTo, r.RunKey, r.ScheduleID, r.Trigger, r.State, r.Analyst, r.Profile, r.Model, r.PromptHash,
		r.ElapsedMinutes, r.PID, fmtTime(r.StartedAt), fmtTime(r.EndedAt), r.StagingPath, r.EvidenceDir, r.SessionID,
		r.SkipReason, r.QueryCount, r.RepairCount, r.ItemsFiled, r.ItemsDeduplicated, r.ItemsSkipped, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRun returns a run or ErrNotFound.
func (t *Tx) GetRun(ctx context.Context, id int64) (Run, error) {
	return scanRun(t.queryRow(ctx, `SELECT `+runCols+` FROM run WHERE id=?`, id))
}

// GetRun returns a run or ErrNotFound.
func (l *Ledger) GetRun(ctx context.Context, id int64) (Run, error) {
	return scanRun(l.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM run WHERE id=?`, id))
}

// LastRunForKey returns the newest run with the given run key, or ErrNotFound.
func (l *Ledger) LastRunForKey(ctx context.Context, key string) (Run, error) {
	return scanRun(l.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM run WHERE run_key=? ORDER BY id DESC LIMIT 1`, key))
}

// Lock is a held run lock.
type Lock struct {
	Lens, Scope string
	RunID       int64
	PID         int
	StartedAt   time.Time
	AcquiredAt  time.Time
}

// AcquireLock takes the (lens, scope) run lock for a run, recording the pid
// and its process start time. A held lock yields ErrLockBusy. The lock is a
// table row (not flock) so a reaper can read it.
func (t *Tx) AcquireLock(ctx context.Context, lens, scope string, runID int64, pid int, startedAt time.Time) error {
	_, err := t.exec(ctx, `INSERT INTO run_lock(lens,scope,run_id,pid,started_at,acquired_at) VALUES(?,?,?,?,?,?)`,
		lens, scope, runID, pid, fmtTime(startedAt), fmtTime(t.now()))
	if errors.Is(err, ErrDuplicate) {
		return ErrLockBusy
	}
	return err
}

// ReleaseLock deletes the lock row; releasing an absent lock is not an error.
func (t *Tx) ReleaseLock(ctx context.Context, lens, scope string) error {
	_, err := t.exec(ctx, `DELETE FROM run_lock WHERE lens=? AND scope=?`, lens, scope)
	return err
}

// GetLock returns the lock for (lens, scope) or ErrNotFound.
func (l *Ledger) GetLock(ctx context.Context, lens, scope string) (Lock, error) {
	var k Lock
	var started, acquired string
	err := l.db.QueryRowContext(ctx, `SELECT lens,scope,run_id,pid,started_at,acquired_at FROM run_lock WHERE lens=? AND scope=?`,
		lens, scope).Scan(&k.Lens, &k.Scope, &k.RunID, &k.PID, &started, &acquired)
	if errors.Is(err, sql.ErrNoRows) {
		return Lock{}, ErrNotFound
	}
	k.StartedAt, k.AcquiredAt = parseTime(started), parseTime(acquired)
	return k, err
}

// Locks lists every held lock (the reaper's view).
func (l *Ledger) Locks(ctx context.Context) ([]Lock, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens,scope,run_id,pid,started_at,acquired_at FROM run_lock ORDER BY lens,scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lock
	for rows.Next() {
		var k Lock
		var s, a string
		if err := rows.Scan(&k.Lens, &k.Scope, &k.RunID, &k.PID, &s, &a); err != nil {
			return nil, err
		}
		k.StartedAt, k.AcquiredAt = parseTime(s), parseTime(a)
		out = append(out, k)
	}
	return out, rows.Err()
}

// SetHeartbeat records the last_maintain_at row.
func (t *Tx) SetHeartbeat(ctx context.Context, at time.Time) error {
	_, err := t.exec(ctx, `INSERT INTO meta(key,value) VALUES('last_maintain_at',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fmtTime(at))
	return err
}

// Heartbeat returns the last_maintain_at time, or ErrNotFound when maintain never ran.
func (l *Ledger) Heartbeat(ctx context.Context) (time.Time, error) {
	var v string
	err := l.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='last_maintain_at'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	return parseTime(v), err
}
