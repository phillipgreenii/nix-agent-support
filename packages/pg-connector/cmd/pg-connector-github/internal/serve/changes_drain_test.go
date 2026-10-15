package serve_test

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The SIGTERM drain of the real binary with a settle row pending: the row is
// appended BEFORE the process exits (the store read after exit code 0 holds it,
// the pending table is empty), and a second serve on the same state directory
// starts cleanly, appends nothing more and serves.

func TestChangesDrain_ASIGTERMAppendsThePendingRowBeforeExitAndASecondServeStartsCleanly(t *testing.T) {
	t.Parallel()
	d := startFilled(t, servetest.Options{Config: settleConfig("5m")})
	versionBefore := d.EntityVersion(t, "pr", prA.NodeID)

	armHeadChange(t, d)
	pending := awaitPending(t, d)
	if !slices.Contains(pending.Kinds, "head_changed") {
		t.Fatalf("the pending row = %+v, want head_changed", pending)
	}
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("a row was appended before the drain: %+v", rows)
	}

	// SIGTERM while the dependent is still held: the drain ends the row, then the
	// process exits 0.
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("the drain exited %d; output: %s", code, d.Stderr())
	}
	rows := d.ChangeLog(t)
	if len(rows) != 1 {
		t.Fatalf("change log after the exit = %+v, want the one row the drain appended", rows)
	}
	row := rows[0]
	if !slices.Contains(row.Kinds, "head_changed") || row.ID != prA.Ref() || row.HeadSHA != prAHead {
		t.Errorf("the drained row = %+v, want head_changed of %s at %s", row, prA.Ref(), prAHead)
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got != versionBefore+1 || row.Version != got {
		t.Errorf("entity version = %d (row %d), want %d: moved once", got, row.Version, versionBefore+1)
	}
	if left := d.PendingRows(t); len(left) != 0 {
		t.Errorf("pending rows after the drain = %+v", left)
	}

	// A second serve on the same state directory: the held fetch died with the
	// first process, the shim lets later ones through, and the daemon starts,
	// logs no recovery (nothing was pending) and serves.
	d.Shim.Release("runs")
	d.Start(t)
	if servetest.HasChange(d.DaemonLog(t), "pending_changes_recovered") {
		t.Errorf("the second serve recovered pending rows although the drain left none")
	}
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()}))
	if show.Repo != prA.Repo || show.Number != prA.Number {
		t.Errorf("the second serve answered show with %+v, want %s", show, prA.Ref())
	}
	// The runs dependent the drain did not wait for refreshes now and appends its
	// own row, never a second copy of the drained one.
	until(t, 60*time.Second, "the runs refresh after the second start", func() bool {
		for _, r := range d.ChangeLog(t) {
			if r.Seq != row.Seq && slices.Contains(r.Kinds, "ci_changed") {
				return true
			}
		}
		return false
	})
	quiet(t, d)
	all := d.ChangeLog(t)
	headRows := 0
	for _, r := range all {
		if slices.Contains(r.Kinds, "head_changed") {
			headRows++
		}
	}
	if headRows != 1 {
		t.Errorf("head_changed was logged %d times, want once: %+v", headRows, all)
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Errorf("the second drain exited %d", code)
	}
	requireNoToken(t, d, d.Stderr())
}
