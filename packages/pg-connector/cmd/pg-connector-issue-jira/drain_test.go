package main

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// SIGTERM drains the daemon: the listener closes, the requests in flight finish,
// the background work stops, every pending settle row is appended to the change
// log (so no detected change is lost), and the process exits 0. These tests run
// the real daemon child and hold its pjira calls with the shim.

// TestDrainSIGTERMFinishesTheRequestInFlightAndExitsZero stops the daemon while a
// read waits on the origin: the read still gets its answer and the exit is 0.
func TestDrainSIGTERMFinishesTheRequestInFlightAndExitsZero(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{Hooks: "poll_interval=50ms"})
	d.Shim.SetIssue(servetest.Open("EXA-5"))
	t.Cleanup(d.Shim.Release)

	// A read of an issue the daemon does not hold goes to the origin by a key
	// search; hold that search.
	d.Shim.Hold(`key in (`)
	type answer struct {
		resp scriptout.Response
		err  error
	}
	got := make(chan answer, 1)
	go func() {
		resp, err := d.Call(servetest.Op("show", `{"id":"EXA-5"}`), 60*time.Second)
		got <- answer{resp, err}
	}()
	d.Shim.WaitHeld(t, 30*time.Second)

	d.Term(t)
	// The daemon is draining, not gone: it waits for the request in flight.
	select {
	case a := <-got:
		t.Fatalf("the request answered (%+v, %v) while its origin call was still held", a.resp, a.err)
	case <-time.After(500 * time.Millisecond):
	}
	if ended, code := d.Exited(); ended {
		t.Fatalf("the daemon exited (%d) with a request still in flight; output: %s", code, d.Stderr())
	}

	d.Shim.Release()
	select {
	case a := <-got:
		if a.err != nil || a.resp.Error != nil {
			t.Fatalf("the request in flight was not finished by the drain: %v %+v", a.err, a.resp.Error)
		}
		var iss schema.Issue
		if err := json.Unmarshal(a.resp.Result, &iss); err != nil || iss.ID != "EXA-5" {
			t.Errorf("the answer = %s (%v), want EXA-5", a.resp.Result, err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the request in flight never answered")
	}
	if code := d.Wait(t, 30*time.Second); code != 0 {
		t.Errorf("exit = %d, want 0; output: %s", code, d.Stderr())
	}
	if !servetest.HasChange(d.DaemonLog(t), "drain_done") {
		t.Errorf("the daemon log has no drain_done line: %v", d.DaemonLog(t))
	}
}

// TestDrainAppendsEverySettleRowAndASecondStartFindsThemDurable detects two
// changes whose detail refresh is held, so both wait pending at SIGTERM; the
// drain appends both rows, exit 0, and a second start finds them in the log
// where a consumer that polled before polls them.
func TestDrainAppendsEverySettleRowAndASecondStartFindsThemDurable(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{
		Hooks: "poll_interval=50ms",
		// A long settle timeout: the rows end only by the drain.
		Config: map[string]any{"settle_timeout": "10m"},
		Before: func(d *servetest.Daemon) {
			d.Shim.SetIssues(servetest.MineJQL, confA, confB)
		},
	})
	t.Cleanup(d.Shim.Release)
	if resp := d.MustCall(t, servetest.Op("list", `{"query":"`+servetest.QueryMine+`"}`), 20*time.Second); resp.Error != nil {
		t.Fatalf("warm the mine query: %+v", resp.Error)
	}
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)
	const consumer = "drain"
	invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil)).wantResult(t, "tail poll", schema.IssueSchemaVersion)
	d.Shim.WaitQuiet(t, 300*time.Millisecond, 30*time.Second)

	// The detail group's fetches wait; the summaries commit and detect the
	// changes, which then wait for the detail group to settle.
	d.Shim.Hold(`key = "`)
	d.Shim.SetIssue(confA.WithStatus("In Progress"))
	d.Shim.SetIssue(confB.WithStatus("Done"))
	for _, id := range []string{"EXA-1", "EXA-2"} {
		invoke(t, b, `{"op":"invalidate","args":{"id":"`+id+`"}}`).wantResult(t, "invalidate "+id, schema.IssueSchemaVersion)
	}
	db := d.OpenStore(t)
	until(t, 30*time.Second, "both changes pending", func() bool {
		var n int
		return db.QueryRow(`SELECT COUNT(*) FROM pending_change`).Scan(&n) == nil && n == 2
	})
	var logged int
	if err := db.QueryRow(`SELECT COUNT(*) FROM change_log`).Scan(&logged); err != nil || logged != 0 {
		t.Fatalf("before the drain the log has %d rows (%v), want none: the rows are pending", logged, err)
	}

	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d, want 0; output: %s", code, d.Stderr())
	}
	d.Shim.Release()
	ids := changeLogIDs(t, d)
	if !slices.Equal(ids, []string{"EXA-1", "EXA-2"}) {
		t.Errorf("after the drain the change log holds %v, want both pending rows", ids)
	}
	var pending int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM pending_change`).Scan(&pending); err != nil || pending != 0 {
		t.Errorf("after the drain %d rows are still pending (%v)", pending, err)
	}

	// A second start finds the rows durable: the consumer that polled before the
	// changes receives both.
	d.Start(t)
	r := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil))
	r.wantResult(t, "poll after the restart", schema.IssueSchemaVersion)
	var seen []string
	for _, row := range confChanges(r.confDoc(t)) {
		ent, _ := row["entity"].(map[string]any)
		seen = append(seen, ent["id"].(string))
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"EXA-1", "EXA-2"}) {
		t.Errorf("the restarted daemon delivered %v, want both rows the drain appended", seen)
	}
}

// changeLogIDs is the ids of the change log's rows, sorted.
func changeLogIDs(t *testing.T, d *servetest.Daemon) []string {
	t.Helper()
	rows, err := d.OpenStore(t).Query(`SELECT id FROM change_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// until polls cond every 10 ms until it holds or the timeout passes.
func until(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
