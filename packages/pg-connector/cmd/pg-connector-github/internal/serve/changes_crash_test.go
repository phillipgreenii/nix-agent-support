package serve_test

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
)

// A crash and a drain of the real binary with a settle row pending: the row
// survives SIGKILL in the store and is appended after the restart, once; the
// store writes are transactional, so no content is lost and no row is
// duplicated. The promise under test is that a daemon killed with SIGKILL while a
// row is pending appends it after the restart, and that a crash costs the
// supervisor's restart and nothing else; it is run against the real process over
// the counting fake gh.

// settleConfig is the settle scenarios' config: the summary re-read every
// second (a head change is seen at once), the dependents kept fresh for 30 s, the
// active CI freshness at 2 s, and settle_timeout as the test chooses.
func settleConfig(settleTimeout string) map[string]any {
	return map[string]any{
		"summary_ttl": "1s", "terminal_ttl": "1s",
		"detail_ttl": "30s", "detail_max_age": "1h",
		"conversation_ttl": "30s", "conversation_max_age": "1h",
		"ci_active_ttl": "2s", "settle_timeout": settleTimeout,
	}
}

// startFilled starts the real daemon over the Phase 3a fixtures and waits until
// it has stored every group of the fixture pull requests, the baselines of the
// two configured queries and the files and commits groups of prA (so a head
// change has something to re-pull).
func startFilled(t *testing.T, o servetest.Options) *servetest.Daemon {
	t.Helper()
	o.Before = seed3a
	d := servetest.NewDaemon(t, o)
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})
	quiet(t, d)
	return d
}

// awaitPending waits until the head change of prA is stored as a pending row
// and the runs fetch it awaits is held by the shim.
func awaitPending(t *testing.T, d *servetest.Daemon) servetest.PendingRow {
	t.Helper()
	var row servetest.PendingRow
	until(t, 60*time.Second, "the runs fetch to be held and the head change to be stored as a pending row", func() bool {
		if d.Shim.Held("runs") < 1 {
			return false
		}
		rows := d.PendingRows(t)
		if len(rows) != 1 {
			return false
		}
		row = rows[0]
		return true
	})
	return row
}

func TestChangesCrash_ASIGKILLWithAPendingRowAppendsItOnceAfterTheRestart(t *testing.T) {
	t.Parallel()
	d := startFilled(t, servetest.Options{Config: settleConfig("5m")})
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("the cold start logged %+v", rows)
	}
	versionBefore := d.EntityVersion(t, "pr", prA.NodeID)

	// The head moves while the runs fetch is held: the change is stored as a
	// pending row and nothing is appended.
	armHeadChange(t, d)
	pending := awaitPending(t, d)
	if !slices.Contains(pending.Kinds, "head_changed") || !slices.Contains(pending.Awaited, "runs") {
		t.Fatalf("the pending row = %+v, want head_changed awaiting runs", pending)
	}
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("a row was appended while a dependent was still held: %+v", rows)
	}

	// The crash: the pending row is in the store, the dependent never returns.
	d.Kill(t)
	if exited, code := d.Exited(); !exited || code == 0 {
		t.Fatalf("after SIGKILL: exited %v, code %d", exited, code)
	}
	// The held fetch belonged to the dead process; it is let go, and the shim's
	// remaining blocks cleared, so the restarted daemon's own fetch runs through.
	d.Shim.Release("runs")
	if rows := d.PendingRows(t); len(rows) != 1 {
		t.Fatalf("pending rows after the crash = %+v, want the one the daemon stored", rows)
	}
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("the crash logged %+v", rows)
	}

	// The restart picks the row up, re-queues its dependent and appends ONE row
	// with both the summary's and the runs' fields.
	d.Start(t)
	until(t, 60*time.Second, "the restart to append the pending row", func() bool {
		return len(d.ChangeLog(t)) >= 1 && len(d.PendingRows(t)) == 0
	})
	quiet(t, d)
	rows := d.ChangeLog(t)
	if len(rows) != 1 {
		t.Fatalf("the change was appended %d times, want once: %+v", len(rows), rows)
	}
	row := rows[0]
	for _, k := range []string{"head_changed", "ci_changed"} {
		if !slices.Contains(row.Kinds, k) {
			t.Errorf("row kinds = %v, want %s among them", row.Kinds, k)
		}
	}
	if row.ID != prA.Ref() || row.HeadSHA != prAHead {
		t.Errorf("row = %+v, want %s at head %s", row, prA.Ref(), prAHead)
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got != versionBefore+1 || row.Version != got {
		t.Errorf("entity version = %d (row %d), want %d: moved once", got, row.Version, versionBefore+1)
	}
	if !servetest.HasChange(d.DaemonLog(t), "pending_changes_recovered") {
		t.Errorf("the restart did not log pending_changes_recovered; log: %v", d.DaemonLog(t))
	}

	// The daemon the restart left is a working one: a poll sees the row once.
	pollPR(t, d, "crash-reader", servetest.QueryMine) // a first poll starts at the tail
	if got := len(d.ChangeLog(t)); got != 1 {
		t.Errorf("change log has %d rows after the poll", got)
	}
}

func TestChangesCrash_ASIGKILLPastTheDeadlineAppendsTheRowAtTheRestartAndTheDependentAppendsItsOwn(t *testing.T) {
	t.Parallel()
	d := startFilled(t, servetest.Options{Config: settleConfig("3s")})
	versionBefore := d.EntityVersion(t, "pr", prA.NodeID)

	armHeadChange(t, d)
	pending := awaitPending(t, d)
	d.Kill(t)
	d.Shim.Release("runs")

	// Time passes beyond the row's deadline while the daemon is down.
	if wait := time.Until(pending.Deadline); wait > 0 {
		time.Sleep(wait + 200*time.Millisecond)
	}
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("the downtime logged %+v", rows)
	}

	// The dependent is held again at the restart: the row is ended by the start
	// (its deadline passed), with the runs group left stale, before the dependent
	// can return.
	d.Shim.Block("runs", 1)
	d.Start(t)
	until(t, 60*time.Second, "the restart to append the overdue row", func() bool {
		return len(d.ChangeLog(t)) >= 1
	})
	first := d.ChangeLog(t)[0]
	if !slices.Contains(first.Kinds, "head_changed") || slices.Contains(first.Kinds, "ci_changed") {
		t.Errorf("the overdue row's kinds = %v, want head_changed without the runs' ci_changed (runs never returned)", first.Kinds)
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got < versionBefore+1 {
		t.Errorf("entity version = %d, want at least %d", got, versionBefore+1)
	}
	if rows := d.PendingRows(t); len(rows) != 0 {
		t.Errorf("pending rows after the restart = %+v", rows)
	}
	if ended := logLines(d.DaemonLog(t), "settle_ended"); len(ended) == 0 || ended[0]["result"] != "timeout" {
		t.Errorf("the overdue row was not ended with result timeout; log: %v", ended)
	}

	// The dependent's later refresh appends its own row, once.
	until(t, 60*time.Second, "the runs fetch to be held after the restart", func() bool { return d.Shim.Held("runs") >= 1 })
	d.Shim.Release("runs")
	until(t, 60*time.Second, "the dependent's own row", func() bool { return len(d.ChangeLog(t)) >= 2 })
	quiet(t, d)
	rows := d.ChangeLog(t)
	if len(rows) != 2 {
		t.Fatalf("change log = %+v, want the overdue row and the runs' own row", rows)
	}
	if !slices.Contains(rows[1].Kinds, "ci_changed") || rows[1].Version != rows[0].Version+1 {
		t.Errorf("the dependent's row = %+v, want ci_changed with the next version after %d", rows[1], rows[0].Version)
	}
}

// logLines returns the daemon log lines of one change (event) name.
func logLines(lines []map[string]any, change string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["change"] == change {
			out = append(out, l)
		}
	}
	return out
}
