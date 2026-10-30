package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Bead pg2-pzb5d8: a slow or hung postStartup hook of one role must not delay
// producers or another role's dispatch; a role's own first dispatch waits for
// ITS OWN hook, bounded by postStartupTimeout.

// hookRunner is a wireclient.Runner whose argv is [fake-handler, subcommand,
// role]. The postStartup of role "slow" blocks until released or its ctx ends.
type hookRunner struct {
	releaseSlow chan struct{}
	mu          sync.Mutex
	steps       []string // "<role>:<subcommand>" in completion/start order
	slowHookErr error
}

func (r *hookRunner) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, s)
}

func (r *hookRunner) seen(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.steps {
		if x == s {
			return true
		}
	}
	return false
}

func (r *hookRunner) Run(ctx context.Context, argv []string, _ []byte) ([]byte, int, error) {
	sub, role := argv[1], argv[2]
	if sub == "validate" {
		return nil, 2, nil
	}
	if sub == "postStartup" && role == "slow" {
		select {
		case <-r.releaseSlow:
		case <-ctx.Done():
			r.mu.Lock()
			r.slowHookErr = ctx.Err()
			r.mu.Unlock()
			r.record("slow:postStartup-cancelled")
			return nil, 0, ctx.Err()
		}
		r.record("slow:postStartup-returned")
	}
	if sub == "dispatch" {
		r.record(role + ":dispatch")
	}
	return []byte(`{"schemaVersion":"1","id":"x","outcome":"delivered"}`), 0, nil
}

func startHookDaemon(t *testing.T, r *hookRunner) (stop context.CancelFunc, done <-chan int) {
	t.Helper()
	cfg := config.Config{
		LogDir:       shortDir(t),
		PollInterval: 20 * time.Millisecond,
		Roles: roles.RoleSet{
			{Name: "slow", Enabled: true, Binds: []string{"t1"}},
			{Name: "fast", Enabled: true, Binds: []string{"t2"}},
		},
		Queries: query.SourceSet{
			{Name: "tick1", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"t1"}}}},
			{Name: "tick2", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"t2"}}}},
		},
	}
	h := &wireclient.Client{Runner: r, Command: func(role roles.Role, sub string) ([]string, error) {
		return []string{"fake-handler", sub, role.Name}, nil
	}}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: h, Bindings: core.NewBindings("t1", "t2")}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan int, 1)
	go func() {
		ch <- runLongRunningBody(ctx, preparedRun{cfg: cfg, o: o, cleanup: func() {}, declaredRoles: cfg.Roles}, time.Second)
	}()
	return cancel, ch
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunLongRunning_blockedPostStartupDoesNotDelayOtherRole(t *testing.T) {
	r := &hookRunner{releaseSlow: make(chan struct{})}
	stop, done := startHookDaemon(t, r)
	released := false
	release := func() {
		if !released {
			released = true
			close(r.releaseSlow)
		}
	}
	t.Cleanup(func() { release(); stop(); <-done })

	// Producers enqueue and the OTHER role dispatches while slow's hook is blocked.
	waitFor(t, "fast role dispatch while slow's postStartup is blocked", func() bool { return r.seen("fast:dispatch") })
	if r.seen("slow:dispatch") {
		t.Fatal("slow role dispatched before its own postStartup returned")
	}
	// Give the gated dispatch time to (wrongly) slip through, then release.
	time.Sleep(100 * time.Millisecond)
	if r.seen("slow:dispatch") {
		t.Fatal("slow role dispatched before its own postStartup returned")
	}
	release()
	waitFor(t, "slow role dispatch after its hook returned", func() bool { return r.seen("slow:dispatch") })
	r.mu.Lock()
	defer r.mu.Unlock()
	ret, disp := -1, -1
	for i, s := range r.steps {
		if s == "slow:postStartup-returned" && ret < 0 {
			ret = i
		}
		if s == "slow:dispatch" && disp < 0 {
			disp = i
		}
	}
	if ret < 0 || disp < ret {
		t.Fatalf("slow dispatch (%d) must follow its postStartup return (%d); steps=%v", disp, ret, r.steps)
	}
}

func TestRunLongRunning_hungPostStartupIsBoundedByTimeout(t *testing.T) {
	old := postStartupTimeout
	postStartupTimeout = 150 * time.Millisecond
	t.Cleanup(func() { postStartupTimeout = old })

	r := &hookRunner{releaseSlow: make(chan struct{})} // never released
	stop, done := startHookDaemon(t, r)
	t.Cleanup(func() { stop(); <-done })

	waitFor(t, "slow role dispatch after its hook timed out", func() bool { return r.seen("slow:dispatch") })
	if !r.seen("slow:postStartup-cancelled") {
		t.Fatalf("the hung hook was not cancelled; steps=%v", r.steps)
	}
}
