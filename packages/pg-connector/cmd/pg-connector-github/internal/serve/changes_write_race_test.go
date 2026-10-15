package serve_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
)

// The write race of the real binary: a runs refresh that began before a
// rerun_failed write reads the old state and is held; the write invalidates the
// group; the held refresh, once released, neither overwrites what the write's
// own refetch stored nor logs a change, and the refetch appends the one row,
// with origin "write".

func TestChangesWriteRace_ARefreshStartedBeforeAWriteNeitherOverwritesNorLogsAndTheRefetchAppendsWithOriginWrite(t *testing.T) {
	t.Parallel()
	d := servetest.NewDaemon(t, servetest.Options{
		Hooks: "poll_interval=50ms",
		Config: map[string]any{
			// The failed run's freshness is ci_failed_ttl (ci_active_ttl applies
			// only while a run is active); both are short so the group's
			// background refresh begins without a read.
			"ci_active_ttl": "2s", "ci_failed_ttl": "2s",
			"detail_ttl": "30s", "detail_max_age": "1h", "conversation_ttl": "30s", "conversation_max_age": "1h",
		},
		Before: func(d *servetest.Daemon) {
			seed3a(d)
			// The rerun starts attempt 2 of the failed run: the runs list afterwards
			// shows it in progress.
			d.Shim.Set("runs.after_rerun.json", servetest.RunsJSON(t, prA.Repo, "feature-3",
				servetest.Run{ID: 3001, Name: "ci", Status: "in_progress", Head: "head-3", Attempt: 2},
				servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"}))
			d.Shim.After("rerun", "runs.after_rerun.json", servetest.RunsFile(prA.Repo, "feature-3"))
		},
	})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("the cold start logged %+v", rows)
	}
	versionBefore := d.EntityVersion(t, "pr", prA.NodeID)
	before := d.Group(t, prA.NodeID, "runs")
	if !before.Present || strings.Contains(before.Content, "in_progress") {
		t.Fatalf("fixture: the runs group should hold the failed run only: %+v", before)
	}

	// The refresh that begins before the write: the next runs call (the group's
	// own refresh, two seconds after its last fetch) is held with the OLD answer.
	d.Shim.Block("runs", 1)
	until(t, 60*time.Second, "the group's background refresh to begin and be held", func() bool { return d.Shim.Held("runs") >= 1 })
	if rows := d.ChangeLog(t); len(rows) != 0 {
		t.Fatalf("the held refresh logged %+v", rows)
	}

	// The write: it reads the runs itself (the block is spent, it passes), sends
	// the rerun, and invalidates the group while the old refresh is still held.
	callOK(t, d, "ci", "rerun_failed", map[string]any{"pr_id": prA.Ref()})
	if got := d.Shim.CountKind("rerun"); got != 1 {
		t.Fatalf("rerun requests = %d, want 1", got)
	}
	if got := d.Group(t, prA.NodeID, "runs"); got.Generation <= before.Generation {
		t.Errorf("the write did not invalidate the runs group: generation %d, was %d", got.Generation, before.Generation)
	}

	// The held refresh now finishes. Its old answer must not be stored over the
	// invalidated group and must not be logged.
	d.Shim.Release("runs")
	until(t, 60*time.Second, "the write's refetch to append its row", func() bool { return len(d.ChangeLog(t)) >= 1 })
	quiet(t, d)
	rows := d.ChangeLog(t)
	if len(rows) != 1 {
		t.Fatalf("the write and the stale refresh appended %d rows, want the one: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Origin != "write" {
		t.Errorf("the row's origin = %q, want write", row.Origin)
	}
	if !slices.Contains(row.Kinds, "ci_changed") || row.ID != prA.Ref() {
		t.Errorf("the row = %+v, want ci_changed of %s", row, prA.Ref())
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got != versionBefore+1 || row.Version != got {
		t.Errorf("entity version = %d (row %d), want %d: moved once", got, row.Version, versionBefore+1)
	}
	after := d.Group(t, prA.NodeID, "runs")
	if !strings.Contains(after.Content, "in_progress") {
		t.Errorf("the stored runs content lost the rerun (the stale refresh overwrote it): %s", after.Content)
	}
}
