package serve_test

import (
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Restart and cold start for every group (ruling 3 as a test, extended): a stop
// and a start serve everything inside its freshness window, for ALL groups
// including the Phase 3a ones, with NO origin call, and the job table keeps its
// rows. The catch-up after a long downtime is TestRestartAfterALongDowntime...
// in restart_test.go.

func TestRestart_EveryGroupInsideFreshnessIsServedWithZeroOriginCalls_AndTheJobTableSurvives(t *testing.T) {
	d := newDaemon3a(t)
	d.Start(t)
	settle(t, d)
	// Fill the groups that are fetched on a read.
	for _, op := range []string{"files", "commits", "review_pending"} {
		callOK(t, d, "pr", op, map[string]any{"id": prA.Ref()})
	}
	filled := quiet(t, d)
	if d.Shim.CountKind("jobs") != 1 {
		t.Fatalf("fixture: want the failed run's jobs fetched once, got %d", d.Shim.CountKind("jobs"))
	}
	jobRows := countRows(t, d, "job")
	groupRows := countRows(t, d, "entity_group")

	if code := stopCode(t, d); code != 0 {
		t.Fatalf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	if fi, err := os.Stat(d.StorePath() + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("the store's WAL still holds %d bytes after the drain", fi.Size())
	}

	d.Start(t)
	afterStart := quiet(t, d)
	if got, want := afterStart, filled+1; got != want {
		t.Fatalf("a restart made %d origin calls, want exactly the viewer lookup: %v", got-filled, kinds(originSince(d, filled)))
	}
	if got := countRows(t, d, "job"); got != jobRows {
		t.Errorf("job rows = %d after the restart, want %d", got, jobRows)
	}
	if got := countRows(t, d, "entity_group"); got != groupRows {
		t.Errorf("group rows = %d after the restart, want %d", got, groupRows)
	}

	// Every read inside its freshness: all groups, no origin call, none stale.
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()}))
	files := decode[schema.PRFilesResult](t, callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()}))
	commits := decode[schema.PRCommitsResult](t, callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()}))
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prA.Ref()})
	runs := decode[[]schema.CIRun](t, callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()}))
	for _, q := range []string{"mine", "team"} {
		mustList(t, d, q, nil)
	}
	callOK(t, d, "pr", "explain", map[string]any{"id": prA.Ref()})
	if show.Stale || show.ServedFrom != "cache" || len(files.Files) != 2 || files.ServedFrom != "cache" || len(commits.Commits) != 2 || commits.ServedFrom != "cache" {
		t.Errorf("served after the restart: show %+v files %+v commits %+v", show.ServedFrom, files.ServedFrom, commits.ServedFrom)
	}
	if len(runs) != 2 || len(runs[0].Jobs)+len(runs[1].Jobs) != 2 {
		t.Errorf("list_runs after the restart = %+v, want both runs with the failed one's two jobs", runs)
	}
	if got := len(d.Shim.OriginCalls()); got != afterStart {
		t.Errorf("reads inside freshness made %d origin calls after the restart, want 0: %v", got-afterStart, kinds(originSince(d, afterStart)))
	}
	// Every group is fresh and none is due: explain says so.
	res := decode[explain.Result](t, callOK(t, d, "pr", "explain", map[string]any{"id": prA.Ref()}))
	for _, g := range res.Groups {
		if g.FetchedAt != nil && g.Status != "fresh" {
			t.Errorf("group %s is %s after the restart: %+v", g.Group, g.Status, g)
		}
	}
	// And nothing is started by the passage of a moment.
	time.Sleep(1500 * time.Millisecond)
	if got := len(d.Shim.OriginCalls()); got != afterStart {
		t.Errorf("the daemon made %d origin calls on its own after the restart", got-afterStart)
	}
}
