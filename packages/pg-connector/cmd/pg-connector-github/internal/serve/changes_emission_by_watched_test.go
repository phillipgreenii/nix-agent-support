package serve_test

import (
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Emission by the watched flag on the real GitHub assembly: a configuration whose
// queries carry watched = true appends rows for the arrival and the departure the
// daemon sees and answers them, while the same configuration without the flag
// tracks the same PRs and appends and answers nothing.

type scenarioOutcome struct {
	// Answers are the changes the member poll delivered: change word, entity id
	// and kinds, ordered by entity id.
	Answers []string
	// Rows are the change log rows without their timestamps and snapshot.
	Rows []string
	// Watched is how many entities carry the watched flag at the end.
	Watched int
}

// runScenario runs the brand-new-member and confirmed-departure scenario
// over a daemon whose two queries are defined by queries.
func runScenario(t *testing.T, queries map[string]any, wantRows bool) scenarioOutcome {
	t.Helper()
	d := servetest.NewDaemon(t, servetest.Options{
		Before: seed3a,
		Config: map[string]any{
			"pr":        map[string]any{"queries": queries},
			"query_ttl": map[string]any{servetest.QueryMine: "1s", servetest.QueryTeam: "1h"},
		},
	})
	p := runInProcess(t, d, nil)
	defer p.stop(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	pollPR(t, d, "reader", servetest.QueryMine) // the key starts at the tail

	prD := servetest.PR{NodeID: "PR_node_d", Repo: "example-org/gadget", Number: 15}
	prCMerged := prC
	prCMerged.State = "MERGED"
	d.Shim.SetPRs("is:open label:unused-fixture", prCMerged)
	d.Shim.SetPRs(servetest.MineExpr, prA, prB, prD)

	var res schema.PRChangesResult
	if wantRows {
		until(t, 60*time.Second, "the arrival and the departure to be delivered", func() bool {
			res = pollPR(t, d, "reader", servetest.QueryMine)
			return len(res.Changes) >= 2
		})
	} else {
		// Nothing will be delivered, so wait until the daemon has tracked the arrival.
		until(t, 60*time.Second, "the arrival to be tracked", func() bool {
			var n int
			err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM entity WHERE kind = 'pr' AND id = ?`, prD.NodeID).Scan(&n)
			return err == nil && n == 1
		})
		time.Sleep(3 * time.Second) // a few query runs (mine re-runs every second) after the arrival
		res = pollPR(t, d, "reader", servetest.QueryMine)
	}

	var out scenarioOutcome
	for _, c := range res.Changes {
		kinds := slices.Clone(c.Kinds)
		sort.Strings(kinds)
		out.Answers = append(out.Answers, fmt.Sprint(c.Change, " ", c.Entity.ID, " ", kinds))
	}
	sort.Strings(out.Answers)

	db := d.OpenStore(t)
	rows, err := db.Query(`SELECT kind, id, version, head_sha, kinds, fields, origin FROM change_log ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, id, head, kinds, fields, origin string
		var version int64
		if err := rows.Scan(&kind, &id, &version, &head, &kinds, &fields, &origin); err != nil {
			t.Fatal(err)
		}
		out.Rows = append(out.Rows, fmt.Sprint(kind, "|", id, "|", version, "|", head, "|", kinds, "|", fields, "|", origin))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM entity WHERE watched_at IS NOT NULL`).Scan(&out.Watched); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEmissionByWatched_OnlyWatchedQueriesAppendRowsForTheirArrivalsAndDepartures(t *testing.T) {
	plain := runScenario(t, map[string]any{
		servetest.QueryMine: servetest.MineExpr,
		servetest.QueryTeam: servetest.TeamExpr,
	}, false)
	flagged := runScenario(t, map[string]any{
		servetest.QueryMine: map[string]any{"expr": servetest.MineExpr, "watched": true},
		servetest.QueryTeam: map[string]any{"expr": servetest.TeamExpr, "watched": true},
	}, true)

	if len(plain.Rows) != 0 || len(plain.Answers) != 0 {
		t.Errorf("unwatched queries appended rows %v and answered %v, want neither", plain.Rows, plain.Answers)
	}
	if plain.Watched != 0 {
		t.Errorf("%d entities are watched without the flag, want none", plain.Watched)
	}
	if len(flagged.Rows) == 0 || len(flagged.Answers) < 2 {
		t.Errorf("watched queries appended rows %v and answered %v, want the arrival and the departure", flagged.Rows, flagged.Answers)
	}
	if flagged.Watched == 0 {
		t.Error("no entity is watched with the flag: the runner did not mark the members of the watched queries")
	}
}
