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

// Beads pg2-pzb5d8 and pg2-fx5x0l.6: a slow or hung postStartup hook must not
// delay producers or ANY role's dispatch, including its own role's first one
// (the per-role dispatch gate and its timeout are gone; the handler protects the
// first dispatch itself, INV-CCH-41).

// hookRunner is a wireclient.Runner whose argv is [fake-handler, subcommand,
// role]. The postStartup of role "slow" blocks until released or its ctx ends.
type hookRunner struct {
	releaseSlow chan struct{}
	mu          sync.Mutex
	steps       []string // "<role>:<subcommand>" in completion/start order
	slowHookErr error
	failFast    bool // the "fast" role's postStartup exits non-zero
}

func (r *hookRunner) count(s string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, x := range r.steps {
		if x == s {
			n++
		}
	}
	return n
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
	if sub == "postStartup" && role == "fast" && r.failFast {
		r.record("fast:postStartup-failed")
		return nil, 1, nil
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

func TestRunLongRunning_blockedPostStartupDoesNotDelayAnyRole(t *testing.T) {
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

	// Both roles dispatch while slow's hook is still blocked: no gate holds slow's
	// own first dispatch behind its hook.
	waitFor(t, "fast role dispatch while slow's postStartup is blocked", func() bool { return r.seen("fast:dispatch") })
	waitFor(t, "slow role dispatch while its own postStartup is blocked", func() bool { return r.seen("slow:dispatch") })
	if r.seen("slow:postStartup-returned") {
		t.Fatal("the slow hook returned; the test needs it still blocked to prove the dispatch is not gated")
	}
}

func TestRunLongRunning_postStartupRunsOncePerEnabledRoleAndFailureIsNotFatal(t *testing.T) {
	r := &hookRunner{releaseSlow: make(chan struct{}), failFast: true}
	close(r.releaseSlow)
	stop, done := startHookDaemon(t, r)
	t.Cleanup(func() { stop(); <-done })

	waitFor(t, "both hooks", func() bool { return r.seen("slow:postStartup-returned") && r.seen("fast:postStartup-failed") })
	// A failed hook is logged and nothing more: the role still dispatches.
	waitFor(t, "fast role dispatch after its hook failed", func() bool { return r.seen("fast:dispatch") })
	if n := r.count("fast:postStartup-failed"); n != 1 {
		t.Fatalf("fast hook ran %d times, want exactly once", n)
	}
}
