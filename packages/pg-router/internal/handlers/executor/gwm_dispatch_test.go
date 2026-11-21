package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"
	"github.com/phillipgreenii/pg-router/internal/handlers/eventlog"
	"github.com/phillipgreenii/pg-router/internal/handlers/gwm"
	"github.com/phillipgreenii/pg-router/internal/handlers/item"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// These tests answer pg-gwm with cmdruntest fakes behind the real gwm.CLI, so
// the argv the handler sends is asserted exactly.

const (
	gwmRoot = "/canonical"
	gwmSess = "pg-router-worker-bd-1"
)

const gwmLeaseJSON = `{"id":"w-1","lease_id":"l-1","worktree_path":"/wt/worker-bd-1","branch":"pg-router/bd-1","name":"worker/bd-1","head":"abc123","renewed":false}`

func gwmByVerb(verb string) func(cmdrun.Call) bool {
	return func(c cmdrun.Call) bool { return c.Name == "pg-gwm" && len(c.Args) > 0 && c.Args[0] == verb }
}

func gwmCalls(f *cmdruntest.Fake, verb string) [][]string {
	var out [][]string
	for _, c := range f.Calls() {
		if c.Name == "pg-gwm" && len(c.Args) > 0 && c.Args[0] == verb {
			out = append(out, c.Args)
		}
	}
	return out
}

func gwmEnvelope(name, hint string, details string) string {
	return `{"error":{"name":"` + name + `","message":"refused","hint":"` + hint + `","details":` + details + `}}`
}

// gitBranch answers git for the handler: whether refs/heads/pg-router/bd-1
// exists, and a recording of every call.
type gitScript struct {
	mu        sync.Mutex
	branch    bool
	head      string
	porcelain string
	calls     [][]string
}

func (g *gitScript) run(_ context.Context, _ string, out io.Writer, args ...string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, append([]string(nil), args...))
	switch {
	case len(args) > 2 && args[0] == "rev-parse" && strings.HasPrefix(args[len(args)-1], "refs/heads/"):
		if !g.branch {
			return errors.New("exit 1")
		}
	case len(args) > 0 && args[0] == "rev-parse":
		_, _ = out.Write([]byte(g.head + "\n"))
	case len(args) > 0 && args[0] == "status":
		_, _ = out.Write([]byte(g.porcelain))
	}
	return nil
}

type gwmHarness struct {
	run  *ccpoolRun
	gwm  *cmdruntest.Fake
	git  *gitScript
	log  string
	role roles.Role
	cc   *dtest.FakeCC
}

func newGWMHarness(t *testing.T, readOnly bool) *gwmHarness {
	t.Helper()
	cfg := fastCfg()
	cfg.RepoRoot = gwmRoot
	cfg.WorktreeDir = t.TempDir()
	h := &gwmHarness{gwm: cmdruntest.New(t), git: &gitScript{head: "deadbeef"}, cc: &dtest.FakeCC{}}
	h.log = filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(h.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lw.Close() })
	clk := &dtest.ManualClock{T: time.Unix(1_000_000, 0)}
	h.run = &ccpoolRun{deps: Deps{
		CC: h.cc, BD: &dtest.ScriptBD{}, Cfg: cfg, Log: lw, Now: clk.Now, Tick: clk.TickAdvancing(),
		ExternalID: gwmSess, GitRun: h.git.run, StatDir: func(string) error { return nil },
		GWM: &gwm.CLI{Exec: h.gwm.Runner(), RepoRoot: gwmRoot},
	}}
	h.role = workerRole(cfg)
	if readOnly {
		h.role.CCPool.Isolation.BranchAccess = roles.BranchRead
	}
	return h
}

func (h *gwmHarness) dispatchCtx() DispatchContext {
	return DispatchContext{Role: h.role, Item: item.Item{ID: "bd-1"}}
}

func (h *gwmHarness) events(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func TestWorktreeIsolationLeases(t *testing.T) {
	base := []string{
		"lease", "--name", "worker/bd-1", "--or-renew", "--owner", gwmSess, "--ttl", "30m",
		"--metadata", "role=worker", "--metadata", "bead=bd-1", "--metadata", "session=" + gwmSess,
	}
	tail := []string{"--repo", gwmRoot, "--format=json"}
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name     string
		readOnly bool
		branch   bool
		want     []string
	}{
		{"write role creates the branch", false, false, join(base, []string{"-b", "pg-router/bd-1"}, tail)},
		{"write role with an existing branch drops -b", false, true, join(base, tail, []string{"pg-router/bd-1"})},
		{"read role detaches at the existing branch", true, true, join(base, []string{"--detach"}, tail, []string{"pg-router/bd-1"})},
		{"read role without a branch detaches alone", true, false, join(base, []string{"--detach"}, tail)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGWMHarness(t, tc.readOnly)
			h.git.branch = tc.branch
			h.gwm.On(gwmByVerb("lease")).Return(cmdruntest.Out(gwmLeaseJSON))
			wt, _, err := h.run.leaseWorktree(context.Background(), h.dispatchCtx(), h.role.CCPool)
			if err != nil {
				t.Fatal(err)
			}
			if wt != "/wt/worker-bd-1" {
				t.Errorf("worktree = %q, want the leased path", wt)
			}
			if got := gwmCalls(h.gwm, "lease"); len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Errorf("lease argv = %v\nwant       %v", got, tc.want)
			}
			if h.run.gwm.startHead != "abc123" {
				t.Errorf("start head = %q, want the lease's head on a first lease", h.run.gwm.startHead)
			}
		})
	}
}

func TestWorktreeIsolationLeases_usageRefusalRetriesOnceWithoutB(t *testing.T) {
	h := newGWMHarness(t, false)
	h.gwm.On(gwmByVerb("lease")).Seq(cmdruntest.Fail(2, "branch exists"), cmdruntest.Out(gwmLeaseJSON))
	if _, _, err := h.run.leaseWorktree(context.Background(), h.dispatchCtx(), h.role.CCPool); err != nil {
		t.Fatal(err)
	}
	got := gwmCalls(h.gwm, "lease")
	if len(got) != 2 || !slices2(got[0], "-b") || slices2(got[1], "-b") {
		t.Errorf("want one -b call then one retry without it, got %v", got)
	}
}

func slices2(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestWorktreeIsolationLeases_renewedOnAnotherBranchWarns(t *testing.T) {
	h := newGWMHarness(t, false)
	h.gwm.On(gwmByVerb("lease")).Return(cmdruntest.Out(
		`{"id":"w-1","lease_id":"l-1","worktree_path":"/wt","branch":"other","name":"worker/bd-1","head":"h","renewed":true}`,
	))
	if _, _, err := h.run.leaseWorktree(context.Background(), h.dispatchCtx(), h.role.CCPool); err != nil {
		t.Fatal(err)
	}
	if ev := h.events(t); !strings.Contains(ev, `"kind":"gwm_unexpected_branch"`) || !strings.Contains(ev, `"level":"warn"`) {
		t.Errorf("events = %s", ev)
	}
	if h.run.gwm.startHead != "" {
		t.Errorf("start head must not be written for a renewed lease, got %q", h.run.gwm.startHead)
	}
	meta := map[string]string{}
	h.run.gwmMeta(meta)
	if _, ok := meta[ccpool.MetaKeyGWMStartHead]; ok || meta[ccpool.MetaKeyGWMLease] != "l-1" {
		t.Errorf("meta = %v", meta)
	}
}

func TestRefusalMapping(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		envelope string
		is       error // the outcome sentinel, nil for a plain failure
		not      error
		unclaims bool
		holder   string
	}{
		{"max reached declines for capacity", 20, gwmEnvelope("max-reached", "wait", `{}`), ErrPoolAtCapacity, nil, false, ""},
		{"busy declines for capacity", 9, gwmEnvelope("busy", "retry", `{"command":"gc"}`), ErrPoolAtCapacity, nil, false, "command=gc"},
		{"low disk declines as low disk", 29, gwmEnvelope("low-disk", "free space", `{}`), ErrLowDisk, ErrPoolAtCapacity, false, ""},
		{"branch in use declines naming the holder", 22, gwmEnvelope("branch-in-use", "release it", `{"path":"/old/bd-1","name":"other/bd-1"}`), ErrPoolAtCapacity, nil, false, "path=/old/bd-1"},
		{"name held is internal", 21, gwmEnvelope("name-held", "x", `{"lease_id":"l-9","status":"active"}`), gwm.ErrNameHeld, ErrPoolAtCapacity, false, "lease_id=l-9"},
		{"ref unresolvable fails the dispatch", 26, gwmEnvelope("ref-unresolvable", "fetch", `{}`), gwm.ErrRefUnresolvable, ErrPoolAtCapacity, true, ""},
		{"broken fails the dispatch", 28, gwmEnvelope("worktree-broken", "repair", `{}`), gwm.ErrBroken, ErrPoolAtCapacity, true, ""},
		{"anything else fails the dispatch", 1, gwmEnvelope("odd", "?", `{}`), gwm.ErrUsage, nil, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGWMHarness(t, false)
			h.role.CCPool.OnDispatchFail = roles.DispatchUnclaim
			h.run.deps.BD = &dtest.ScriptBD{
				StatusSeq:   map[string][]string{"bd-1": {"in_progress"}},
				AssigneeSeq: map[string][]string{"bd-1": {gwmSess}},
			}
			h.gwm.On(gwmByVerb("lease")).Return(cmdrun.Result{Code: tc.code, Stdout: []byte(tc.envelope)})
			_, res, err := h.run.leaseWorktree(context.Background(), h.dispatchCtx(), h.role.CCPool)
			if err == nil {
				t.Fatal("a refusal must fail the lease")
			}
			if tc.is != nil && tc.code != 1 && !errors.Is(err, tc.is) {
				t.Errorf("err = %v, want it to match %v", err, tc.is)
			}
			if tc.not != nil && errors.Is(err, tc.not) {
				t.Errorf("err = %v must not match %v", err, tc.not)
			}
			if tc.unclaims != (len(res.Actions) == 1 && res.Actions[0].Verb == participant.Unclaimed) {
				t.Errorf("unclaim result = %v, want unclaim=%v", res, tc.unclaims)
			}
			ev := h.events(t)
			if !strings.Contains(ev, `"kind":"gwm_refusal"`) || !strings.Contains(ev, `"hint":"`+hintOf(t, tc.envelope)+`"`) {
				t.Errorf("events row missing the name/hint: %s", ev)
			}
			if tc.holder != "" && !strings.Contains(ev, tc.holder) {
				t.Errorf("events row must name the holder %q: %s", tc.holder, ev)
			}
			if strings.Contains(ev, `"level":"warn"`) == (tc.is == gwm.ErrNameHeld) {
				t.Errorf("level wrong for %s: %s", tc.name, ev)
			}
		})
	}
}

func hintOf(t *testing.T, envelope string) string {
	t.Helper()
	var e struct {
		Error struct {
			Hint string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(envelope), &e); err != nil {
		t.Fatal(err)
	}
	return e.Error.Hint
}

// gwmTickClock is a fake clock that counts its readings, so a test can wait
// until the supervision tick it just fired has finished reading the time.
type gwmTickClock struct {
	mu    sync.Mutex
	t     time.Time
	reads chan struct{}
}

func (c *gwmTickClock) Now() time.Time {
	c.mu.Lock()
	t := c.t
	c.mu.Unlock()
	select {
	case c.reads <- struct{}{}:
	default:
	}
	return t
}

func (c *gwmTickClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// startGWMTicks wires a leased dispatch to a hand-fired supervision ticker.
// Per tick the loop reads the clock twice with a lease on (the refresh and the
// renew check).
func startGWMTicks(t *testing.T, h *gwmHarness, ttl time.Duration) (clk *gwmTickClock, t0 time.Time, tick func(at time.Time), stop func()) {
	t.Helper()
	t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	clk = &gwmTickClock{t: t0, reads: make(chan struct{}, 4096)}
	h.run.deps.Now = clk.Now
	h.run.deps.Cfg.LeaseTTL = 2 * time.Minute // refresh interval LeaseTTL/4 = 30s
	mt := newManualTicker()
	h.run.deps.NewTicker = mt.factory
	h.run.gwm.on = true
	h.run.gwm.lease = gwm.Lease{LeaseID: "l-1", Name: "worker/bd-1", Path: "/wt"}
	h.run.gwm.ttl, h.run.gwm.parkTTL = ttl, 72*time.Hour
	h.run.gwm.renewedAt = t0
	stopLease := h.run.startLease(context.Background(), gwmSess)
	<-mt.ready
	if mt.every != 30*time.Second {
		t.Fatalf("tick period = %v, want 30s", mt.every)
	}
	drain := func() {
		for range 2 {
			select {
			case <-clk.reads:
			case <-time.After(30 * time.Second): // a hang guard, not a pacing delay
				t.Fatal("supervision tick did not finish")
			}
		}
	}
	drain() // the inline first pass
	tick = func(at time.Time) {
		clk.set(at)
		mt.ch <- at
		drain()
	}
	return clk, t0, tick, stopLease
}

func TestRenewRidesSupervisionTick(t *testing.T) {
	h := newGWMHarness(t, false)
	renewed := make(chan struct{}, 16)
	h.gwm.On(gwmByVerb("renew")).Respond(func(cmdrun.Call) cmdrun.Result {
		renewed <- struct{}{}
		return cmdrun.Result{}
	})
	_, t0, tick, stop := startGWMTicks(t, h, 30*time.Minute)
	// Each renew is the first tick with under 15 minutes left: 15m30s after the
	// lease, then 15m30s after that renew.
	want := map[time.Time]bool{t0.Add(15*time.Minute + 30*time.Second): true, t0.Add(31 * time.Minute): true}
	for now := t0.Add(30 * time.Second); !now.After(t0.Add(31 * time.Minute)); now = now.Add(30 * time.Second) {
		tick(now)
		if want[now] {
			select {
			case <-renewed:
			case <-time.After(30 * time.Second): // a hang guard, not a pacing delay
				t.Fatalf("no renew on the tick at %v", now)
			}
		}
	}
	stop()

	calls := gwmCalls(h.gwm, "renew")
	if len(calls) != 2 {
		t.Fatalf("renew calls over 31 minutes = %d, want 2; %v", len(calls), calls)
	}
	for _, c := range calls {
		if c[len(c)-1] != "l-1" || !slices2(c, "30m") {
			t.Errorf("renew argv = %v, want the lease id and --ttl 30m", c)
		}
	}
}

func TestLeaseLostMidSession(t *testing.T) {
	h := newGWMHarness(t, false)
	h.gwm.On(gwmByVerb("renew")).Return(cmdrun.Result{Code: 25, Stdout: []byte(gwmEnvelope("lease-lost", "re-lease", `{}`))})
	_, t0, tick, stop := startGWMTicks(t, h, 30*time.Minute)
	for now := t0.Add(30 * time.Second); !now.After(t0.Add(15*time.Minute + 30*time.Second)); now = now.Add(30 * time.Second) {
		tick(now)
	}
	stop()

	if n := len(gwmCalls(h.gwm, "renew")); n != 1 {
		t.Errorf("renew calls = %d, want 1 (the first renew finds it lost)", n)
	}
	if ev := h.events(t); !strings.Contains(ev, `"kind":"worktree_lost"`) || !strings.Contains(ev, `"level":"error"`) {
		t.Errorf("events = %s", ev)
	}
	flagged := false
	for _, m := range h.cc.SetMetaCalls() {
		if m.ExternalID == gwmSess && m.Key == ccpool.MetaKeyWorktreeLost && m.Value == "1" {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("the session row must carry %s; meta writes = %v", ccpool.MetaKeyWorktreeLost, h.cc.SetMetaCalls())
	}
	if !h.run.gwm.lost.Load() {
		t.Error("the run must remember the lease is lost, so the dispatch fails once the session settles")
	}
}

func TestPrefetchDetachesBeforeRelease(t *testing.T) {
	prefetchRole := func(h *gwmHarness) *roles.CCPoolConfig {
		cc := h.role.CCPool
		cc.Isolation.Prefetch = &roles.PrefetchConfig{}
		return cc
	}
	setup := func(t *testing.T, head, porcelain string) *gwmHarness {
		h := newGWMHarness(t, true)
		h.git.head, h.git.porcelain = head, porcelain
		h.run.gwm = gwmState{on: true, lease: gwm.Lease{LeaseID: "l-1", Name: "worker/bd-1", Path: "/wt"}, startHead: "start1", prefetchHead: "pf1"}
		return h
	}
	// order records git and pg-gwm calls in one sequence.
	order := func(h *gwmHarness) []string {
		var seq []string
		h.git.mu.Lock()
		for _, c := range h.git.calls {
			seq = append(seq, "git "+strings.Join(c, " "))
		}
		h.git.mu.Unlock()
		return seq
	}
	t.Run("clean checkout at the prefetched head detaches, then releases", func(t *testing.T) {
		h := setup(t, "pf1", "")
		h.gwm.On(gwmByVerb("release")).Return(cmdrun.Result{})
		h.run.gwmSessionEnd(context.Background(), prefetchRole(h), gwmSess, true)
		got := order(h)
		if len(got) == 0 || got[len(got)-1] != "git checkout --detach --quiet start1" {
			t.Errorf("git calls = %v, want a final detach to the start head", got)
		}
		if n := len(gwmCalls(h.gwm, "release")); n != 1 {
			t.Errorf("release calls = %d, want 1", n)
		}
	})
	t.Run("a head that moved is left alone", func(t *testing.T) {
		h := setup(t, "elsewhere", "")
		h.gwm.On(gwmByVerb("release")).Return(cmdrun.Result{})
		h.run.gwmSessionEnd(context.Background(), prefetchRole(h), gwmSess, true)
		for _, c := range order(h) {
			if strings.HasPrefix(c, "git checkout") {
				t.Errorf("must not detach a worktree whose head moved: %v", order(h))
			}
		}
	})
	t.Run("a dirty tree is expired, never detached", func(t *testing.T) {
		h := setup(t, "pf1", " M file.go\n")
		h.gwm.On(gwmByVerb("release")).Return(cmdrun.Result{Code: 23, Stdout: []byte(gwmEnvelope("dirty", "x", `{}`))})
		h.gwm.On(gwmByVerb("expire")).Return(cmdrun.Result{})
		h.run.gwmSessionEnd(context.Background(), prefetchRole(h), gwmSess, true)
		for _, c := range order(h) {
			if strings.HasPrefix(c, "git checkout") {
				t.Errorf("must not detach a dirty tree: %v", order(h))
			}
		}
		if len(gwmCalls(h.gwm, "release")) != 1 || len(gwmCalls(h.gwm, "expire")) != 1 {
			t.Errorf("want release then expire, got %v", h.gwm.Calls())
		}
	})
	t.Run("a live session renews for the parked TTL", func(t *testing.T) {
		h := setup(t, "pf1", "")
		h.run.gwm.parkTTL = 72 * time.Hour
		h.gwm.On(gwmByVerb("renew")).Return(cmdrun.Result{})
		h.run.gwmSessionEnd(context.Background(), prefetchRole(h), gwmSess, false)
		got := gwmCalls(h.gwm, "renew")
		if len(got) != 1 || !slices2(got[0], "3d") {
			t.Errorf("renew calls = %v, want one --ttl 3d", got)
		}
	})
}

func TestLeaseWorktree_missingDirectoryIsRefusedNotDispatched(t *testing.T) {
	h := newGWMHarness(t, false)
	missing := filepath.Join(t.TempDir(), "pristine-wt-5")
	h.run.deps.StatDir = nil // use the real os.Stat against a directory that does not exist
	h.gwm.On(gwmByVerb("lease")).Return(cmdruntest.Out(
		`{"id":"w-5","lease_id":"l-5","worktree_path":"` + missing + `","branch":"pg-router/bd-1","name":"worker/bd-1","head":"h","renewed":true}`,
	))
	h.gwm.On(gwmByVerb("expire")).Return(cmdruntest.Out(`{}`))
	wt, _, err := h.run.leaseWorktree(context.Background(), h.dispatchCtx(), h.role.CCPool)
	if err == nil || wt != "" {
		t.Fatalf("a missing slot directory must refuse the dispatch, got wt=%q err=%v", wt, err)
	}
	if !errors.Is(err, gwm.ErrBroken) || !strings.Contains(err.Error(), missing) {
		t.Errorf("error must be a broken-worktree refusal naming the slot path, got %v", err)
	}
	if h.run.gwm.on {
		t.Error("a refused lease must not be recorded as held")
	}
	if got := gwmCalls(h.gwm, "expire"); len(got) != 1 {
		t.Errorf("the lease on the missing slot must be expired once, got %v", got)
	}
	if ev := h.events(t); !strings.Contains(ev, missing) {
		t.Errorf("events row must show the slot path, got %s", ev)
	}
}

func TestLeaseWorktree_existingDirectoryDispatches(t *testing.T) {
	h := newGWMHarness(t, false)
	dir := t.TempDir()
	h.run.deps.StatDir = nil
	h.gwm.On(gwmByVerb("lease")).Return(cmdruntest.Out(
		`{"id":"w-5","lease_id":"l-5","worktree_path":"` + dir + `","branch":"pg-router/bd-1","name":"worker/bd-1","head":"h","renewed":false}`,
	))
	wt, _, err := h.run.leaseWorktree(context.Background(), h.dispatchCtx(), h.role.CCPool)
	if err != nil || wt != dir {
		t.Fatalf("wt=%q err=%v", wt, err)
	}
}
