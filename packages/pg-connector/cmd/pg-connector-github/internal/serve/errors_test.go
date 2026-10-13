package serve_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The error table: one test per row whose behavior exists in Phase 2, each
// driven through the real daemon and the counting fake gh. Every error is in the
// existing seven-value taxonomy; no new code appears. The rows for protocol,
// socket and config live in wire_test.go and store_test.go.

func statusReport(t *testing.T, d *servetest.Daemon) status.Report {
	t.Helper()
	resp := d.MustCall(t, "pr", scriptout.Request{Op: "status"}, 10*time.Second)
	if resp.Error != nil {
		t.Fatalf("status: %+v", resp.Error)
	}
	var r status.Report
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("decode the status report %s: %v", resp.Result, err)
	}
	return r
}

func graphqlBucket(t *testing.T, r status.Report) status.Bucket {
	t.Helper()
	for _, b := range r.Budget {
		if b.Bucket == "graphql" {
			return b
		}
	}
	t.Fatalf("the status report has no graphql bucket: %+v", r.Budget)
	return status.Bucket{}
}

func authStatus(t *testing.T, d *servetest.Daemon) scriptout.AuthStatus {
	t.Helper()
	resp := d.MustCall(t, "pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 10*time.Second)
	if resp.Error != nil {
		t.Fatalf("auth_status: %+v", resp.Error)
	}
	var a scriptout.AuthStatus
	if err := json.Unmarshal(resp.Result, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// lastRequestRow is the newest request row of the event log for op.
func lastRequestRow(t *testing.T, d *servetest.Daemon, op string) map[string]any {
	t.Helper()
	var last map[string]any
	for _, row := range d.EventLog(t) {
		if row["kind"] == "request" && row["op"] == op {
			last = row
		}
	}
	if last == nil {
		t.Fatalf("no request row for op %s in the event log", op)
	}
	return last
}

// ---- GitHub 5xx or timeout -------------------------------------------------------

func TestErrorTable_GitHub5xx_RetriesThreeTimesThenServesStaleInsideExpiry(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed})
	d.Start(t)
	mustList(t, d, servetest.QueryMine, nil)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)

	// A transient failure that clears inside the three attempts is invisible.
	d.Shim.FailNext(2)
	before := d.Shim.CountKind("search")
	res := mustList(t, d, servetest.QueryMine, map[string]any{"fresh": true})
	if got := d.Shim.CountKind("search") - before; got != 3 {
		t.Errorf("a search that failed twice then succeeded made %d calls, want 3", got)
	}
	for _, e := range res.Entities {
		if e.Stale {
			t.Errorf("%s is stale although the third attempt succeeded", e.ID)
		}
	}

	// A failure that outlasts the three attempts leaves the group stale with
	// last_error, and the interactive read gets the stored content, marked stale.
	d.Shim.WaitQuiet(t, 300*time.Millisecond, 30*time.Second)
	d.Shim.Mode("502")
	before = d.Shim.CountKind("search")
	res = mustList(t, d, servetest.QueryMine, map[string]any{"fresh": true})
	if got := d.Shim.CountKind("search") - before; got != 3 {
		t.Errorf("a search that kept failing made %d calls, want exactly the 3 attempts", got)
	}
	if len(res.Entities) != 3 {
		t.Fatalf("want the three stored entities served, got %+v", res.PresentIDs)
	}
	for _, e := range res.Entities {
		if !e.Stale || e.AsOf == "" {
			t.Errorf("%s: stale=%v as_of=%q, want stale with its age", e.ID, e.Stale, e.AsOf)
		}
	}
	row := lastRequestRow(t, d, "list")
	if row["stale"] != true || row["served_from"] != "cache" {
		t.Errorf("the event row must say stale and served_from cache: %v", row)
	}
	db := d.OpenStore(t)
	var qerr string
	if err := db.QueryRow(`SELECT last_error FROM query WHERE key = 'mine'`).Scan(&qerr); err != nil || !strings.Contains(qerr, "502") {
		t.Errorf("the query row's last_error = %q (%v), want the 502", qerr, err)
	}
	var withErr int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entity_group WHERE group_name = 'summary' AND last_error != ''`).Scan(&withErr); err != nil || withErr != 3 {
		t.Errorf("summary groups with last_error = %d (%v), want 3", withErr, err)
	}
}

func TestErrorTable_GitHub5xx_WithNothingStoredTheReadIsUnavailable(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		seed(d)
		d.Shim.Mode("502")
	}})
	d.Start(t)
	resp, _ := list(t, d, servetest.QueryMine, 30*time.Second, nil)
	if resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Fatalf("with nothing stored and GitHub failing, want unavailable, got %+v", resp.Error)
	}
}

func TestErrorTable_GitHubTimeout_TheCallersDeadlineServesStale(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed})
	d.Start(t)
	mustList(t, d, servetest.QueryMine, nil)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)

	// GitHub answers, but after the caller's deadline: the refresh is not
	// cancelled, the answer at the deadline is the stored content.
	d.Shim.Delay(3)
	start := time.Now()
	resp, res := list(t, d, servetest.QueryMine, 1*time.Second, map[string]any{"fresh": true})
	if resp.Error != nil {
		t.Fatalf("an interactive read past GitHub's delay: %+v", resp.Error)
	}
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Errorf("the read took %s with a 1 s deadline", took)
	}
	if len(res.Entities) != 3 {
		t.Errorf("want the stored entities, got %v", res.PresentIDs)
	}
	for _, e := range res.Entities {
		if !e.Stale {
			t.Errorf("%s must be served stale at the deadline", e.ID)
		}
	}
}

// ---- Secondary rate limit ----------------------------------------------------------

func TestErrorTable_SecondaryLimit_PausesTheBackgroundAndServesInteractiveReadsStale(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{
		Hooks:  "poll_interval=200ms",
		Before: seed,
		// The mine query goes stale every 2 s, so a background refresh runs soon.
		Config: map[string]any{"query_ttl": map[string]any{servetest.QueryMine: "2s", servetest.QueryTeam: "1h"}},
	})
	d.Start(t)
	mustList(t, d, servetest.QueryMine, nil)
	d.Shim.WaitQuiet(t, 300*time.Millisecond, 30*time.Second)

	d.Shim.Mode("secondary") // Retry-After: 3600
	until(t, 20*time.Second, "the background refresh to meet the secondary limit and pause the bucket", func() bool {
		return graphqlBucket(t, statusReport(t, d)).Paused
	})
	b := graphqlBucket(t, statusReport(t, d))
	if !strings.Contains(b.PauseReason, "secondary") || b.PausedUntil == nil || time.Until(*b.PausedUntil) < 50*time.Minute {
		t.Errorf("the pause must be the stated Retry-After (an hour): %+v", b)
	}
	if !servetest.HasChange(d.DaemonLog(t), "budget_paused") {
		t.Error("the pause must be logged as budget_paused")
	}
	if r := statusReport(t, d); r.Health != status.Degraded {
		t.Errorf("a paused bucket makes the report degraded, got %s", r.Health)
	}

	// Background work holds: nothing reaches the origin although the query is
	// overdue many times over.
	held := len(d.Shim.OriginCalls())
	time.Sleep(3 * time.Second)
	if got := len(d.Shim.OriginCalls()); got != held {
		t.Errorf("the background made %d origin calls while paused", got-held)
	}

	// An interactive read still goes to the origin (classes 1 and 2 stay
	// admitted), meets the limit, and is served stale; it is not an auth failure.
	before := d.Shim.CountKind("search")
	resp, res := list(t, d, servetest.QueryMine, 20*time.Second, map[string]any{"fresh": true})
	if resp.Error != nil {
		t.Fatalf("an interactive read under a secondary limit: %+v", resp.Error)
	}
	if got := d.Shim.CountKind("search") - before; got != 1 {
		t.Errorf("the interactive search was attempted %d times, want once (a rate limit is not retried)", got)
	}
	for _, e := range res.Entities {
		if !e.Stale {
			t.Errorf("%s must be served stale", e.ID)
		}
	}
	if a := authStatus(t, d); a.State != scriptout.AuthOK {
		t.Errorf("a secondary limit is not an authentication failure: %+v", a)
	}
}

// ---- Primary limit below the reserve ------------------------------------------------

func TestErrorTable_PrimaryLimitBelowTheReserve_StopsEverySpendAndNamesTheReserve(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed})
	d.Start(t)
	mustList(t, d, servetest.QueryMine, nil)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)

	// GitHub now reports 100 points left, under rate_reserve_points (1000). The
	// next response carries the reading.
	d.Shim.Remaining(100)
	mustList(t, d, servetest.QueryMine, map[string]any{"fresh": true})
	d.Shim.WaitQuiet(t, 300*time.Millisecond, 30*time.Second)
	b := graphqlBucket(t, statusReport(t, d))
	if b.Remaining == nil || *b.Remaining != 100 || b.Reserve != 1000 {
		t.Fatalf("the daemon must hold the reading (remaining 100, reserve 1000): %+v", b)
	}

	// Nothing spends below the reserve: a refreshed read is served stale, with no
	// origin call at all.
	held := len(d.Shim.OriginCalls())
	resp, res := list(t, d, servetest.QueryMine, 20*time.Second, map[string]any{"fresh": true})
	if resp.Error != nil {
		t.Fatalf("a read with stored content under the reserve: %+v", resp.Error)
	}
	for _, e := range res.Entities {
		if !e.Stale {
			t.Errorf("%s must be served stale", e.ID)
		}
	}
	// A query nobody stored is unavailable, and the message names the reserve.
	resp, _ = list(t, d, servetest.QueryMine, 20*time.Second, map[string]any{"args": map[string]string{"author": "someone"}})
	if resp.Error == nil || resp.Error.Code != "unavailable" || !strings.Contains(resp.Error.Message, "reserve") {
		t.Errorf("want unavailable naming the reserve, got %+v", resp.Error)
	}
	if got := len(d.Shim.OriginCalls()); got != held {
		t.Errorf("%d origin calls were made below the reserve", got-held)
	}
}

// ---- Auth failure -------------------------------------------------------------------

func TestErrorTable_AuthFailure_FetchesAreUnauthenticatedCachedReadsStayWithTheirAge(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Hooks: "viewer_interval=300ms", Before: seed})
	d.Start(t)
	mustList(t, d, servetest.QueryMine, nil)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second, "search", "nodes")
	if a := authStatus(t, d); a.State != scriptout.AuthOK {
		t.Fatalf("fixture: authenticated before the failure: %+v", a)
	}

	d.Shim.Mode("401")
	until(t, 15*time.Second, "auth_status to report the failure", func() bool {
		return authStatus(t, d).State == scriptout.AuthMissing
	})
	if a := authStatus(t, d); !strings.Contains(a.Detail, "unauthenticated") {
		t.Errorf("auth_status detail = %q, want the unauthenticated reason", a.Detail)
	}
	if r := statusReport(t, d); r.Auth == nil || !r.Auth.Unauthenticated || r.Health != status.Degraded {
		t.Errorf("status must report unauthenticated and degraded: %+v %v", r.Auth, r.Health)
	}

	fetches := func() int { return d.Shim.CountKind("search") + d.Shim.CountKind("nodes") }
	before := fetches()

	// Cached reads are still served, with their age.
	res := mustList(t, d, servetest.QueryMine, nil)
	if len(res.Entities) != 3 || res.Entities[0].AsOf == "" {
		t.Errorf("a cached read must be served with its age: %+v", res.Entities)
	}
	// Even asked to refresh, the stored content is served (stale); the fetch
	// itself answers unauthenticated without calling the origin.
	res = mustList(t, d, servetest.QueryMine, map[string]any{"fresh": true})
	for _, e := range res.Entities {
		if !e.Stale {
			t.Errorf("%s must be served stale while unauthenticated", e.ID)
		}
	}
	resp, _ := list(t, d, servetest.QueryMine, 20*time.Second, map[string]any{"args": map[string]string{"author": "someone"}})
	if resp.Error == nil || resp.Error.Code != "unauthenticated" {
		t.Errorf("a fetch must answer unauthenticated, got %+v", resp.Error)
	}
	if got := fetches(); got != before {
		t.Errorf("%d searches or summary fetches were made while unauthenticated", got-before)
	}

	// The credential works again: the next viewer refresh clears the state.
	d.Shim.Mode("ok")
	until(t, 15*time.Second, "auth_status to recover", func() bool {
		return authStatus(t, d).State == scriptout.AuthOK
	})
}

// ---- Viewer changed ------------------------------------------------------------------

func TestErrorTable_ViewerChanged_FlushesIdentityScopedQueriesAndLogsIt(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Hooks: "viewer_interval=300ms", Before: seed})
	d.Start(t)
	mustList(t, d, servetest.QueryMine, nil)
	mustList(t, d, servetest.QueryTeam, nil)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second, "search", "nodes")

	fetchedAt := func(key string) int64 {
		var at int64
		if err := d.OpenStore(t).QueryRow(`SELECT COALESCE(fetched_at, 0) FROM query WHERE key = ?`, key).Scan(&at); err != nil {
			return 0 // the row is gone (flushed) and not yet re-run
		}
		return at
	}
	mineBefore, teamBefore := fetchedAt(servetest.QueryMine), fetchedAt(servetest.QueryTeam)
	if mineBefore == 0 || teamBefore == 0 {
		t.Fatalf("fixture: both queries stored (mine %d, team %d)", mineBefore, teamBefore)
	}

	d.Shim.Viewer("another-viewer")
	until(t, 20*time.Second, "the viewer_changed log line", func() bool {
		return servetest.HasChange(d.DaemonLog(t), "viewer_changed")
	})
	// The mine query uses @me, so its stored result was flushed and re-run at
	// once for the new identity; the team query does not, and stays as it was.
	until(t, 20*time.Second, "the viewer-scoped query to be re-run", func() bool {
		return fetchedAt(servetest.QueryMine) > mineBefore
	})
	if got := fetchedAt(servetest.QueryTeam); got != teamBefore {
		t.Errorf("the team query does not depend on the viewer and must not be flushed (fetched_at %d -> %d)", teamBefore, got)
	}
	for _, l := range d.DaemonLog(t) {
		if l["change"] == "viewer_changed" && (l["old_login"] != servetest.DefaultViewer || l["new_login"] != "another-viewer") {
			t.Errorf("viewer_changed line = %v", l)
		}
	}
	if r := statusReport(t, d); r.Auth == nil || r.Auth.Viewer != "another-viewer" {
		t.Errorf("status must show the new viewer: %+v", r.Auth)
	}
}
