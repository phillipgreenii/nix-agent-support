package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// postStartupTimeout bounds ONE role's postStartup hook in the long-running
// daemon (bead pg2-pzb5d8). A hook that has not returned by then is cancelled
// (its adapter process is killed through the context), logged with role and
// elapsed time, and that role's dispatch gate opens anyway: a hung hook cannot
// hold dispatch indefinitely. A variable so a test can shrink it.
var postStartupTimeout = 5 * time.Minute

// startPostStartupAsync is the long-running daemon's replacement for the
// blocking postStartupAll (bead pg2-pzb5d8). On 2026-10-30 three restarts left
// the whole router idle for 10 to 22 minutes because every enabled role's
// postStartup hook (the ccpool adapter's per-role reconcile takes minutes) ran
// one after another before the first producer tick.
//
// It returns immediately, so the tick loop and the startup dispatch pass start
// at once, and runs ONE goroutine per enabled role, each bounded by
// postStartupTimeout. Per-role ordering that is KEPT: a role's FIRST dispatch
// waits for ITS OWN hook (returned, failed or timed out), because the ccpool
// reconcile closes sessions whose bead closed before that role's new dispatches
// start. It does NOT wait for any other role's hook, and producers never wait.
// Mechanism: o.Handler is wrapped so Dispatch blocks on the role's gate.
//
// Concurrent hooks are safe: the adapter's passes are idempotent, best effort
// and take per-session/per-worktree flocks (pg-router-handler-ccpool's
// runSweeps doc), and dispatches of different roles already run the same
// sweeps concurrently with each other.
//
// If ctx (shutdown) ends while a hook is still running, its gate stays closed:
// a dispatch then waits until its own context is cancelled rather than starting
// work after a stop request. A nil o.Handler is logged once and skipped, as in
// postStartupAll. preShutdownAll is untouched (it calls the wrapper's
// PreShutdown, which delegates verbatim).
func startPostStartupAsync(ctx context.Context, o *orchestrator.Orchestrator, cfg config.Config) {
	if o.Handler == nil {
		slog.Warn("postStartup skipped: no Handler configured (internal/wireclient.HandlerClient)")
		return
	}
	g := &postStartupGate{inner: o.Handler, gates: map[string]chan struct{}{}}
	var enabled []roles.Role
	for _, r := range cfg.Roles {
		if !r.Enabled {
			continue
		}
		if _, dup := g.gates[r.Name]; !dup {
			g.gates[r.Name] = make(chan struct{})
		}
		enabled = append(enabled, r)
	}
	o.Handler = g
	for _, r := range enabled {
		go g.runHook(ctx, r)
	}
}

// postStartupGate is the wireclient.HandlerClient wrapper that holds a role's
// Dispatch until that role's postStartup hook has finished or timed out.
type postStartupGate struct {
	inner wireclient.HandlerClient
	gates map[string]chan struct{} // role name -> closed when its dispatch may start; read-only after start
	once  sync.Map                 // role name -> *sync.Once guarding close
}

func (g *postStartupGate) open(name string) {
	o, _ := g.once.LoadOrStore(name, &sync.Once{})
	o.(*sync.Once).Do(func() { close(g.gates[name]) })
}

func (g *postStartupGate) runHook(parent context.Context, r roles.Role) {
	start := time.Now()
	hctx, cancel := context.WithTimeout(parent, postStartupTimeout)
	defer cancel()
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, err := g.inner.PostStartup(hctx, r)
		done <- result{err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			slog.Warn("postStartup failed", "role", r.Name, "err", res.err, "elapsed", time.Since(start))
		} else {
			slog.Info("postStartup done", "role", r.Name, "elapsed", time.Since(start))
		}
	case <-hctx.Done():
		if parent.Err() != nil {
			// Shutdown requested: leave the gate closed (see the doc above).
			return
		}
		slog.Warn("postStartup timed out; releasing the role's dispatch gate", "role", r.Name, "elapsed", time.Since(start), "timeout", postStartupTimeout)
	}
	if parent.Err() != nil {
		return
	}
	g.open(r.Name)
}

func (g *postStartupGate) Dispatch(ctx context.Context, role roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	if ch, ok := g.gates[role.Name]; ok {
		select {
		case <-ch:
		case <-ctx.Done():
			return wireclient.Reply{}, fmt.Errorf("pg-router: role %q dispatch abandoned while its postStartup hook was running: %w", role.Name, ctx.Err())
		}
	}
	return g.inner.Dispatch(ctx, role, evt)
}

func (g *postStartupGate) PostStartup(ctx context.Context, role roles.Role) (wireclient.Reply, error) {
	return g.inner.PostStartup(ctx, role)
}

func (g *postStartupGate) PreShutdown(ctx context.Context, role roles.Role) (wireclient.Reply, error) {
	return g.inner.PreShutdown(ctx, role)
}
