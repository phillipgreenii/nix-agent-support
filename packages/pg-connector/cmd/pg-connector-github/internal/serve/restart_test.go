package serve_test

import (
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Restart and cold start (ruling 3 as a test): a stop and a start must serve
// everything inside its freshness window with NO origin call, and after a long
// downtime the catch-up must stay inside the budget.

func TestRestartServesEveryFreshReadWithZeroOriginCalls(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed})
	d.Start(t)

	// Fill the store through list calls, and let the daemon's own refreshes of
	// its configured queries and their members finish.
	for _, q := range []string{servetest.QueryMine, servetest.QueryTeam} {
		mustList(t, d, q, nil)
	}
	awaitFirstSightingFetches(t, d, 3)
	filled := d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)
	if d.Shim.CountKind("search") == 0 || d.Shim.CountKind("nodes") == 0 {
		t.Fatalf("fixture: the fill made no origin calls (%d calls)", filled)
	}

	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	// The drain checkpointed the WAL last: the write-ahead log holds nothing.
	if fi, err := os.Stat(d.StorePath() + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("the store's WAL still holds %d bytes after the drain", fi.Size())
	}

	d.Start(t)
	// Start resolves the viewer once; everything after is the daemon serving.
	afterStart := d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)
	if got, want := afterStart, filled+1; got != want {
		t.Fatalf("a restart made %d origin calls, want exactly the viewer lookup (1): calls %v", got-filled, d.Shim.OriginCalls()[filled:])
	}

	for _, q := range []string{servetest.QueryMine, servetest.QueryTeam} {
		res := mustList(t, d, q, nil)
		if len(res.Entities) == 0 {
			t.Errorf("%s: no entities after the restart", q)
		}
		for _, e := range res.Entities {
			if e.Stale {
				t.Errorf("%s: %s served stale, but it is inside its freshness", q, e.ID)
			}
		}
		mustList(t, d, q, map[string]any{"ids_only": true})
	}
	if got := len(d.Shim.OriginCalls()); got != afterStart {
		t.Errorf("reads inside freshness made %d origin calls, want 0: %v", got-afterStart, d.Shim.OriginCalls()[afterStart:])
	}
	// auth_status, status and explain are local too.
	for _, req := range []scriptout.Request{{Op: scriptout.OpAuthStatus}, {Op: "status"}} {
		if resp := d.MustCall(t, "pr", req, 10*time.Second); resp.Error != nil {
			t.Errorf("%s: %+v", req.Op, resp.Error)
		}
	}
	if got := len(d.Shim.OriginCalls()); got != afterStart {
		t.Errorf("the local ops made %d origin calls", got-afterStart)
	}
}

func TestRestartAfterALongDowntimeStaysInsideTheBudget(t *testing.T) {
	// One summary and one detail per call (batch_size 1) so the demand is many
	// small calls, and a generous cap for the fill.
	d := servetest.NewDaemon(t, servetest.Options{
		Before: seed3a,
		Config: map[string]any{"batch_size": 1, "rate_reserve_points": 0},
	})
	d.Start(t)
	filled := settle(t, d)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	// A long downtime: every stored due time is in the past, so the queue
	// rebuilt at start holds every query, every summary and every background
	// group as overdue.
	past := time.Now().Add(-48 * time.Hour).UnixMilli()
	db := d.WritableStore(t)
	for _, stmt := range []string{`UPDATE entity_group SET fresh_until = ?`, `UPDATE query SET fresh_until = ?`} {
		if _, err := db.Exec(stmt, past); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	// GraphQL calls cost one point each; the runs and jobs requests are REST.
	graphqlCalls := func(calls []servetest.Call) int {
		n := 0
		for _, c := range calls {
			switch c.Kind() {
			case "runs", "jobs", "rerun", "create_review":
			default:
				n++
			}
		}
		return n
	}
	filledGraphQL := graphqlCalls(d.Shim.OriginCalls())
	spentBefore := graphqlSpent(t, d)
	if spentBefore != int64(filledGraphQL) {
		t.Fatalf("fixture: the budget table holds %d points, the shim saw %d GraphQL calls (1 point each)", spentBefore, filledGraphQL)
	}
	jobRowsBefore := countRows(t, d, "job")
	if jobRowsBefore == 0 {
		t.Fatal("fixture: the failed run's jobs were not stored")
	}
	// The demand of the catch-up: the two queries, three summaries, three details
	// and three conversation reads of three pages each, one point per call.
	const demand = 2 + 3 + 3 + 3*3
	const headroom = 6
	cap := int(spentBefore) + headroom

	// Restart under a cap that leaves only a little headroom. The hour's spend is
	// replayed from the budget table, so the catch-up can use only what is left.
	d.Rewrite(map[string]any{"batch_size": 1, "rate_reserve_points": 0, "graphql_points_per_hour": cap})
	d.Start(t)
	d.Shim.WaitQuiet(t, 1500*time.Millisecond, 30*time.Second, workKinds...)
	total := len(d.Shim.OriginCalls())

	newGraphQL := graphqlCalls(d.Shim.OriginCalls()[filled:])
	t.Logf("catch-up: %d calls in the fill, %d new GraphQL calls (demand %d plus the viewer lookup), cap %d", filled, newGraphQL, demand, cap)
	if newGraphQL >= demand+1 { // +1 for the viewer lookup
		t.Errorf("the catch-up made %d GraphQL calls for a demand of %d: the cap did not limit it", newGraphQL, demand+1)
	}
	if spent := graphqlSpent(t, d); spent > int64(cap) {
		t.Errorf("the hour's spend is %d points, over the cap %d", spent, cap)
	} else if want := int64(filledGraphQL + newGraphQL); spent != want {
		t.Errorf("budget table says %d points, the shim saw %d GraphQL calls (after the fill: %v)", spent, want, kinds(d.Shim.OriginCalls()[filled:]))
		rows, _ := d.OpenStore(t).Query(`SELECT seq, bucket, spent, purpose, remaining FROM budget ORDER BY seq`)
		for rows.Next() {
			var seq, spent, rem int64
			var bucket, purpose string
			_ = rows.Scan(&seq, &bucket, &spent, &purpose, &rem)
			t.Logf("budget row %d %s spent=%d purpose=%q remaining=%d", seq, bucket, spent, purpose, rem)
		}
	}
	// The job table survived the restart, and a failed run's jobs are not fetched
	// again: the runs requests of the catch-up reuse the cached rows.
	if got := countRows(t, d, "job"); got != jobRowsBefore {
		t.Errorf("job rows = %d after the restart, want the %d from before", got, jobRowsBefore)
	}
	if got := d.Shim.CountKind("jobs"); got != 1 {
		t.Errorf("jobs were fetched %d times in all, want once", got)
	}
	// The cap held: the catch-up stopped on its own and nothing more comes.
	time.Sleep(2500 * time.Millisecond)
	if got := len(d.Shim.OriginCalls()); got != total {
		t.Errorf("the daemon kept calling after the cap: %d more calls: %v", got-total, kinds(d.Shim.OriginCalls()[total:]))
	}
	// Reads are still answered (stale content inside expiry), not refused.
	if resp, _ := list(t, d, servetest.QueryMine, 20*time.Second, nil); resp.Error != nil {
		t.Errorf("a read after the catch-up: %+v", resp.Error)
	}
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()})
}

// countRows is the number of rows of a store table.
func countRows(t *testing.T, d *servetest.Daemon, table string) int {
	t.Helper()
	var n int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// graphqlSpent is the sum of the GraphQL points in the budget table over the
// last hour (read while the daemon runs or after it stopped).
func graphqlSpent(t *testing.T, d *servetest.Daemon) int64 {
	t.Helper()
	db := d.OpenStore(t)
	var n int64
	since := time.Now().Add(-time.Hour).UnixMilli()
	if err := db.QueryRow(`SELECT COALESCE(SUM(spent), 0) FROM budget WHERE bucket = 'graphql' AND at >= ?`, since).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
