package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Closed-row retention: a dead row that someone closed and nothing has touched
// since is deleted once closed_at is older than the retention. The transcript is
// never removed, and a dead row nobody closed is never deleted.

const closedRetention = 7 * 24 * time.Hour

// closedRowEnv is a deadFixture whose Exister reports every transcript present
// (so rows are resumable, not phantoms) and counts any transcript removal.
func closedRowEnv(t *testing.T, now time.Time, rows ...deadRow) *deadEnv {
	t.Helper()
	e := deadFixture(t, now, rows...)
	for _, r := range rows {
		if _, err := e.st.OpenRun(context.Background(), r.id); err != nil {
			t.Fatalf("OpenRun %s: %v", r.id, err)
		}
	}
	return e
}

func (e *deadEnv) exists(t *testing.T, id string) bool {
	t.Helper()
	_, ok, err := e.st.GetByExternalID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByExternalID(%s): %v", id, err)
	}
	return ok
}

func TestReapClosedRow_deletesPastRetentionAndFinalizesRun(t *testing.T) {
	calls := spyClosed(t)
	now := time.Unix(10_000_000, 0)
	ageSec := sec(closedRetention) + 3600
	closedAt := now.Unix() - sec(closedRetention) - 60
	e := closedRowEnv(t, now, deadRow{id: "old", state: store.Working, ageSec: ageSec, closedAt: closedAt})
	run, ok, err := e.st.OpenRunFor(context.Background(), "old")
	if err != nil || !ok {
		t.Fatalf("OpenRunFor: ok=%v err=%v", ok, err)
	}
	e.reap(t, WithClosedRowRetention(closedRetention))
	if e.exists(t, "old") {
		t.Fatal("closed dead row past retention was kept")
	}
	// The open run was finalized (reason exited, ended at last activity) and
	// its metric emitted exactly once before the runs were removed.
	if len(*calls) != 1 || (*calls)[0].result != "exited" {
		t.Fatalf("closed calls = %+v, want exactly one exited run (run %d)", *calls, run.ID)
	}
}

func TestReapClosedRow_keptInsideRetention(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := closedRowEnv(t, now,
		deadRow{id: "young", state: store.Working, ageSec: 3600 * 48, closedAt: now.Unix() - sec(closedRetention) + 60},
		deadRow{id: "exactly", state: store.Idle, ageSec: sec(closedRetention) + 10, closedAt: now.Unix() - sec(closedRetention)})
	e.reap(t, WithClosedRowRetention(closedRetention))
	for _, id := range []string{"young", "exactly"} {
		if !e.exists(t, id) {
			t.Errorf("%s: deleted inside retention", id)
		}
	}
}

func TestReapClosedRow_neverDeletesUnclosedDeadRow(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	old := 10 * sec(closedRetention)
	e := closedRowEnv(t, now,
		deadRow{id: "dead-working", state: store.Working, ageSec: old},
		deadRow{id: "dead-idle", state: store.Idle, ageSec: old},
		// closed, then resumed: last activity is past closed_at, so it is open again
		deadRow{id: "resumed", state: store.Working, ageSec: 60, closedAt: now.Unix() - old})
	e.reap(t, WithClosedRowRetention(closedRetention))
	for _, id := range []string{"dead-working", "dead-idle", "resumed"} {
		if !e.exists(t, id) {
			t.Errorf("%s: deleted though nobody closed it since its last activity", id)
		}
	}
}

func TestReapClosedRow_deadNeedsInputAwaitingHumanKept(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := closedRowEnv(t, now, deadRow{id: "waiting", state: store.NeedsInput, ageSec: 10 * sec(closedRetention)})
	// Retention alone never deletes it, and the ADR 0086 backstop only closes it.
	e.reap(t, WithClosedRowRetention(closedRetention))
	if !e.exists(t, "waiting") {
		t.Fatal("dead needs_input row awaiting a human was deleted")
	}
	e.reap(t, WithClosedRowRetention(closedRetention), WithDeadNeedsInputTTL(deadTTL))
	if got := e.row(t, "waiting"); got.CloseReason != "dead_needs_input_ttl" {
		t.Fatalf("ADR 0086 path changed: %+v", got)
	}
}

func TestReapClosedRow_zeroRetentionKeepsEverything(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := closedRowEnv(t, now, deadRow{id: "old", state: store.Working, ageSec: 10 * sec(closedRetention), closedAt: now.Unix() - 9*sec(closedRetention)})
	e.reap(t)
	e.reap(t, WithClosedRowRetention(0))
	e.reap(t, WithClosedRowRetention(-time.Hour))
	if !e.exists(t, "old") {
		t.Fatal("row deleted although retention is disabled")
	}
}

func TestReapClosedRow_liveRowNeverDeleted(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := closedRowEnv(t, now, deadRow{id: "live", state: store.Idle, ageSec: 10 * sec(closedRetention), closedAt: now.Unix() - 9*sec(closedRetention), live: true})
	e.reap(t, WithClosedRowRetention(closedRetention))
	if !e.exists(t, "live") {
		t.Fatal("live row deleted")
	}
}

// The unlocked snapshot can be stale: reapDeadRow re-reads under the lock, so a
// row resumed (tmux live) or touched (fresh activity) since is kept.
func TestReapClosedRow_revalidatesUnderLock(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	closedAt := now.Unix() - 2*sec(closedRetention)
	e := closedRowEnv(t, now,
		deadRow{id: "resumed", state: store.Working, ageSec: 2*sec(closedRetention) - 5, closedAt: closedAt, live: true},
		deadRow{id: "touched", state: store.Working, ageSec: 5, closedAt: closedAt})
	for _, id := range []string{"resumed", "touched"} {
		if _, _, err := e.s.reapDeadRow(context.Background(), id, now, closedRetention); err != nil {
			t.Fatalf("reapDeadRow %s: %v", id, err)
		}
		if !e.exists(t, id) {
			t.Errorf("%s: deleted from a stale snapshot", id)
		}
	}
}

func TestReapClosedRow_transcriptNotRemoved(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := closedRowEnv(t, now, deadRow{id: "old", state: store.Working, ageSec: 10 * sec(closedRetention), closedAt: now.Unix() - 9*sec(closedRetention)})
	// The row's recorded transcript path maps to a real file on disk.
	e.s.d.Exister = statExister{real: transcript}
	e.reap(t, WithClosedRowRetention(closedRetention))
	if e.exists(t, "old") {
		t.Fatal("row kept")
	}
	if _, err := os.Stat(transcript); err != nil {
		t.Errorf("transcript was removed: %v", err)
	}
}

// statExister answers Exists by statting the real file standing in for every
// recorded transcript path.
type statExister struct{ real string }

func (e statExister) Exists(string) bool { _, err := os.Stat(e.real); return err == nil }
