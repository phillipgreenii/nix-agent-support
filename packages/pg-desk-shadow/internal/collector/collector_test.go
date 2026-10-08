package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(_ context.Context, until time.Time) error {
	if until.After(c.t) {
		c.t = until
	}
	return nil
}

// fakeExec stands in for the sandboxed pg-desk call.
type fakeExec struct {
	clk   *clock
	calls int
	// next programs the call: it may mutate the store and returns the result.
	next func(n int) (runner.Result, error)
	// advance is how long the call takes on the fake clock.
	advance time.Duration
	argv    [][]string
}

func (f *fakeExec) Run(_ context.Context, _ time.Duration, argv ...string) (runner.Result, error) {
	f.calls++
	f.argv = append(f.argv, argv)
	f.clk.t = f.clk.t.Add(f.advance)
	return f.next(f.calls)
}

type env struct {
	l   scratch.Layout
	m   scratch.Manifest
	db  sqlite.DB
	clk *clock
	ex  *fakeExec
	cfg Config
}

func envelope(from, to int64, sources string, records ...string) string {
	return fmt.Sprintf(`{"contract":"pg-desk.changes/v1","type":"pr","consumer":"shadow-compare","cursor":{"from":%d,"to":%d},"sources":%s,"records":[%s]}`, from, to, sources, strings.Join(records, ","))
}

func record(seq int64, id, kind, origin, at string) string {
	return fmt.Sprintf(`{"seq":%d,"type":"pr","id":%q,"title":"t","version":1,"kinds":[%q],"origin":%q,"at":%q}`, seq, id, kind, origin, at)
}

func okSources() string { return `[{"query":"mine","status":"ok"},{"query":"team","status":"ok"}]` }

func newEnv(t *testing.T) *env {
	t.Helper()
	testutil.RequireSQLite(t)
	root := t.TempDir()
	l := scratch.Layout{Root: root}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir()
	m := scratch.Manifest{
		Phase: "A", CreatedAt: "2026-01-05T09:00:00Z", Seeded: true, BDMode: "passthrough", Consumer: "shadow-compare", MaxPerPoll: 50,
		Builds: map[string]string{"pg-desk": "pg-desk version 0.0.0-test"}, Scrub: []string{"acme/api", "someone"},
		Live: scratch.LiveSources{RouterEvents: filepath.Join(live, "events.jsonl"), RouterQueue: filepath.Join(live, "queue.jsonl"), RunRecord: filepath.Join(live, "run-record.log"), ConnectorEvents: filepath.Join(live, "conn.jsonl")},
	}
	db := testutil.FakeStore(t, l.StoreDB())
	ctx := context.Background()
	if err := db.Exec(ctx, fmt.Sprintf(`INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, version, hydrated_at, active, list_fp) VALUES
 ('acme/api','pr','acme/api#1',%s,'x',1,'2026-01-05T09:00:00Z',1,'fp1'),
 ('acme/api','pr','acme/api#2',%s,'x',1,'2026-01-05T09:00:00Z',1,'fp2');
INSERT INTO consumer (name, type, cursor) VALUES ('shadow-compare','pr',0);`, sqlite.Quote(testutil.FactsJSON("aaa", "OPEN", 1)), sqlite.Quote(testutil.FactsJSON("bbb", "OPEN", 2)))); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 1, 5, 9, 59, 30, 0, time.UTC)}
	ex := &fakeExec{clk: clk, advance: 10 * time.Second}
	cfg := Config{
		Layout: l, Manifest: m, Exec: ex, Period: time.Minute, TickTimeout: time.Minute, KillPointsPerHour: 1500, MaxFailedTicks: 10,
		FloorMargin: 200, WarmupExclusion: true, Now: clk.now, Sleep: clk.sleep,
	}
	return &env{l: l, m: m, db: db, clk: clk, ex: ex, cfg: cfg}
}

func (e *env) collector(t *testing.T) *Collector {
	t.Helper()
	e.cfg.Manifest = e.m
	c, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) rows(t *testing.T) []schema.Row {
	t.Helper()
	rows, err := schema.ReadAll(e.l.TicksFile())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (e *env) ticks(t *testing.T) []schema.Row {
	var out []schema.Row
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindTick {
			out = append(out, r)
		}
	}
	return out
}

func (e *env) hydrate(n int) {
	if err := e.db.Exec(context.Background(), fmt.Sprintf("INSERT INTO meta (key,value) VALUES ('change_flow.hydrations.pr','%d') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+%d", n, n)); err != nil {
		panic(err)
	}
}

func TestTickRecordsItemsHydrationsCostAndChangedFields(t *testing.T) {
	e := newEnv(t)
	e.ex.next = func(n int) (runner.Result, error) {
		e.hydrate(1)
		// The hydration changes the entity's head.
		if err := e.db.Exec(context.Background(), fmt.Sprintf("UPDATE entity SET facts=%s WHERE entity_id='acme/api#1'", sqlite.Quote(testutil.FactsJSON("ccc", "OPEN", 1)))); err != nil {
			t.Fatal(err)
		}
		// The scratch connector log gains one row inside the tick window.
		row := fmt.Sprintf(`{"time":%q,"op":"show","graphql_cost":7,"graphql_remaining":3900,"graphql_reset_at":%q}`+"\n", e.clk.t.Add(-2*time.Second).Format(time.RFC3339Nano), e.clk.t.Add(time.Hour).Format(time.RFC3339))
		if err := os.WriteFile(e.l.ConnectorLog(), []byte(row), 0o600); err != nil {
			t.Fatal(err)
		}
		return runner.Result{Stdout: envelope(0, 1, okSources(), record(1, "acme/api#1", "head_changed", "pg-connector", "2026-01-05T10:00:05Z"))}, nil
	}
	c := e.collector(t)
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Baseline projection first (an earlier tick), so the diff is meaningful.
	prev, _ := ReadProjection(context.Background(), e.db)
	c.prev = Projection{"acme/api#1": prev["acme/api#1"], "acme/api#2": prev["acme/api#2"]}
	c.prev["acme/api#1"] = map[string]string{}
	for k, v := range prev["acme/api#1"] {
		c.prev["acme/api#1"][k] = v
	}
	c.prev["acme/api#1"]["head_sha"] = "aaa"
	ran, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	if err != nil || !ran {
		t.Fatalf("tick: %v %v", ran, err)
	}
	ts := e.ticks(t)
	if len(ts) != 1 {
		t.Fatalf("%d tick rows", len(ts))
	}
	r := ts[0]
	if r.Status != schema.StatusOK || r.ExitCode == nil || *r.ExitCode != 0 || r.Hydrations != 1 || r.GraphQLCostSum != 7 {
		t.Errorf("row = %+v", r)
	}
	if len(r.Items) != 1 || r.Items[0].Origin != "pg-connector" || r.Items[0].Kinds[0] != "head_changed" || strings.Join(r.Items[0].Fields, ",") != "head_sha" || r.Items[0].UpdatedAt == "" {
		t.Errorf("items = %+v", r.Items)
	}
	if !r.Warmup {
		t.Error("the first tick is inside the warm-up window")
	}
	// tick_start precedes tick, bracketing the call.
	var kinds []string
	for _, row := range e.rows(t) {
		kinds = append(kinds, row.Kind)
	}
	if strings.Join(kinds, ",") != "tick_start,tick" {
		t.Errorf("row kinds = %v", kinds)
	}
	// Argv is the direct pg-desk call with the shadow consumer and --json.
	want := []string{filepath.Join(e.l.BinDir(), "pg-desk"), "pr", "changes", "--consumer", "shadow-compare", "--json"}
	if strings.Join(e.ex.argv[0], " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v", e.ex.argv[0])
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name       string
		res        runner.Result
		err        error
		wantStatus string
		wantExit   int
	}{
		{"exit 2 partial keeps the records", runner.Result{Exit: 2, Stdout: envelope(0, 1, `[{"query":"mine","status":"degraded","reason":"hydration_budget; list acme/api#1 failed"}]`, record(1, "acme/api#1", "reconcile", "pg-connector", "2026-01-05T10:00:05Z"))}, nil, schema.StatusPartial, 2},
		{"exit 3 is a failed tick with an envelope", runner.Result{Exit: 3, Stdout: envelope(4, 4, `[{"query":"mine","status":"failed","reason":"boom"}]`)}, nil, schema.StatusFailed, 3},
		{"exit 1 has no envelope", runner.Result{Exit: 1, Stderr: "changes: store acme/api is old-schema\nmore"}, nil, schema.StatusFailed, 1},
		{"a deadline is a failed tick", runner.Result{}, fmt.Errorf("deadline: context deadline exceeded"), schema.StatusFailed, -1},
		{"garbage output is a failed tick", runner.Result{Stdout: "not json"}, nil, schema.StatusFailed, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.ex.next = func(int) (runner.Result, error) { return c.res, c.err }
			col := e.collector(t)
			if _, err := col.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			r := e.ticks(t)[0]
			if r.Status != c.wantStatus || r.ExitCode == nil || *r.ExitCode != c.wantExit {
				t.Errorf("status %q exit %v, want %q %d (row %+v)", r.Status, r.ExitCode, c.wantStatus, c.wantExit, r)
			}
			if strings.Contains(r.Error, "acme/api") {
				t.Errorf("error text leaks the slug: %q", r.Error)
			}
			for _, src := range r.Sources {
				if strings.Contains(src.Reason, "acme/api") {
					t.Errorf("source reason leaks: %q", src.Reason)
				}
			}
			if c.wantExit == 2 && (len(r.Items) != 1 || len(r.Sources) != 1) {
				t.Errorf("exit 2 must keep records and sources: %+v", r)
			}
		})
	}
}

func TestWarmupWindowEndsAfterTwoConsecutiveZeroHydrationTicks(t *testing.T) {
	e := newEnv(t)
	hyd := []int{3, 0, 1, 0, 0, 0}
	e.ex.next = func(n int) (runner.Result, error) {
		e.hydrate(hyd[n-1])
		return runner.Result{Stdout: envelope(0, 0, okSources())}, nil
	}
	c := e.collector(t)
	slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	for i := range hyd {
		if _, err := c.Tick(context.Background(), slot.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var got []bool
	for _, r := range e.ticks(t) {
		got = append(got, r.Warmup)
	}
	want := []bool{true, true, true, true, true, false}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("warm-up flags = %v, want %v (a hydration resets the streak; the window ends after two consecutive zero ticks)", got, want)
	}
}

func TestPhaseBHasNoWarmupExclusion(t *testing.T) {
	e := newEnv(t)
	e.cfg.WarmupExclusion = false
	e.ex.next = func(int) (runner.Result, error) {
		e.hydrate(40)
		return runner.Result{Stdout: envelope(0, 0, okSources())}, nil
	}
	c := e.collector(t)
	if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if e.ticks(t)[0].Warmup {
		t.Error("phase B ticks are the measurement and must not be excluded")
	}
}

func TestSlotsAreAlignedAndTicksNeverOverlap(t *testing.T) {
	e := newEnv(t)
	e.ex.next = func(int) (runner.Result, error) { return runner.Result{Stdout: envelope(0, 0, okSources())}, nil }
	e.cfg.MaxTicks = 4
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ts := e.ticks(t)
	if len(ts) != 4 {
		t.Fatalf("%d ticks", len(ts))
	}
	var prevEnd time.Time
	for _, r := range ts {
		slot, _ := schema.Time(r.Slot)
		if slot.Second() != 0 || slot.Nanosecond() != 0 {
			t.Errorf("slot %s is not aligned to the minute", r.Slot)
		}
		start, _ := schema.Time(r.TickStart)
		end, _ := schema.Time(r.TickEnd)
		if start.Before(slot) {
			t.Errorf("tick started before its slot")
		}
		if !prevEnd.IsZero() && start.Before(prevEnd) {
			t.Errorf("ticks overlap: start %s before the previous end %s", start, prevEnd)
		}
		prevEnd = end
	}
	if e.ex.calls != 4 {
		t.Errorf("%d pg-desk calls", e.ex.calls)
	}
}

func TestOverrunSkipsMissedSlotsAndLogsAGap(t *testing.T) {
	e := newEnv(t)
	e.ex.advance = 75 * time.Second // longer than a slot
	e.ex.next = func(int) (runner.Result, error) { return runner.Result{Stdout: envelope(0, 0, okSources())}, nil }
	e.cfg.MaxTicks = 3
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var gaps []schema.Row
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindGap {
			gaps = append(gaps, r)
		}
	}
	if len(gaps) == 0 || gaps[0].Reason != schema.GapOverrun {
		t.Fatalf("expected an overrun gap, got %+v", gaps)
	}
	ts := e.ticks(t)
	s0, _ := schema.Time(ts[0].Slot)
	s1, _ := schema.Time(ts[1].Slot)
	if s1.Sub(s0) != 2*time.Minute {
		t.Errorf("the missed slot must be skipped, not caught up: %s -> %s", s0, s1)
	}
}

func TestLateWakeRecordsASleepGapAndRealigns(t *testing.T) {
	e := newEnv(t)
	e.ex.next = func(int) (runner.Result, error) { return runner.Result{Stdout: envelope(0, 0, okSources())}, nil }
	woke := false
	e.cfg.Sleep = func(ctx context.Context, until time.Time) error {
		if !woke && e.ex.calls == 1 {
			woke = true
			e.clk.t = until.Add(3 * time.Hour) // the lid was closed
			return nil
		}
		return e.clk.sleep(ctx, until)
	}
	e.cfg.MaxTicks = 2
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var sleepGaps int
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindGap && r.Reason == schema.GapSleep {
			sleepGaps++
		}
	}
	if sleepGaps != 1 {
		t.Errorf("%d sleep gaps, want 1 (no duplicate for the realigned slot)", sleepGaps)
	}
}

func TestBudgetSkipWritesARowAndResumesAfterReset(t *testing.T) {
	e := newEnv(t)
	e.ex.next = func(int) (runner.Result, error) { return runner.Result{Stdout: envelope(0, 0, okSources())}, nil }
	slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	e.clk.t = slot
	reset := slot.Add(2 * time.Minute)
	low := fmt.Sprintf(`{"time":%q,"op":"list","graphql_remaining":1500,"graphql_reset_at":%q}`+"\n", slot.Add(-time.Minute).Format(time.RFC3339Nano), reset.Format(time.RFC3339))
	if err := os.WriteFile(e.m.Live.ConnectorEvents, []byte(low), 0o600); err != nil {
		t.Fatal(err)
	}
	c := e.collector(t)
	ran, err := c.Tick(context.Background(), slot)
	if err != nil || ran {
		t.Fatalf("below the floor the tick must be skipped: ran=%v err=%v", ran, err)
	}
	if e.ex.calls != 0 {
		t.Fatal("pg-desk must not be called when the budget is below the floor")
	}
	r := e.ticks(t)[0]
	if !r.SkippedBudget || r.Status != schema.StatusSkippedBudget || r.Budget == nil || r.Budget.Remaining != 1500 || r.Budget.Floor != 2000 {
		t.Errorf("row = %+v", r)
	}
	// The window resets and nothing new is logged: the guard must not deadlock.
	e.clk.t = reset.Add(10 * time.Second)
	ran, err = c.Tick(context.Background(), NextBoundary(e.clk.t, time.Minute))
	if err != nil || !ran {
		t.Fatalf("after reset+5s the guard must resume: ran=%v err=%v", ran, err)
	}
}

func TestKillCriteria(t *testing.T) {
	t.Run("spend above the ceiling", func(t *testing.T) {
		e := newEnv(t)
		e.cfg.KillPointsPerHour = 100
		e.ex.next = func(int) (runner.Result, error) {
			row := fmt.Sprintf(`{"time":%q,"op":"list","graphql_cost":60}`+"\n", e.clk.t.Add(-time.Second).Format(time.RFC3339Nano))
			f, _ := os.OpenFile(e.l.ConnectorLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			_, _ = f.WriteString(row)
			_ = f.Close()
			return runner.Result{Stdout: envelope(0, 0, okSources())}, nil
		}
		c := e.collector(t)
		slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
		if _, err := c.Tick(context.Background(), slot); err != nil {
			t.Fatalf("60 points is under the ceiling: %v", err)
		}
		if _, err := c.Tick(context.Background(), slot.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "spend") {
			t.Fatalf("120 points in the hour must kill, got %v", err)
		}
	})
	t.Run("consecutive failed ticks", func(t *testing.T) {
		e := newEnv(t)
		e.cfg.MaxFailedTicks = 3
		e.ex.next = func(int) (runner.Result, error) { return runner.Result{Exit: 1, Stderr: "boom"}, nil }
		c := e.collector(t)
		slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
		var err error
		n := 0
		for ; err == nil && n < 10; n++ {
			_, err = c.Tick(context.Background(), slot.Add(time.Duration(n)*time.Minute))
		}
		if err == nil || n != 4 {
			t.Fatalf("must abort on the 4th consecutive failure (more than 3), got n=%d err=%v", n, err)
		}
	})
	t.Run("a sandbox denial", func(t *testing.T) {
		e := newEnv(t)
		e.ex.next = func(int) (runner.Result, error) {
			return runner.Result{Stdout: envelope(0, 0, okSources()), Denied: true}, nil
		}
		c := e.collector(t)
		if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "denial") {
			t.Fatalf("a denial must kill the run, got %v", err)
		}
	})
	t.Run("a rejected write verb", func(t *testing.T) {
		e := newEnv(t)
		e.ex.next = func(int) (runner.Result, error) {
			_ = os.WriteFile(e.l.ShimLog("gh"), []byte(`{"tool":"gh","argv":["pr","merge","1"],"verdict":"reject-write"}`+"\n"), 0o600)
			return runner.Result{Stdout: envelope(0, 0, okSources())}, nil
		}
		c := e.collector(t)
		if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "write verb") {
			t.Fatalf("a rejected write must kill the run, got %v", err)
		}
	})
	t.Run("a safety refusal from the runner", func(t *testing.T) {
		e := newEnv(t)
		e.ex.next = func(int) (runner.Result, error) {
			return runner.Result{}, fmt.Errorf("%w: XDG_STATE_HOME is live", runner.ErrSafety)
		}
		c := e.collector(t)
		if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("a safety refusal must stop the run")
		}
	})
}

func TestCrashAndResumeRecoversFromChangeLogWithTheSameCursor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A previous collector wrote tick_start (cursor 0), pg-desk delivered and
	// advanced the cursor to 2, then the collector died before the tick row.
	if err := schema.Append(e.l.TicksFile(), schema.Row{Kind: schema.KindTickStart, Phase: "A", Slot: "2026-01-05T10:00:00.000Z", TickStart: "2026-01-05T10:00:01.000Z", CursorFrom: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(ctx, `INSERT INTO change_log (repo, entity_type, entity_id, version, kinds, origin, at) VALUES
 ('acme/api','pr','acme/api#1',1,'["head_changed"]','pg-connector','2026-01-05T10:00:05Z'),
 ('acme/api','pr','acme/api#2',1,'["reconcile"]','local-reconcile','2026-01-05T10:00:06Z');
UPDATE consumer SET cursor=2 WHERE name='shadow-compare';`); err != nil {
		t.Fatal(err)
	}
	e.clk.t = time.Date(2026, 1, 5, 10, 5, 0, 0, time.UTC)
	c := e.collector(t)
	if err := c.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	ts := e.ticks(t)
	if len(ts) != 1 || ts[0].Status != schema.StatusRecovered || !ts[0].Recovered || len(ts[0].Items) != 2 || ts[0].Items[0].Origin != "pg-connector" || ts[0].CursorTo != 2 {
		t.Fatalf("recovered row = %+v", ts)
	}
	reasons := map[string]bool{}
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindGap {
			reasons[r.Reason] = true
		}
	}
	if !reasons[schema.GapUnfinish] || !reasons[schema.GapRestart] {
		t.Errorf("gap reasons = %v", reasons)
	}
	// The next tick starts from the SAME cursor (2): no loss, no re-read.
	e.ex.next = func(int) (runner.Result, error) { return runner.Result{Stdout: envelope(2, 2, okSources())}, nil }
	if _, err := c.Tick(ctx, time.Date(2026, 1, 5, 10, 6, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var last schema.Row
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindTickStart {
			last = r
		}
	}
	if last.CursorFrom != 2 {
		t.Errorf("resumed tick_start cursor = %d, want 2", last.CursorFrom)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	e.ex.next = func(int) (runner.Result, error) {
		e.hydrate(1)
		return runner.Result{Stdout: envelope(0, 0, okSources())}, nil
	}
	e.cfg.MaxTicks = 1
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil || st.Executed != 1 || st.LastSlot == "" {
		t.Fatalf("state = %+v %v", st, err)
	}
	c2 := e.collector(t)
	if c2.st.Executed != 1 || c2.st.LastSlot != st.LastSlot {
		t.Errorf("a new collector must load the saved state, got %+v", c2.st)
	}
}

func TestLockRefusesASecondCollector(t *testing.T) {
	e := newEnv(t)
	unlock, err := Lock(e.l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(e.l); err != ErrAlreadyRunning {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	if u2, err := Lock(e.l); err != nil {
		t.Fatalf("after release: %v", err)
	} else {
		u2()
	}
}

func TestScrubRemovesIdentifiers(t *testing.T) {
	in := "hydrate acme/api#12: gh failed for someone at /Users/someone/work/x.go with @octo-handle"
	out := Scrub(in, []string{"someone"})
	for _, bad := range []string{"acme/api", "#12", "someone", "/Users", "@octo"} {
		if strings.Contains(out, bad) {
			t.Errorf("Scrub left %q in %q", bad, out)
		}
	}
	if len(Scrub(strings.Repeat("x", 1000), nil)) > 240 {
		t.Error("Scrub must truncate")
	}
}

func TestNextBoundary(t *testing.T) {
	at := time.Date(2026, 1, 5, 10, 0, 59, 0, time.UTC)
	if got := NextBoundary(at, time.Minute); !got.Equal(time.Date(2026, 1, 5, 10, 1, 0, 0, time.UTC)) {
		t.Errorf("NextBoundary = %s", got)
	}
	exact := time.Date(2026, 1, 5, 10, 1, 0, 0, time.UTC)
	if got := NextBoundary(exact, time.Minute); !got.Equal(exact.Add(time.Minute)) {
		t.Errorf("a boundary instant must advance to the next slot: %s", got)
	}
}
