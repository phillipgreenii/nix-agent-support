package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/budget"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/eventlog"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/report"
	"github.com/phillipgreenii/pg-router-handlers/internal/usage"
)

// Lease owner and generation (D-8, S-12, S-13): the session lease carries the
// dispatch id of its owner and a generation that a takeover increments; the
// superseded dispatch notices the higher generation on its next refresh and
// stops without a terminal action.

// fenceClock is a clock tests move forward by hand.
type fenceClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fenceClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func fenceRun(t *testing.T, cc ccpool.Runner, now time.Time) *ccpoolRun {
	t.Helper()
	clk := &fenceClock{t: now}
	deps := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, leaseCfg()).deps
	deps.CC = cc
	deps.Now = clk.Now
	return &ccpoolRun{deps: deps, ownerID: "disp-new"}
}

var fenceNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// A new session is stamped with its first owner: the dispatch id, generation 1
// and one supervisor, in the launch metadata; with the lease off nothing is.
func TestLeaseOwner_launchStampsOwnerGenerationOne(t *testing.T) {
	for _, ttl := range []time.Duration{leaseTTL, 0} {
		cfg := leaseCfg()
		cfg.LeaseTTL = ttl
		clk := &dtest.ManualClock{T: fenceNow}
		bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
		cc := newSettleCC(leaseSess, func() ccpool.SessionState {
			if bd.Idx["zr-w"] >= 2 {
				return ccpool.StateIdle
			}
			return ccpool.StateWorking
		})
		deps := leaseDeps(t, cc, bd, cfg, clk)
		d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}, DispatchID: "disp-1"}
		if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
			t.Fatalf("ttl=%v dispatch: %v", ttl, err)
		}
		m := cc.EnsuredMeta
		if ttl == 0 {
			for _, k := range []string{ccpool.MetaKeyLeaseOwner, ccpool.MetaKeyLeaseGeneration, ccpool.MetaKeySupervisors} {
				if _, ok := m[k]; ok {
					t.Errorf("lease off: %s must not be stamped: %v", k, m)
				}
			}
			continue
		}
		if m[ccpool.MetaKeyLeaseOwner] != "disp-1" || m[ccpool.MetaKeyLeaseGeneration] != "1" || m[ccpool.MetaKeySupervisors] != "1" {
			t.Errorf("launch meta = %v, want owner disp-1, generation 1, supervisors 1", m)
		}
		// The refresh never rewrites the owner or generation.
		for _, c := range cc.SetMetaCalls() {
			if c.Key != ccpool.MetaKeyLeaseUntil {
				t.Errorf("a refresh wrote %+v; only lease_until may be written", c)
			}
		}
	}
}

// The takeover increments the generation (absent or unparsable reads as 0),
// records the owner, and computes supervisors from whether the previous lease
// was still fresh at the takeover instant.
func TestTakeOverLease_generationOwnerAndSupervisors(t *testing.T) {
	future := fenceNow.Add(time.Minute).Format(time.RFC3339)
	past := fenceNow.Add(-time.Minute).Format(time.RFC3339)
	cases := []struct {
		name           string
		meta           map[string]string
		wantGen, wantS string
	}{
		{"no generation, no supervisors, expired lease", map[string]string{ccpool.MetaKeyLeaseUntil: past}, "1", "1"},
		{"unparsable generation reads as 0", map[string]string{ccpool.MetaKeyLeaseGeneration: "x", ccpool.MetaKeyLeaseUntil: past}, "1", "1"},
		{"generation 3 becomes 4", map[string]string{ccpool.MetaKeyLeaseGeneration: "3", ccpool.MetaKeyLeaseUntil: past}, "4", "1"},
		{"fresh lease, supervisors absent reads as 1", map[string]string{ccpool.MetaKeyLeaseUntil: future}, "1", "2"},
		{"fresh lease, supervisors 2 becomes 3", map[string]string{ccpool.MetaKeyLeaseGeneration: "2", ccpool.MetaKeySupervisors: "2", ccpool.MetaKeyLeaseUntil: future}, "3", "3"},
		{"fresh lease, unparsable supervisors reads as 1", map[string]string{ccpool.MetaKeySupervisors: "many", ccpool.MetaKeyLeaseUntil: future}, "1", "2"},
		{"expired lease resets supervisors", map[string]string{ccpool.MetaKeySupervisors: "5", ccpool.MetaKeyLeaseUntil: past}, "1", "1"},
		{"absent expiry is not fresh", map[string]string{ccpool.MetaKeySupervisors: "5"}, "1", "1"},
		{"unparsable expiry is not fresh", map[string]string{ccpool.MetaKeySupervisors: "5", ccpool.MetaKeyLeaseUntil: "soon"}, "1", "1"},
		{"expiry equal to the takeover instant is not fresh", map[string]string{ccpool.MetaKeySupervisors: "5", ccpool.MetaKeyLeaseUntil: fenceNow.Format(time.RFC3339)}, "1", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := &dtest.FakeCC{}
			r := fenceRun(t, cc, fenceNow)
			row := ccpool.Session{ExternalID: "att-old", Meta: tc.meta}
			s, err := r.takeOverLease(context.Background(), row)
			if err != nil {
				t.Fatalf("takeOverLease: %v", err)
			}
			if got := cc.MetaOf("att-old"); got[ccpool.MetaKeyLeaseOwner] != "disp-new" ||
				got[ccpool.MetaKeyLeaseGeneration] != tc.wantGen || got[ccpool.MetaKeySupervisors] != tc.wantS {
				t.Errorf("meta = %v, want owner disp-new, generation %s, supervisors %s", got, tc.wantGen, tc.wantS)
			}
			if strconv.Itoa(s) != tc.wantS {
				t.Errorf("returned supervisors = %d, want %s", s, tc.wantS)
			}
			if g := r.generation.Load(); strconv.FormatInt(g, 10) != tc.wantGen {
				t.Errorf("owned generation = %d, want %s", g, tc.wantGen)
			}
			// The generation is written last, after its owner is visible.
			calls := cc.SetMetaCalls()
			if len(calls) != 3 || calls[2].Key != ccpool.MetaKeyLeaseGeneration {
				t.Errorf("write order = %+v, want owner, supervisors, then generation", calls)
			}
		})
	}
}

// A takeover whose write fails aborts the absorption: it declines, owning nothing.
func TestTakeOverLease_writeFailureAbortsAbsorption(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{turnRow()}}, SetMetaErr: errors.New("meta set: disk I/O error")}
	e := newExec(cc, &dtest.ScriptBD{}, leaseCfg())
	e.ownerID = "disp-new"
	e.deps.LockDir = t.TempDir()
	if _, ok, err := e.takeOverForAbsorb(context.Background(), turnRow(), absorbEvent); err == nil || ok {
		t.Fatalf("a failed takeover write must abort the absorption: ok=%v err=%v", ok, err)
	}
	if g := e.generation.Load(); g != 0 {
		t.Errorf("a failed takeover must leave no owned generation, got %d", g)
	}
}

// With the lease off a takeover writes nothing.
func TestTakeOverLease_leaseOffWritesNothing(t *testing.T) {
	cc := &dtest.FakeCC{}
	r := fenceRun(t, cc, fenceNow)
	r.deps.Cfg.LeaseTTL = 0
	if s, err := r.takeOverLease(context.Background(), ccpool.Session{ExternalID: "att-old"}); err != nil || s != 0 {
		t.Fatalf("lease off: supervisors=%d err=%v", s, err)
	}
	if n := len(cc.SetMetaCalls()); n != 0 {
		t.Errorf("lease off: %d writes", n)
	}
}

// A refresh that reads a generation above its own supersedes the dispatch and
// writes nothing; an equal or lower one refreshes; a read that fails decides
// nothing (the expiry is still written).
func TestLeaseRefresh_supersededOnHigherGenerationOnly(t *testing.T) {
	cases := []struct {
		name           string
		seen           string
		getErr         error
		wantSuperseded bool
		wantWrite      bool
	}{
		{"higher generation", "3", nil, true, false},
		{"equal generation", "2", nil, false, true},
		{"lower generation", "1", nil, false, true},
		{"absent generation reads as 0", "", nil, false, true},
		{"unparsable generation reads as 0", "x", nil, false, true},
		{"unreadable metadata decides nothing", "9", errors.New("ccpool meta list: boom"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := &dtest.FakeCC{}
			if tc.seen != "" {
				_ = cc.SetMeta(context.Background(), "att-old", ccpool.MetaKeyLeaseGeneration, tc.seen)
			}
			cc.GetMetaErr = tc.getErr
			before := len(cc.SetMetaCalls())
			r := fenceRun(t, cc, fenceNow)
			r.generation.Store(2)
			var cause error
			r.cancelDispatch = func(err error) { cause = err }
			(&leaseRefresher{r: r, name: "att-old"}).refresh(context.Background())
			if got := r.superseded.Load(); got != tc.wantSuperseded {
				t.Errorf("superseded = %v, want %v", got, tc.wantSuperseded)
			}
			if tc.wantSuperseded && !errors.Is(cause, ErrSuperseded) {
				t.Errorf("cancel cause = %v, want ErrSuperseded", cause)
			}
			if !tc.wantSuperseded && cause != nil {
				t.Errorf("must not cancel, got cause %v", cause)
			}
			writes := len(cc.SetMetaCalls()) - before
			if (writes == 1) != tc.wantWrite || writes > 1 {
				t.Errorf("expiry writes = %d, want written=%v", writes, tc.wantWrite)
			}
		})
	}
}

// liveRowCC lists the one session a dispatch launched, with the metadata the
// fake holds for it, so a second dispatch of the same event sees what a real
// pool would show it.
type liveRowCC struct {
	*dtest.FakeCC
	session, cwd string
}

func (c *liveRowCC) rows(ctx context.Context) []ccpool.Session {
	m, _ := c.GetMeta(ctx, c.session)
	if len(m) == 0 {
		return nil
	}
	return []ccpool.Session{{
		ExternalID: c.session, Name: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking,
		CWD: c.cwd, Meta: m,
	}}
}

func (c *liveRowCC) List(ctx context.Context) ([]ccpool.Session, error) { return c.rows(ctx), nil }

func (c *liveRowCC) ListLight(ctx context.Context) ([]ccpool.Session, error) { return c.rows(ctx), nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// The live double-dispatch check: two dispatches of the same event in one
// process against the fake pool. The second absorbs the first's live session and
// takes the lease over at the next generation; the first notices on its next
// refresh and ends superseded, with no session close, no worktree removal, no
// reminder paste and no failure report, and records a superseded outcome.
func TestLeaseFence_secondDispatchSupersedesFirst(t *testing.T) {
	cfg := leaseCfg()
	cfg.MaxWait = time.Hour
	cfg.WatchdogPoll = 2 * time.Millisecond
	clk := &fenceClock{t: fenceNow}
	fake := &dtest.FakeCC{}
	cc := &liveRowCC{FakeCC: fake, session: "att-a", cwd: t.TempDir()}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	opener := &dtest.NoopGitOpener{}

	mkDeps := func(ext string) Deps {
		deps := leaseDeps(t, cc, bd, cfg, &dtest.ManualClock{T: fenceNow})
		deps.ExternalID = ext
		deps.Now = clk.Now
		deps.Tick = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Millisecond):
				return nil
			}
		}
		deps.GitOpener = opener.Open
		deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{}}}
		deps.FirstTurn = func(string) bool { return true }
		return deps
	}

	// Dispatch A launches the session under a time budget, so its watchdog runs.
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	depsA := mkDeps("att-a")
	depsA.Log = lw
	roleA := workerRole(cfg)
	roleA.CCPool.Budget = budget.Budget{
		Time:       25 * time.Minute,
		Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
	}
	type outcome struct {
		res report.Result
		err error
	}
	doneA := make(chan outcome, 1)
	go func() {
		res, err := (ccpoolExecutor{}).Dispatch(context.Background(),
			DispatchContext{Role: roleA, Item: item.Item{ID: "zr-w"}, EventID: absorbEvent, DispatchID: "disp-a"}, depsA)
		doneA <- outcome{res, err}
	}()
	waitFor(t, "dispatch A to hold the lease", func() bool {
		m := fake.MetaOf("att-a")
		_, refreshed := m[ccpool.MetaKeyLeaseUntil]
		return refreshed && m[ccpool.MetaKeyLeaseOwner] == "disp-a" && len(fake.SetMetaCalls()) > 0
	})
	if m := fake.MetaOf("att-a"); m[ccpool.MetaKeyLeaseGeneration] != "1" || m[ccpool.MetaKeySupervisors] != "1" {
		t.Fatalf("first owner meta = %v, want generation 1 and supervisors 1", m)
	}

	// Dispatch B redelivers the same event and absorbs the live session.
	depsB := mkDeps("att-b")
	depsB.LockDir = t.TempDir()
	ctxB, cancelB := context.WithCancel(context.Background())
	doneB := make(chan outcome, 1)
	go func() {
		res, err := (ccpoolExecutor{}).Dispatch(ctxB,
			DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}, EventID: absorbEvent, DispatchID: "disp-b"}, depsB)
		doneB <- outcome{res, err}
	}()

	var a outcome
	select {
	case a = <-doneA:
	case <-time.After(10 * time.Second):
		cancelB()
		t.Fatal("dispatch A did not stop after being superseded")
	}
	if !errors.Is(a.err, ErrSuperseded) {
		t.Fatalf("A err = %v, want ErrSuperseded", a.err)
	}
	if len(a.res.Actions) != 0 {
		t.Errorf("A reported a failure action: %+v", a.res)
	}
	m := fake.MetaOf("att-a")
	if m[ccpool.MetaKeyLeaseGeneration] != "2" || m[ccpool.MetaKeyLeaseOwner] != "disp-b" || m[ccpool.MetaKeySupervisors] != "2" {
		t.Errorf("after the takeover meta = %v, want owner disp-b, generation 2, supervisors 2", m)
	}
	// No terminal action by the superseded dispatch: no close, no worktree removal.
	if got := fake.Closed; len(got) != 0 {
		t.Errorf("a session was closed: %v", got)
	}
	for _, c := range opener.WTM.Calls {
		if c[0] == "remove" {
			t.Errorf("the superseded dispatch removed a worktree: %v", c)
		}
	}
	// No reminder paste: push the clock past the budget's reminder level, give a
	// still-running watchdog many polls to act, and expect only the first nudge.
	clk.advance(24 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if len(fake.Sent) != 1 {
		t.Errorf("sends = %v, want only the launch nudge (no reminder paste)", fake.SentText)
	}
	cancelB()
	<-doneB

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"outcome":"superseded"`) || strings.Contains(string(raw), "dispatch_result") {
		t.Errorf("A's end record must carry outcome superseded and no failure record:\n%s", raw)
	}
}
