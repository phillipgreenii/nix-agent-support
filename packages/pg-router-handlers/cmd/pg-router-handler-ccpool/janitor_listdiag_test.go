package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
)

// Session-list observer, the list-call counters and the bounded diagnostic.

// listCC is a ccpool.Runner whose List blocks on gate (when set) and returns err.
type listCC struct {
	ccpool.Runner
	gate chan struct{}
	err  error
	mu   sync.Mutex
	n    int
}

func (l *listCC) List(context.Context) ([]ccpool.Session, error) {
	l.mu.Lock()
	l.n++
	l.mu.Unlock()
	if l.gate != nil {
		<-l.gate
	}
	return nil, l.err
}

func newTestDiag(t *testing.T, mutate func(*listDiagConfig)) (*listDiagConfig, *[]map[string]any) {
	t.Helper()
	var recs []map[string]any
	var mu sync.Mutex
	d := &listDiagConfig{
		Enabled: true, Threshold: 10 * time.Millisecond,
		Expiry:   time.Now().Add(time.Hour),
		StateDir: t.TempDir(), MaxPerRun: 2, MaxTotal: 5,
		Sample: func(context.Context, int) listDiagSample { return listDiagSample{Stuck: "git"} },
		Record: func(f map[string]any) { mu.Lock(); recs = append(recs, f); mu.Unlock() },
	}
	if mutate != nil {
		mutate(d)
	}
	return d, &recs
}

// slowList runs one List that outlives the threshold by a margin.
func slowList(t *testing.T, o *listObserver, ctx context.Context, gate chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	go func() { _, _ = o.List(ctx); close(done) }()
	time.Sleep(60 * time.Millisecond) // well past the 10 ms threshold
	close(gate)
	<-done
}

func TestListObserver_countsCallsWallTimeAndTimeouts(t *testing.T) {
	counts := &passCounts{Pass: passWorktree, Role: "a"}
	ctx := withCounts(context.Background(), counts)
	cc := &listCC{}
	o := &listObserver{Runner: cc}
	for i := 0; i < 2; i++ {
		if _, err := o.List(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cc.err = fmt.Errorf("ccpool list timed out after 1m0s: %w", ccpool.ErrCallTimeout)
	if _, err := o.List(ctx); err == nil {
		t.Fatal("want the timeout error back")
	}
	cc.err = errors.New("some other failure")
	_, _ = o.List(ctx)
	if counts.ListCalls != 4 || counts.ListTimeouts != 1 {
		t.Fatalf("calls %d timeouts %d, want 4 and 1 (only a timeout counts as one)", counts.ListCalls, counts.ListTimeouts)
	}
	// a context without counters pays nothing and does not panic
	if _, err := o.List(context.Background()); err == nil {
		t.Fatal("want the error back")
	}
}

func TestListDiag_slowListIsSampledOnceAndRecordedWithContext(t *testing.T) {
	d, recs := newTestDiag(t, nil)
	counts := &passCounts{Pass: passOrphan, Role: "a", Pool: "/p"}
	o := &listObserver{Runner: &listCC{gate: make(chan struct{})}, diag: d}
	gate := o.Runner.(*listCC).gate
	slowList(t, o, withCounts(context.Background(), counts), gate)
	if len(*recs) != 1 {
		t.Fatalf("records = %v, want exactly one", *recs)
	}
	r := (*recs)[0]
	if r["stuck"] != "git" || r["pass"] != passOrphan || r["role"] != "a" || r["pool"] != "/p" {
		t.Fatalf("record = %v", r)
	}
}

func TestListDiag_fastListIsNotSampled(t *testing.T) {
	d, recs := newTestDiag(t, func(d *listDiagConfig) { d.Threshold = time.Hour })
	o := &listObserver{Runner: &listCC{}, diag: d}
	_, _ = o.List(context.Background())
	if len(*recs) != 0 {
		t.Fatalf("records = %v, want none", *recs)
	}
}

func TestListDiag_perRunAndTotalCapsBoundTheSamples(t *testing.T) {
	d, recs := newTestDiag(t, func(d *listDiagConfig) { d.MaxPerRun = 2; d.MaxTotal = 3 })
	for i := 0; i < 4; i++ {
		o := &listObserver{Runner: &listCC{gate: make(chan struct{})}, diag: d}
		slowList(t, o, context.Background(), o.Runner.(*listCC).gate)
	}
	if len(*recs) != 2 {
		t.Fatalf("per-run cap: %d samples, want 2", len(*recs))
	}
	// A second run (fresh config, same state dir) may take only one more.
	d2, recs2 := newTestDiag(t, func(c *listDiagConfig) { c.StateDir = d.StateDir; c.MaxPerRun = 2; c.MaxTotal = 3 })
	for i := 0; i < 3; i++ {
		o := &listObserver{Runner: &listCC{gate: make(chan struct{})}, diag: d2}
		slowList(t, o, context.Background(), o.Runner.(*listCC).gate)
	}
	if len(*recs2) != 1 {
		t.Fatalf("total cap: second run took %d samples, want 1 (3 - 2)", len(*recs2))
	}
	if b, _ := os.ReadFile(filepath.Join(d.StateDir, listDiagCountFile)); string(b) != "3\n" {
		t.Fatalf("count file = %q", b)
	}
}

func TestListDiag_offAndExpiredTakeNoSample(t *testing.T) {
	for name, mut := range map[string]func(*listDiagConfig){
		"off":     func(d *listDiagConfig) { d.Enabled = false },
		"expired": func(d *listDiagConfig) { d.Expiry = time.Now().Add(-time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			d, recs := newTestDiag(t, mut)
			o := &listObserver{Runner: &listCC{gate: make(chan struct{})}, diag: d}
			slowList(t, o, context.Background(), o.Runner.(*listCC).gate)
			if len(*recs) != 0 {
				t.Fatalf("records = %v, want none", *recs)
			}
		})
	}
}

const psFixture = `  100     1 S    01:00 /sbin/launchd
  200   100 S    00:20 /nix/store/x/bin/pg-router-handler-ccpool janitor
  300   200 S    00:19 /nix/store/y/bin/ccpool list --all --json
  400   300 S    00:18 tmux -L pool list-panes -a
  500   100 S    00:05 /usr/bin/git status
`

func TestClassifyStuck(t *testing.T) {
	rows := parsePS(psFixture)
	tree := descendants(rows, 200)
	if len(tree) != 2 || tree[0].PID != 300 || tree[1].PID != 400 {
		t.Fatalf("descendants of 200 = %+v (pid 500 is not below it)", tree)
	}
	if stuck, pid := classifyStuck(tree); stuck != "tmux" || pid != 300 {
		t.Fatalf("stuck %q pid %d, want tmux under 300", stuck, pid)
	}
	if stuck, _ := classifyStuck(tree[:1]); stuck != "ccpool-self" {
		t.Fatalf("a ccpool with no child = %q, want ccpool-self", stuck)
	}
	gitRows := parsePS("300 200 S 00:19 ccpool list\n450 300 S 00:02 /usr/bin/git -C /r rev-parse\n")
	if stuck, _ := classifyStuck(descendants(gitRows, 200)); stuck != "git" {
		t.Fatalf("stuck = %q, want git", stuck)
	}
	if stuck, pid := classifyStuck(nil); stuck != "none" || pid != 0 {
		t.Fatalf("empty tree = %q %d", stuck, pid)
	}
}

// The janitor row and the gauges carry the list numbers and the oldest id.
func TestProgress_janitorRecordsListCallsAndOldestCandidate(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", "/pool/a"})
	cc := &listCC{}
	mk := func(config.Config, roles.Role) executor.Deps {
		return executor.Deps{CC: cc, LockDir: sessionlock.Dir(f.stateDir)}
	}
	var pts []gaugePoint
	rec := &recorder{}
	p := rec.passes()
	p.emit = spyEmit(&pts)
	p.worktree = func(ctx context.Context, _ roles.Role, _ config.Config, deps executor.Deps) int {
		_, _ = deps.CC.List(ctx)
		cc.err = fmt.Errorf("timed out: %w", ccpool.ErrCallTimeout)
		_, _ = deps.CC.List(ctx)
		c := countsFrom(ctx)
		c.OldestUnexaminedID = "bead-old"
		return 0
	}
	out := new(bytes.Buffer)
	code := serveJanitor(context.Background(), out, f.opts, mk, p)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	ev := f.events()
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	row := ev[0]
	if row["list_calls"] != 2.0 || row["list_timeouts"] != 1.0 {
		t.Fatalf("run totals = %v", row)
	}
	byPool, _ := row["list_by_pool"].(map[string]any)
	pool, _ := byPool["/pool/a"].(map[string]any)
	if pool["calls"] != 2.0 || pool["timeouts"] != 1.0 {
		t.Fatalf("list_by_pool = %v", byPool)
	}
	var wt map[string]any
	for _, r := range row["counters"].([]any) {
		if m, _ := r.(map[string]any); m["pass"] == passWorktree {
			wt = m
		}
	}
	if wt["list_calls"] != 2.0 || wt["list_timeouts"] != 1.0 || wt["pool"] != "/pool/a" || wt["oldest_unexamined_id"] != "bead-old" {
		t.Fatalf("worktree counters = %v", wt)
	}
	for name, want := range map[string]float64{gaugeJanitorListCalls: 2, gaugeJanitorListTimeouts: 1, gaugeJanitorOldestHash: idHash("bead-old")} {
		if g, ok := find(pts, name, passWorktree); !ok || g.value != want {
			t.Errorf("%s = %+v ok=%v, want %v", name, g, ok, want)
		}
	}
	if g, ok := find(pts, gaugeJanitorListSeconds, passWorktree); !ok || g.labels["pool"] != "/pool/a" {
		t.Errorf("list seconds gauge = %+v ok=%v, want a pool label", g, ok)
	}
}

func TestTrackUnexamined_namesTheLongestWaitingCandidate(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Unix(1700000000, 0)
	a := leakedWorktree{bead: "bead-a", path: "/w/a"}
	b := leakedWorktree{bead: "bead-b", path: "/w/b"}
	trackUnexamined(&passCounts{}, dir, t0, []leakedWorktree{b}, nil) // b first seen at t0
	c := &passCounts{}
	trackUnexamined(c, dir, t0.Add(time.Hour), []leakedWorktree{a, b}, nil)
	if c.OldestUnexaminedID != "bead-b" || c.OldestUnexaminedAgeSeconds != 3600 {
		t.Fatalf("oldest = %q %v, want bead-b 3600", c.OldestUnexaminedID, c.OldestUnexaminedAgeSeconds)
	}
	c2 := &passCounts{}
	trackUnexamined(c2, dir, t0.Add(time.Hour), []leakedWorktree{a, b}, map[string]bool{"bead-a": true, "bead-b": true})
	if c2.OldestUnexaminedID != "" {
		t.Fatalf("a pass that reached everything names no candidate, got %q", c2.OldestUnexaminedID)
	}
}

func TestIdHashIsStable(t *testing.T) {
	if idHash("abc") != idHash("abc") || idHash("abc") == idHash("abd") {
		t.Fatal("hash must be stable and discriminating")
	}
	if idHash("abc") != 440920331 { // FNV-1a 32 of "abc"
		t.Fatalf("hash = %v", idHash("abc"))
	}
}
