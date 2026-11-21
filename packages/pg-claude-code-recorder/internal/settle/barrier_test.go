package settle

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A process that dies after the archive is flushed and before the "removal
// pending" record is committed must not have touched Claude's directory: the
// archive flush runs before the removal decision and before any rename.
func TestCrashBetweenArchiveFlushAndPendingRecordRemovesNothing(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(6, removeAge+time.Hour)
	disarm := faults.ArmPanic("remove.before-pending")
	crashed := faults.Recover(func() { _, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}}) })
	disarm()
	if !crashed {
		t.Fatal("the point between the archive flush and the pending record was not reached")
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the original changed before the pending record was committed")
	}
	e.crash()
	if got := e.stage(); got != StageSettled {
		t.Fatalf("stage after the crash and recovery = %s, want settled (no removal decision was made)", got)
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the original is gone after recovery")
	}
	// The next removal pass flushes the archive again and completes.
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("removal after recovery: %+v", rep)
	}
	if e.exists(e.transcript()) {
		t.Fatal("the original is still there after a completed removal")
	}
}

// A reader that pins an older snapshot of the WAL, with a write committed after
// it, keeps a TRUNCATE checkpoint from finishing. Removal does not checkpoint
// the index, so it must still succeed: the archive is flushed and the "removal
// pending" record is committed FULL (journal fsync), neither of which waits for
// a reader. This is the state a busy daemon is always in.
func TestRemovalSucceedsWhileAReaderPinsTheWAL(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(6, removeAge+time.Hour)
	rd, err := e.st.Read().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Rollback() }()
	var n int
	if err := rd.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// A write after the reader's snapshot: a checkpoint cannot truncate past it.
	if err := e.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES('barrier_probe','1') ON CONFLICT(key) DO UPDATE SET value='1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Prove the setup pins the WAL: the checkpoint the removal path used to run
	// reports busy while the reader is open.
	if err := e.st.Barrier(); err == nil {
		t.Fatal("the held reader did not pin the WAL, so this test proves nothing")
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 1 {
		t.Fatalf("removal with a reader pinning the WAL: %+v (skipped %v)", rep, e.skipReasons(rep, sess))
	}
	if e.exists(e.transcript()) {
		t.Fatal("the original is still there after a completed removal")
	}
	if got := e.stage(); got != store.StageRemoved {
		t.Fatalf("stage = %s, want %s", got, store.StageRemoved)
	}
}

// failReadDirFS fails ReadDir of one directory, once armed, as a full volume or a
// vanished directory would.
type failReadDirFS struct {
	fsx.FS
	armed *bool
	dir   func() string
}

func (f failReadDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if *f.armed && name == f.dir() {
		return nil, &os.PathError{Op: "readdir", Path: name, Err: syscall.ENOSPC}
	}
	return f.FS.ReadDir(name)
}

// The archive flush is what remains of the durability barrier. When it fails
// the removal is refused with its own reason, counted as a blocked operation
// when the cause is a full volume, and nothing is moved.
func TestArchiveFlushFailureRefusesRemovalAsDurabilityBarrier(t *testing.T) {
	armed := false
	e := newEnvWith(t, false, func(pre *env) {
		pre.removeOn = true
		pre.claudeFS = func(in fsx.FS) fsx.FS {
			// pre is the env being built; its store exists by the time ReadDir runs.
			return failReadDirFS{FS: in, armed: &armed, dir: func() string { return pre.st.SegDir(sess) }}
		}
	})
	content := e.oldSettled(6, removeAge+time.Hour)
	armed = true
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockBarrier) {
		t.Fatalf("removal with a failing archive flush: %+v (skipped %v)", rep, e.skipReasons(rep, sess))
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the original changed although the archive flush failed")
	}
	if got := e.stage(); got != StageSettled {
		t.Fatalf("stage = %s, want settled", got)
	}
	if e.counter(obs.MBlockedNoSpace, map[string]string{"op": obs.OpRemovalBarrier}) < 1 {
		t.Fatal("blocked_no_space_total{op=removal_barrier} not counted for a full volume")
	}
	armed = false
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("removal once the flush works again: %+v", rep)
	}
}
