//go:build integration

package serve_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The GitHub registration after the flip, as the real daemon binary shows it
// over the fake gh: rows are emitted for watched PRs only and the feed delivers
// by the watched flag, so a consumer polls without a query and a query is only a
// filter. The shared engine and feed own the proofs of each rule (the hoist's
// tests); these tests pin them on the registration the daemon runs.

// watchedFlipConfig has the mine query watched and the team query not, and
// re-runs mine every second so an origin change shows up quickly.
func watchedFlipConfig() map[string]any {
	return map[string]any{
		"pr": map[string]any{"queries": map[string]any{
			servetest.QueryMine: map[string]any{"expr": servetest.MineExpr, "watched": true},
			servetest.QueryTeam: servetest.TeamExpr,
		}},
		"query_ttl": map[string]any{servetest.QueryMine: "1s", servetest.QueryTeam: "1h"},
	}
}

// pollUnscoped is the pr feed's poll with no query, which must succeed.
func pollUnscoped(t *testing.T, d *servetest.Daemon, consumer string) schema.PRChangesResult {
	t.Helper()
	args := map[string]any{"consumer": consumer}
	resp := d.MustCall(t, "pr", req("changes", args), 30*time.Second)
	if resp.Error != nil {
		t.Fatalf("pr changes %s without a query: %s: %s", consumer, resp.Error.Code, resp.Error.Message)
	}
	return decode[schema.PRChangesResult](t, resp.Result)
}

// changeWords lists "<change> <entity id>" of the rows, sorted.
func changeWords(res schema.PRChangesResult) []string {
	var out []string
	for _, c := range res.Changes {
		out = append(out, c.Change+" "+c.Entity.ID)
	}
	slices.Sort(out)
	return out
}

func TestWatchedFlip_TheFeedDeliversWatchedRowsOnlyWithoutAQueryAndTheFlagIsStickyAcrossARestart(t *testing.T) {
	prD := servetest.PR{NodeID: "PR_node_d", Repo: "example-org/gadget", Number: 15}
	d := servetest.NewDaemon(t, servetest.Options{Before: seedUnqueried, Config: watchedFlipConfig()})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	db := d.OpenStore(t)

	// A query's first complete run appends no rows and marks its members watched;
	// a member of an unwatched query only is tracked.
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Fatalf("the first complete runs appended %d rows, want none", n)
	}
	for _, p := range []servetest.PR{prA, prB, prC} {
		if got := readEntity(t, db, p.NodeID); !got.watched || got.via != "query:"+servetest.QueryMine {
			t.Errorf("%s after the first run = %+v, want watched via the mine query", p.Ref(), got)
		}
	}

	// A PR that was only read is tracked, not watched.
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref()})
	if got := readEntity(t, db, prW.NodeID); !got.exists || got.watched {
		t.Fatalf("a PR that was only read = %+v, want tracked and not watched", got)
	}

	// Both consumers start at the tail: the unscoped one needs no query.
	if res := pollUnscoped(t, d, "unscoped-reader"); len(res.Changes) != 0 {
		t.Fatalf("the first unscoped poll delivered %v, want none", changeWords(res))
	}
	pollPR(t, d, "team-reader", servetest.QueryTeam)

	// prD arrives in mine and prC leaves it (its node says MERGED for the
	// confirming read).
	prCMerged := prC
	prCMerged.State = "MERGED"
	d.Shim.SetPRs("is:open label:unused-fixture", prCMerged)
	d.Shim.SetPRs(servetest.MineExpr, prA, prB, prD)

	var res schema.PRChangesResult
	until(t, 60*time.Second, "the arrival and the departure to be delivered to the unscoped consumer", func() bool {
		res = pollUnscoped(t, d, "unscoped-reader")
		return len(res.Changes) >= 2
	})
	// prC's own state change (it is MERGED now) is delivered with its departure.
	want := []string{"added " + prD.Ref(), "changed " + prC.Ref(), "removed " + prC.Ref()}
	if got := changeWords(res); !slices.Equal(got, want) {
		t.Errorf("unscoped changes = %v, want %v (and nothing for the PR that was only read)", got, want)
	}

	// The query still filters: the team query lists neither PR, so its consumer
	// is told nothing of them.
	if team := pollPR(t, d, "team-reader", servetest.QueryTeam); len(team.Changes) != 0 {
		t.Errorf("the team query delivered %v, want none", changeWords(team))
	}

	// The flag stays after a PR leaves its query, and nothing watched prW.
	check := func(when string) {
		t.Helper()
		db := d.OpenStore(t)
		for _, p := range []servetest.PR{prA, prB, prC, prD} {
			if got := readEntity(t, db, p.NodeID); !got.watched {
				t.Errorf("%s: %s = %+v, want still watched", when, p.Ref(), got)
			}
		}
		if got := readEntity(t, db, prW.NodeID); got.watched {
			t.Errorf("%s: the PR that was only read is watched: %+v", when, got)
		}
	}
	check("after the departure")

	// The flag survives a restart of the real process, and so does the cursor.
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	d.Start(t)
	check("after the restart")
	again := pollUnscoped(t, d, "unscoped-reader")
	for _, c := range again.Changes {
		if c.Entity.ID == prW.Ref() {
			t.Errorf("after the restart the feed delivered a row for the PR that was only read: %+v", c)
		}
	}
}

func TestWatchedFlip_AMovedAsideStoreKeepsNoFlagsUntilTheQueriesRerun(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seedUnqueried, Config: watchedFlipConfig()})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine)
	// prW is watched by a read: no query will ever flag it again.
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref(), "watch": true})
	if got := readEntity(t, d.OpenStore(t), prW.NodeID); !got.watched || got.via != "direct" {
		t.Fatalf("control: prW = %+v, want watched via direct", got)
	}
	pollUnscoped(t, d, "unscoped-reader")

	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	// Replace the store with a file that is not a database, which the daemon
	// moves aside and starts empty over.
	sidecars, _ := filepath.Glob(d.StorePath() + "-*")
	for _, f := range sidecars {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(d.StorePath(), []byte(strings.Repeat("this is not a sqlite database; ", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine)

	// The flags come back only where a watched query lists the PR again.
	db := d.OpenStore(t)
	for _, p := range []servetest.PR{prA, prB, prC} {
		until(t, 30*time.Second, p.Ref()+" to be watched again by the re-run query", func() bool {
			return readEntity(t, db, p.NodeID).watched
		})
	}
	if got := readEntity(t, db, prW.NodeID); got.watched {
		t.Errorf("the PR watched by a read kept its flag across a moved-aside store: %+v", got)
	}
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Errorf("the refill appended %d rows, want none: a first complete run appends nothing", n)
	}

	// A cursor of the old store is expired; a reset without a query recovers it
	// and replays the watched PRs.
	resp := d.MustCall(t, "pr", req("changes", map[string]any{"consumer": "unscoped-reader"}), 30*time.Second)
	if resp.Error == nil || !strings.HasPrefix(resp.Error.Message, "cursor_expired:") {
		t.Fatalf("the first poll after a moved-aside store = %+v, want cursor_expired:", resp.Error)
	}
	resp = d.MustCall(t, "pr", req("changes", map[string]any{"consumer": "unscoped-reader", "reset": true}), 30*time.Second)
	if resp.Error != nil {
		t.Fatalf("the reset poll must succeed without a query: %+v", resp.Error)
	}
	replay := changeWords(decode[schema.PRChangesResult](t, resp.Result))
	want := []string{"added " + prA.Ref(), "added " + prB.Ref(), "added " + prC.Ref()}
	slices.Sort(want)
	if !slices.Equal(replay, want) {
		t.Errorf("the unscoped reset replays %v, want the watched members %v", replay, want)
	}
}
