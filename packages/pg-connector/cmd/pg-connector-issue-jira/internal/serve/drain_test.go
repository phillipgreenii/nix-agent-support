package serve_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// seedPending leaves one pending settle row of a watched issue, awaiting its
// detail group with a deadline an hour away: the row a SIGTERM must still append.
func seedPending(t *testing.T) func(r *rig) {
	return func(r *rig) {
		st, _, err := store.Open(r.storePath(), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		awaited, err := json.Marshal([]map[string]any{{"group": "detail", "since": now}})
		if err != nil {
			t.Fatal(err)
		}
		err = st.InTx(context.Background(), func(tx *store.Tx) error {
			return tx.PutPending(context.Background(), store.PendingChange{
				Kind: changes.LogKindIssue, ID: "ALPHA-1", Kinds: []string{"title"},
				Fields: json.RawMessage(`{}`), Awaited: awaited,
				DetectedAt: now, Deadline: now.Add(time.Hour),
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancellingTheContextDrainsAppendsEveryPendingRowAndClosesTheStore(t *testing.T) {
	r := newRig(t, fakePjiraDown, "", seedPending(t))
	d := r.start(serve.Hooks{})
	d.waitReady()
	if n := r.count(`SELECT COUNT(*) FROM pending_change`); n != 1 {
		t.Fatalf("pending rows while running = %d, want 1 (the deadline is an hour away)", n)
	}

	if code := d.stop(); code != serve.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, serve.ExitOK, d.stderr.String())
	}

	// Every pending row was ended before the store closed.
	if n := r.count(`SELECT COUNT(*) FROM pending_change`); n != 0 {
		t.Errorf("pending rows after the drain = %d, want 0", n)
	}
	lines := r.daemonLog()
	flushed, drained := indexOf(lines, "pending_changes_flushed"), indexOf(lines, "drain_done")
	if flushed < 0 || drained < 0 || flushed > drained {
		t.Errorf("want pending_changes_flushed before drain_done, got positions %d and %d in %v", flushed, drained, lines)
	}
	// The WAL was checkpointed and the store closed: nothing is left in the log.
	if fi, err := os.Stat(r.storePath() + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("the WAL still holds %d bytes after the checkpoint", fi.Size())
	}
	// The lock and the socket are released: a new daemon starts on the same
	// directory and drains again.
	if _, err := os.Lstat(r.cfg.SocketPath); !os.IsNotExist(err) {
		t.Errorf("the drained daemon must remove its socket: %v", err)
	}
	again := r.start(serve.Hooks{})
	again.waitReady()
	if code := again.stop(); code != serve.ExitOK {
		t.Errorf("second run exit = %d; stderr: %s", code, again.stderr.String())
	}
}

// A cancellation that arrives while the store still migrates ends the daemon
// cleanly and leaves the lock free.
func TestCancellingDuringStartupExitsCleanly(t *testing.T) {
	r := newRig(t, fakePjiraOK, "", nil)
	d := r.start(serve.Hooks{MigrationDelay: 3 * time.Second})
	until(t, 15*time.Second, "the socket to answer", func() bool {
		resp, err := r.call(serve.Instance, "status", map[string]any{})
		return err == nil && resp.Error != nil && strings.HasPrefix(resp.Error.Message, "migrating:")
	})
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d, want %d; stderr: %s", code, serve.ExitOK, d.stderr.String())
	}
}

func indexOf(lines []map[string]any, change string) int {
	for i, l := range lines {
		if l["change"] == change {
			return i
		}
	}
	return -1
}
