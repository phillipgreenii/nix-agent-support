package settle

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
)

// A process that dies after the archive is flushed and before the index
// checkpoint finishes must not have touched Claude's directory: the barrier
// runs before the "removal pending" decision and before any rename.
func TestCrashBetweenArchiveFlushAndCheckpointRemovesNothing(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(6, removeAge+time.Hour)
	disarm := faults.ArmPanic("store.before-barrier-checkpoint")
	crashed := faults.Recover(func() { _, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}}) })
	disarm()
	if !crashed {
		t.Fatal("the barrier's checkpoint point was not reached")
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the original changed before the barrier finished")
	}
	e.crash()
	if got := e.stage(); got != StageSettled {
		t.Fatalf("stage after the crash and recovery = %s, want settled (no removal decision was made)", got)
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the original is gone after recovery")
	}
	// The next removal pass runs the barrier again and completes.
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("removal after recovery: %+v", rep)
	}
	if e.exists(e.transcript()) {
		t.Fatal("the original is still there after a completed removal")
	}
}

// A reader that pins an older snapshot keeps the checkpoint from finishing; the
// barrier fails and removal is refused with its own reason, with nothing moved.
func TestBarrierThatCannotCheckpointRefusesRemoval(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(6, removeAge+time.Hour)
	rd, err := e.st.Read().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := rd.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// A write after the reader's snapshot: the checkpoint cannot truncate past it.
	if err := e.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES('barrier_probe','1') ON CONFLICT(key) DO UPDATE SET value='1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockBarrier) {
		t.Fatalf("removal with a blocked checkpoint: %+v", rep)
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the original changed although the barrier failed")
	}
	if got := e.stage(); got != StageSettled {
		t.Fatalf("stage = %s, want settled", got)
	}
	_ = rd.Rollback()
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("removal once the reader is gone: %+v", rep)
	}
}
