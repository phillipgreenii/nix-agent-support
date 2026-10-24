package serve_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The tracking rules of a PR as the assembled daemon shows them, with the real
// binary over the fake gh: a PR that was only read is tracked and delivers
// nothing, and a read with the watch option marks it watched.

// seedUnqueried is seed3a plus prW, a PR the daemon can answer for but that no
// configured query lists, so only a direct read ever shows it to the daemon.
func seedUnqueried(d *servetest.Daemon) {
	seed3a(d)
	d.Shim.SetPRs("author:nobody", prW)
	// The daemon resolves a reference it has never seen with the identity query.
	d.Shim.Set("resolve.example_org__widget__21.json", `{"nameWithOwner":"example-org/widget","pullRequest":{"id":"`+prW.NodeID+`","number":21}}`)
}

type entityFlags struct {
	exists  bool
	watched bool
	via     string
}

func readEntity(t *testing.T, db *sql.DB, node string) entityFlags {
	t.Helper()
	var (
		watched sql.NullInt64
		via     sql.NullString
	)
	err := db.QueryRow(`SELECT watched_at, watched_via FROM entity WHERE kind = 'pr' AND id = ?`, node).Scan(&watched, &via)
	if err == sql.ErrNoRows {
		return entityFlags{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return entityFlags{exists: true, watched: watched.Valid && watched.Int64 != 0, via: via.String}
}

func TestAPRThatWasOnlyReadIsTrackedAndDeliversNoChangeRow(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seedUnqueried})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref()})

	db := d.OpenStore(t)
	if got := readEntity(t, db, prW.NodeID); !got.exists || got.watched {
		t.Fatalf("a PR that was only read must be tracked and not watched, got %+v", got)
	}
	// It changes at the origin and is read again: it is refreshed (tracked) and
	// no consumer is handed a row for it.
	d.Shim.SetPRs("author:nobody", servetest.PR{NodeID: prW.NodeID, Repo: prW.Repo, Number: prW.Number, Head: sha40("b"), Branch: prW.Branch})
	shown := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref(), "fresh": true}))
	if shown.HeadSHA != sha40("b") {
		t.Fatalf("the refreshed read must see the new head, got %q", shown.HeadSHA)
	}
	for _, q := range []string{servetest.QueryMine, servetest.QueryTeam} {
		res := pollPR(t, d, "only-read-"+q, q)
		for _, c := range res.Changes {
			if c.Entity.ID == prW.Ref() {
				t.Errorf("query %s delivered a change row for a PR that was only read: %+v", q, c)
			}
		}
	}
}

func TestAWatchReadMarksAPRWatchedWhetherTheStoreHasSeenItOrNot(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seedUnqueried})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	db := d.OpenStore(t)
	rowsBefore := countRows(t, d, "change_log")

	// prW: never seen by the daemon. prA: known (a query member), not watched.
	if got := readEntity(t, db, prW.NodeID); got.exists {
		t.Fatalf("control: prW must be unknown to the store, got %+v", got)
	}
	if got := readEntity(t, db, prA.NodeID); !got.exists || got.watched {
		t.Fatalf("control: prA must be known and unwatched, got %+v", got)
	}

	shown := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref(), "watch": true}))
	if shown.Repo != prW.Repo || shown.Number != prW.Number {
		t.Fatalf("a watch read still answers the ordinary show: %+v", shown)
	}
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref(), "watch": true})

	for _, p := range []servetest.PR{prW, prA} {
		if got := readEntity(t, db, p.NodeID); !got.watched || got.via != "direct" {
			t.Errorf("%s after a watch read = %+v, want watched via direct", p.Ref(), got)
		}
	}
	// A read without the option changes nothing: a watched PR stays watched.
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref()})
	if got := readEntity(t, db, prW.NodeID); !got.watched {
		t.Errorf("a read without the option cleared the watch: %+v", got)
	}
	// The registration still emits for every tracked PR until the flip, so a watch
	// read adds no row of its own.
	if rowsAfter := countRows(t, d, "change_log"); rowsAfter != rowsBefore {
		t.Errorf("the watch reads appended %d rows under the transitional emission", rowsAfter-rowsBefore)
	}

	// A malformed option is refused and watches nothing.
	callErr(t, d, "pr", "show", map[string]any{"id": prB.Ref(), "watch": "yes"}, 30*time.Second)
	if got := readEntity(t, db, prB.NodeID); got.watched {
		t.Errorf("a refused read watched the PR: %+v", got)
	}
}
