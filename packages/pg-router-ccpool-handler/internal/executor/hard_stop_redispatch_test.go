package executor

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
)

// Budget hard stop, then the next dispatch of the same bead, event and head.
//
// The contract: a terminal budget hard stop leaves the bead open, and the next
// dispatch of it for the same event at the same head (review-source re-emits the
// same event id and head after a stop) launches a NEW ccpool session. It never
// re-attaches to the stopped row (absorbDuplicate): that row's budget runs from
// its original launch time, so the watchdog hard-stops it at once, over and over
// (the 272-hit loop recorded on pg2-aezsm; fixed by pg2-vwb4c, pg2-afre3,
// pg2-tc9c3 and pg2-92rfu).
//
// The two halves were only ever tested apart: the stop by one Dispatch
// (TestDispatch_watchdogHardStop_*, TestTerminal_*), the redispatch against a
// closed row pre-seeded into the fake
// (TestDispatch_sameEventSameHead_afterHandback_launchesFreshSession, ...). The
// test below chains them (bead pg2-bx363) through one store, so the row the
// second Dispatch finds is the row the hard stop actually wrote, and a refactor
// of either half cannot silently reopen the loop.

// storeCC is a ccpool.Runner backed by an in-memory row store: what one Dispatch
// writes (Ensure, SetMeta, Close) is what the next Dispatch reads back through
// List, as with ccpool's own store. The other fakes script List (dtest.FakeCC's
// ListSeq) or hold one row (settleCC), so a test must pre-seed the row a second
// dispatch finds; a chain test must not, or it only re-tests its own seeding.
//
// An open row reads working. A closed row reads not-live, idle and
// handler-closed: the settled shape pg2-aezsm recorded for the rows that were
// re-attached. The embedded dtest.FakeCC still records Ensured, Sent, Closed and
// ClosedPurge; ops adds the per-session call order the redispatch must leave
// untouched.
type storeCC struct {
	*dtest.FakeCC

	mu   sync.Mutex
	rows []*ccpool.Session
	ops  map[string][]string // per session id: "ensure", "send", "cancel", "meta:<key>", "close", "purge", in call order
}

func newStoreCC() *storeCC {
	return &storeCC{FakeCC: &dtest.FakeCC{}, ops: map[string][]string{}}
}

// row returns the stored row id, or nil. The caller holds mu.
func (c *storeCC) row(id string) *ccpool.Session {
	for _, r := range c.rows {
		if r.ExternalID == id {
			return r
		}
	}
	return nil
}

func (c *storeCC) Ensure(ctx context.Context, id, name, cwd string, env, meta map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops[id] = append(c.ops[id], "ensure")
	c.rows = append(c.rows, &ccpool.Session{
		ExternalID: id, Name: name, CWD: cwd, Live: true, State: ccpool.StateWorking, Meta: maps.Clone(meta),
	})
	return c.FakeCC.Ensure(ctx, id, name, cwd, env, meta)
}

func (c *storeCC) Send(ctx context.Context, id, text string, mode ccpool.SendMode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops[id] = append(c.ops[id], "send")
	return c.FakeCC.Send(ctx, id, text, mode)
}

func (c *storeCC) Cancel(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops[id] = append(c.ops[id], "cancel")
	return nil
}

func (c *storeCC) SetMeta(ctx context.Context, id, key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops[id] = append(c.ops[id], "meta:"+key)
	if r := c.row(id); r != nil {
		if r.Meta == nil {
			r.Meta = map[string]string{}
		}
		r.Meta[key] = value
	}
	return c.FakeCC.SetMeta(ctx, id, key, value)
}

func (c *storeCC) Close(ctx context.Context, id string, purge bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if purge {
		c.ops[id] = append(c.ops[id], "purge")
		c.rows = slices.DeleteFunc(c.rows, func(r *ccpool.Session) bool { return r.ExternalID == id })
	} else {
		c.ops[id] = append(c.ops[id], "close")
		if r := c.row(id); r != nil {
			r.Live, r.CloseReason = false, "handler"
		}
	}
	return c.FakeCC.Close(ctx, id, purge)
}

func (c *storeCC) List(context.Context) ([]ccpool.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ccpool.Session, 0, len(c.rows))
	for _, r := range c.rows {
		s := *r
		s.Meta = maps.Clone(r.Meta)
		if !s.Live {
			s.State = ccpool.StateIdle
		}
		out = append(out, s)
	}
	return out, nil
}

// session returns a copy of the stored row id as List reports it.
func (c *storeCC) session(id string) (ccpool.Session, bool) {
	rows, _ := c.List(context.Background())
	for _, s := range rows {
		if s.ExternalID == id {
			return s, true
		}
	}
	return ccpool.Session{}, false
}

// opsOn returns everything the dispatches did to session id, in call order.
func (c *storeCC) opsOn(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ops[id])
}

// TestDispatch_budgetHardStop_thenRedispatchLaunchesFreshSession: Dispatch 1 runs a
// review session into the watchdog's budget hard stop (the terminal path, on a row
// that settles idle once closed); Dispatch 2 then carries the SAME bead, event id
// and head. It must launch a new session, leaving the stopped row exactly as the
// hard stop left it: not re-attached (no lease refresh, no wait on it), not sent
// input, not closed again. The second dispatch starts five hours after the first
// launch, under a 25-minute time budget, so re-attaching would hard-stop it
// instantly from the old row's launch time. Removing the incomplete marker the
// hard stop stamps (watchdog.terminal) makes this fail.
func TestDispatch_budgetHardStop_thenRedispatchLaunchesFreshSession(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite cap so the usage ramp trips it; the 25m time budget stays on
	cfg.WorktreeDir = t.TempDir()
	role := reviewRole(cfg)
	d := DispatchContext{
		Role: role, EventID: "review.ready:zr-c",
		Item: item.Item{ID: "zr-c", Metadata: map[string]any{"repo": "o/r", "pr_number": "7", "head_sha": "h1"}},
	}
	cc := newStoreCC()
	launched1 := time.Unix(0, 0)
	launched2 := launched1.Add(5 * time.Hour)

	dispatch := func(ext string, now time.Time, bd *dtest.ScriptBD, snap usage.Snapshot) (report.Result, error) {
		clk := &dtest.ManualClock{T: now}
		origTick := clk.TickAdvancing()
		deps := newExecCC(cc, bd, cfg).deps
		deps.ExternalID = ext
		deps.Now = clk.Now
		deps.Tick = func(ctx context.Context, dur time.Duration) error { // same wall-clock pacing as TestDispatch_watchdogHardStop_unclaimed
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(dur):
			}
			return origTick(ctx, dur)
		}
		deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{snap}}
		deps.Git = &dtest.NoopGit{}
		deps.GitOpener = (&dtest.NoopGitOpener{}).Open
		return ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	}

	// Dispatch 1: the session works (bead in_progress) until the watchdog's first
	// reading is over its token cap.
	bd1 := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	res1, err1 := dispatch("sess-1", launched1, bd1, usage.Snapshot{OutputTokens: 2000}) // immediately >100%
	if !errors.Is(err1, watchdog.ErrBudgetExceeded) {
		t.Fatalf("precondition: the first dispatch must end in the watchdog's budget hard stop, got %v", err1)
	}
	if v := verbOf(res1); v != report.Unclaimed {
		t.Errorf("a budget hard stop unclaims its bead, got verb %q", v)
	}
	stopped, ok := cc.session("sess-1")
	if !ok {
		t.Fatal("precondition: the hard-stopped session's row must still be in the store")
	}
	if stopped.Live || stopped.State != ccpool.StateIdle || stopped.CloseReason != "handler" {
		t.Fatalf("precondition: the stopped row must be idle and handler-closed, the settled shape that re-attached; live=%v state=%s close_reason=%q",
			stopped.Live, stopped.State, stopped.CloseReason)
	}
	if got := stopped.Meta[ccpool.MetaKeyLaunchedAt]; got != ccpool.FormatMetaTime(launched1) {
		t.Fatalf("precondition: the stopped row must carry its original launch time %s, got %q", ccpool.FormatMetaTime(launched1), got)
	}
	if stopped.Meta[ccpool.MetaKeyIncomplete] == "" {
		t.Errorf("the hard stop must stamp the row incomplete (its bead is open again); meta=%v", stopped.Meta)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "sess-1" || cc.ClosedPurge[0] {
		t.Errorf("the hard stop must close its session exactly once, non-purge; Closed=%v ClosedPurge=%v", cc.Closed, cc.ClosedPurge)
	}
	stoppedOps := cc.opsOn("sess-1") // the lease ticker stopped with Dispatch 1, so this is final

	// Dispatch 2: the same bead, event id and head, a fresh attempt id, long after
	// the first launch. The fresh session claims the bead and closes it.
	bd2 := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	res2, err2 := dispatch("sess-2", launched2, bd2, usage.Snapshot{})
	if errors.Is(err2, watchdog.ErrBudgetExceeded) {
		t.Fatalf("the re-request re-attached to the stopped row and hard-stopped on its original launch time: %v", err2)
	}
	if err2 != nil || verbOf(res2) != "" {
		t.Fatalf("the re-request must run its fresh session to a normal completion; res=%v err=%v", res2, err2)
	}

	if want := []string{"sess-1", "sess-2"}; !slices.Equal(cc.Ensured, want) {
		t.Errorf("a same-event same-head re-request after a hard stop must launch a NEW session; Ensured=%v want %v", cc.Ensured, want)
	}
	for key, want := range map[string]string{ccpool.MetaKeyEventID: d.EventID, ccpool.MetaKeyHeadSHA: "h1"} {
		if got := cc.EnsuredMeta[key]; got != want {
			t.Errorf("the fresh session must be stamped with the same %s %q, got %q", key, want, got)
		}
	}
	if at, ok := ccpool.ParseMetaTime(cc.EnsuredMeta[ccpool.MetaKeyLaunchedAt]); !ok || at.Before(launched2) {
		t.Errorf("the fresh session must start its own budget at its own launch (%s), got launched_at=%q",
			ccpool.FormatMetaTime(launched2), cc.EnsuredMeta[ccpool.MetaKeyLaunchedAt])
	}
	if want := []string{"sess-1", "sess-2"}; !slices.Equal(cc.Sent, want) {
		t.Errorf("each session gets exactly its own launch nudge, the stopped row no input; Sent=%v want %v", cc.Sent, want)
	}
	// sess-2 stays open here: the store reports idle only for a closed row, so its
	// own settle-close is not exercised (that is TestSettledClose_*'s subject).
	if len(cc.Closed) != 1 || cc.Closed[0] != "sess-1" {
		t.Errorf("the stopped row must be closed exactly once, by the hard stop; Closed=%v", cc.Closed)
	}
	if got := cc.opsOn("sess-1"); !slices.Equal(got, stoppedOps) {
		t.Errorf("the stopped row must be left exactly as the hard stop left it (no lease refresh, send, cancel or close);\nafter the hard stop: %v\nafter the re-request: %v", stoppedOps, got)
	}
}
