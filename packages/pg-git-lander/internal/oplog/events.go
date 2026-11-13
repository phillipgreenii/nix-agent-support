package oplog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

// RotateBytes is the size at which events.jsonl is rotated (one archive kept).
const RotateBytes = int64(20) << 20

// Event types (design section 14.6).
const (
	EvOpStart         = "op_start"
	EvStage           = "stage"
	EvLockWait        = "lock_wait"
	EvRaceRetry       = "race_retry"
	EvHook            = "hook"
	EvHolderConverted = "holder_converted"
	EvConflict        = "conflict"
	EvOpEnd           = "op_end"
)

// Common holds the fields every event line carries besides ts and the type.
type Common struct {
	Repo, Host  string
	PID         int
	ToolVersion string
	OpID        string
	Operation   string
	Branch      string
	Target      string
}

// Events appends to events.jsonl. Each line is one write call on a file
// opened with O_APPEND. When the file has reached RotateAt it is renamed to
// events.jsonl.1 (replacing the older archive) while this process holds the
// admin lock ref refs/pg-git-lander/locks/admin/events; a writer that raced
// the rename appends to the archive, which is harmless.
type Events struct {
	Path   string
	Common Common
	Clock  seams.Clock
	// Locks and Owner take the admin lock for rotation. With Locks nil the
	// file is never rotated.
	Locks *locks.Layer
	Owner locks.Owner
	// RotateAt overrides RotateBytes when positive.
	RotateAt int64
	// RotateWait bounds how long rotation may wait for the admin lock
	// (default 5s); a rotation that cannot take it is skipped.
	RotateWait time.Duration

	rotMu sync.Mutex
	// beforeRename is a test seam run just before the rotation rename, while
	// the admin lock is held.
	beforeRename func()
}

func (e *Events) threshold() int64 {
	if e.RotateAt > 0 {
		return e.RotateAt
	}
	return RotateBytes
}

// Emit appends one event of type typ with fields. The common fields win over
// fields of the same name.
func (e *Events) Emit(ctx context.Context, typ string, fields map[string]any) error {
	e.rotateIfNeeded(ctx)
	m := make(map[string]any, len(fields)+10)
	for k, v := range fields {
		m[k] = v
	}
	c := e.Common
	m["ts"] = e.Clock.Now().UTC().Format(time.RFC3339Nano)
	m["event"] = typ
	m["repo"], m["host"], m["pid"], m["tool_version"] = c.Repo, c.Host, c.PID, c.ToolVersion
	m["op_id"], m["operation"], m["branch"], m["target"] = c.OpID, c.Operation, c.Branch, c.Target
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(e.Path), DirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(e.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, FileMode)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	return errors.Join(werr, f.Close())
}

func (e *Events) rotateIfNeeded(ctx context.Context) {
	if e.Locks == nil {
		return
	}
	if info, err := os.Stat(e.Path); err != nil || info.Size() < e.threshold() {
		return
	}
	// A wait that the rotation itself causes reports through the layer's
	// callbacks into Emit; never rotate re-entrantly.
	if !e.rotMu.TryLock() {
		return
	}
	defer e.rotMu.Unlock()
	wait := e.RotateWait
	if wait <= 0 {
		wait = 5 * time.Second
	}
	actx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	set, err := e.Locks.Acquire(actx, locks.Spec{
		Ref: locks.AdminRef(locks.AdminEvents), Role: locks.RoleAdmin,
		Rec: locks.Record{Owner: e.Owner},
	})
	if err != nil {
		return // another process is rotating, or the lock layer failed: skip
	}
	defer func() { _ = set.Release(context.WithoutCancel(ctx)) }()
	// Re-check under the lock: another process may have rotated already.
	if info, err := os.Stat(e.Path); err != nil || info.Size() < e.threshold() {
		return
	}
	if e.beforeRename != nil {
		e.beforeRename()
	}
	_ = os.Rename(e.Path, e.Path+".1")
}

// OpStart emits op_start.
func (e *Events) OpStart(ctx context.Context, opContext map[string]any, worktrees []locks.WorktreeEntry, workBranch, placeholderBranch string, keep locks.Keep, configDigest string) error {
	return e.Emit(ctx, EvOpStart, map[string]any{
		"context": opContext, "worktrees": worktrees, "work_branch": workBranch,
		"placeholder_branch": placeholderBranch, "keep": keep, "config_digest": configDigest,
	})
}

// Stage emits stage: the stage entered, the one left and how long it took.
func (e *Events) Stage(ctx context.Context, stage, previous string, previousDuration time.Duration) error {
	return e.Emit(ctx, EvStage, map[string]any{
		"stage": stage, "previous_stage": previous, "duration_ms": previousDuration.Milliseconds(),
	})
}

// LockWait emits lock_wait.
func (e *Events) LockWait(ctx context.Context, w locks.LockWait) error {
	return e.Emit(ctx, EvLockWait, map[string]any{
		"lock":      w.Lock,
		"holder":    map[string]any{"op_id": w.Holder.OpID, "stage": w.Holder.Stage},
		"waited_ms": w.Waited.Milliseconds(), "result": w.Result,
	})
}

// RaceRetry emits race_retry: TARGET moved between the snapshot and the move.
// revalidate is the configured mode and reused is true when the hook result
// was kept (a rebase under the TARGET lock, no new hook run); attempt is then
// the number of full race returns so far, unchanged.
func (e *Events) RaceRetry(ctx context.Context, attempt int, oldTargetTip, newTargetTip, revalidate string, reused bool) error {
	return e.Emit(ctx, EvRaceRetry, map[string]any{
		"attempt": attempt, "old_target_tip": oldTargetTip, "new_target_tip": newTargetTip,
		"revalidate": revalidate, "reused": reused,
	})
}

// Hook emits hook for one command run. It carries argv0 only: no argument,
// no variable value and no output.
func (e *Events) Hook(ctx context.Context, rec hooks.CommandRecord) error {
	return e.Emit(ctx, EvHook, map[string]any{
		"point": rec.Point, "index": rec.Index, "argv0": rec.Argv0, "exit": rec.Code,
		"duration_ms": rec.Duration.Milliseconds(), "result": rec.Result,
	})
}

// HolderConverted emits holder_converted.
func (e *Events) HolderConverted(ctx context.Context, c locks.Conversion) error {
	return e.Emit(ctx, EvHolderConverted, map[string]any{
		"lock": c.Lock, "previous_owner": c.PreviousOwner, "why": string(c.Why),
	})
}

// Conflict emits conflict.
func (e *Events) Conflict(ctx context.Context, paths []string, rule, result string) error {
	if paths == nil {
		paths = []string{}
	}
	return e.Emit(ctx, EvConflict, map[string]any{"paths": paths, "rule": rule, "result": result})
}

// OpEnd is the payload of op_end.
type OpEnd struct {
	Outcome      string         `json:"outcome"`
	Reason       string         `json:"reason"`
	Retryable    bool           `json:"retryable"`
	Stage        string         `json:"stage"`
	PathTaken    string         `json:"path_taken"`
	Stats        map[string]any `json:"stats"`
	BranchFate   string         `json:"branch_fate"`
	WorktreeFate string         `json:"worktree_fate"`
	Restored     any            `json:"restored"`
	Warnings     []string       `json:"warnings"`
}

// OpEnd emits op_end.
func (e *Events) OpEnd(ctx context.Context, o OpEnd) error {
	if o.Warnings == nil {
		o.Warnings = []string{}
	}
	return e.Emit(ctx, EvOpEnd, map[string]any{
		"outcome": o.Outcome, "reason": o.Reason, "retryable": o.Retryable, "stage": o.Stage,
		"path_taken": o.PathTaken, "stats": o.Stats, "branch_fate": o.BranchFate,
		"worktree_fate": o.WorktreeFate, "restored": o.Restored, "warnings": o.Warnings,
	})
}

// Attach wires the lock layer's lock_wait and holder_converted emissions to
// the log and the events file (either may be nil). It replaces l.OnWait and
// l.OnConvert.
func Attach(l *locks.Layer, log *Log, ev *Events) {
	l.OnWait = func(ctx context.Context, w locks.LockWait) {
		if log != nil {
			log.LockWait(w)
		}
		if ev != nil {
			_ = ev.LockWait(ctx, w)
		}
	}
	l.OnConvert = func(ctx context.Context, c locks.Conversion) {
		if log != nil {
			log.Conversion(c)
		}
		if ev != nil {
			_ = ev.HolderConverted(ctx, c)
		}
	}
}
