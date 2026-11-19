package hygiene

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
)

// INV-CCH-41, design D2: progress counters. The worktree sweep runs against real
// git in a throwaway repository (the sweepHarness); the rest uses the passes'
// own fakes.

func TestProgress_closedBeadCounters(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-w-zr-done", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-done"}},
		{ExternalID: "pg-router-w-zr-open", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-open"}},
		{ExternalID: "pg-router-w-zr-busy", State: ccpool.StateWorking, Meta: map[string]string{ccpool.MetaKeyBead: "zr-busy"}},
		{ExternalID: "someone-else", State: ccpool.StateIdle},
	}}}
	br := dtest.FakeBR{Out: map[string]string{
		"show zr-done --json": `{"status":"closed"}`,
		"show zr-open --json": `{"status":"open"}`,
	}}
	c := &passCounts{Pass: passClosedBead}
	ctx := withCounts(context.Background(), c)
	n := reconcileClosedBeadSessions(ctx, cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", "", nil)
	c.finish(n)
	if c.Candidates != 2 || c.Examined != 2 || c.Removed != 1 || c.Kept != 1 || c.Unexamined != 0 {
		t.Errorf("closed-bead counters = %+v, want 2 candidates, 2 examined, 1 removed, 1 kept", c)
	}
}

// The janitor_run record carries every role's counters and the progress gauges
// are published.
func TestProgress_janitorRecordsAndPublishesCounters(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""})
	var pts []gaugePoint
	rec := &recorder{}
	p := rec.passes()
	p.emit = spyEmit(&pts)
	p.orphan = func(ctx context.Context, _ roles.Role, _ config.Config, _ executor.Deps) int {
		c := countsFrom(ctx)
		c.seen(3)
		c.examined()
		c.examined()
		return 1
	}
	if code, _ := f.run(p); code != 0 {
		t.Fatalf("exit %d", code)
	}
	ev := f.events()
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	rows, _ := ev[0]["counters"].([]any)
	var wt map[string]any
	for _, r := range rows {
		if m, _ := r.(map[string]any); m["pass"] == passOrphan {
			wt = m
		}
	}
	if wt == nil || wt["role"] != "a" || wt["candidates"] != 3.0 || wt["examined"] != 2.0 || wt["removed"] != 1.0 || wt["kept"] != 1.0 || wt["unexamined"] != 1.0 {
		t.Fatalf("orphan_reconcile counters = %v in %v", wt, rows)
	}
	for _, g := range []struct {
		name string
		want float64
	}{{gaugeJanitorCandidates, 3}, {gaugeJanitorExamined, 2}, {gaugeJanitorCached, 0}, {gaugeJanitorKept, 1}, {gaugeJanitorUnexamined, 1}} {
		if p, ok := find(pts, g.name, passOrphan); !ok || p.value != g.want {
			t.Errorf("%s = %+v ok=%v, want %v", g.name, p, ok, g.want)
		}
	}
}
