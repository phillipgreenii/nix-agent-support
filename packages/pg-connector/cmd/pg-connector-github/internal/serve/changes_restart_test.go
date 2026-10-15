package serve_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Restart over a full store (Ruling 3 as a test, for the change engine): every
// group is inside its freshness at the restart, so every read is served from the
// store with NO origin call, and the unchanged content appends nothing. The
// catch-up spend after a long downtime is covered by restart_groups_test.go and
// the load test.

// callCounts is the number of recorded gh calls per kind, the token requests
// included.
func callCounts(d *servetest.Daemon) map[string]int {
	out := map[string]int{}
	for _, c := range d.Shim.Calls() {
		out[c.Kind()]++
	}
	return out
}

func TestChangesRestart_AFullStoreServesEveryReadInsideFreshnessWithZeroOriginCallsAndAppendsNothing(t *testing.T) {
	t.Parallel()
	hour := "1h"
	d := servetest.NewDaemon(t, servetest.Options{
		Before: seed3a,
		Config: map[string]any{
			"summary_ttl": hour, "terminal_ttl": hour, "detail_ttl": hour, "conversation_ttl": hour,
			"ci_active_ttl": hour, "ci_failed_ttl": hour,
		},
	})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	for _, op := range []string{"files", "commits", "review_pending"} {
		callOK(t, d, "pr", op, map[string]any{"id": prA.Ref()})
	}
	callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()})
	// A consumer's key exists in the store, so the restart has a cursor to keep.
	pollPR(t, d, "restart-reader", servetest.QueryMine)
	filled := quiet(t, d)

	logBefore, versionBefore := d.ChangeLog(t), d.EntityVersion(t, "pr", prA.NodeID)
	countsBefore := callCounts(d)
	if len(logBefore) != 0 || len(d.PendingRows(t)) != 0 {
		t.Fatalf("fixture: the filling logged %+v / pending %+v", logBefore, d.PendingRows(t))
	}

	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	d.Start(t)
	afterStart := quiet(t, d)

	// The restart's one origin call is the viewer lookup (the identity the
	// daemon scopes its queries by); every read kind is unchanged (checked below
	// for every kind of call).
	if got, want := afterStart, filled+1; got != want {
		t.Fatalf("the restart made %d origin calls, want exactly the viewer lookup: %v", got-filled, kinds(originSince(d, filled)))
	}

	// Every read inside its freshness: all groups, served from the store.
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()}))
	files := decode[schema.PRFilesResult](t, callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()}))
	commits := decode[schema.PRCommitsResult](t, callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()}))
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prA.Ref()})
	runs := decode[[]schema.CIRun](t, callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()}))
	for _, q := range []string{servetest.QueryMine, servetest.QueryTeam} {
		mustList(t, d, q, nil)
	}
	if show.Stale || show.ServedFrom != "cache" || files.ServedFrom != "cache" || commits.ServedFrom != "cache" || len(runs) != 2 {
		t.Errorf("after the restart: show %s stale %v, files %s, commits %s, %d runs; want everything from the cache",
			show.ServedFrom, show.Stale, files.ServedFrom, commits.ServedFrom, len(runs))
	}
	if got := len(d.Shim.OriginCalls()); got != afterStart {
		t.Errorf("reads inside freshness made %d origin calls after the restart, want 0: %v", got-afterStart, kinds(originSince(d, afterStart)))
	}
	countsAfter := callCounts(d)
	for _, k := range slices.Sorted(maps.Keys(countsAfter)) {
		want := countsBefore[k]
		// What a start itself does: the viewer lookup and the local token request
		// (`gh auth token` reaches no origin).
		if k == "viewer" || k == "auth_token" {
			want++
		}
		if countsAfter[k] != want {
			t.Errorf("gh calls of kind %s = %d after the restart, want %d (before the restart: %d)", k, countsAfter[k], want, countsBefore[k])
		}
	}

	// Nothing was appended for content that did not change, nothing is pending,
	// the entity's version did not move, and the consumer's key is still at the
	// tail.
	if got := d.ChangeLog(t); len(got) != 0 {
		t.Errorf("the restart appended %+v for unchanged content", got)
	}
	if got := d.PendingRows(t); len(got) != 0 {
		t.Errorf("pending rows after the restart: %+v", got)
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got != versionBefore {
		t.Errorf("entity version = %d after the restart, want %d", got, versionBefore)
	}
	if after := pollPR(t, d, "restart-reader", servetest.QueryMine); len(after.Changes) != 0 {
		t.Errorf("the consumer's poll after the restart answered %+v", after.Changes)
	}
}
