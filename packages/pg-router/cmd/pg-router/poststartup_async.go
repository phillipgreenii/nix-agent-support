package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// startPostStartupAsync is the long-running daemon's replacement for the
// blocking postStartupAll (bead pg2-pzb5d8, simplified by pg2-fx5x0l.6). On
// 2026-10-30 three restarts left the whole router idle for 10 to 22 minutes
// because every enabled role's postStartup hook (then the ccpool adapter's
// per-role reconcile, which took minutes) ran one after another before the
// first producer tick.
//
// It returns immediately and runs ONE fire-and-forget goroutine per enabled
// role, logging each outcome. Nothing waits for a hook: no dispatch gate and no
// timeout (the former postStartupGate and postStartupTimeout are gone). What
// protected a role's first post-boot dispatch from the hook's reconcile moved
// into the handler (ADR 0117, INV-CCH-41): the dispatch itself frees a slot a
// stale session holds, and postStartup now only kicks the janitor and returns.
// A hung hook therefore delays nothing, and ctx (shutdown) cancels it through
// the adapter process's context.
//
// A nil o.Handler is logged once and skipped, as in postStartupAll.
// preShutdownAll is untouched.
func startPostStartupAsync(ctx context.Context, o *orchestrator.Orchestrator, cfg config.Config) {
	if o.Handler == nil {
		slog.Warn("postStartup skipped: no Handler configured (internal/wireclient.HandlerClient)")
		return
	}
	for _, r := range cfg.Roles {
		if !r.Enabled {
			continue
		}
		go runPostStartupHook(ctx, o, r)
	}
}

func runPostStartupHook(ctx context.Context, o *orchestrator.Orchestrator, r roles.Role) {
	start := time.Now()
	if _, err := o.Handler.PostStartup(ctx, r); err != nil {
		slog.Warn("postStartup failed", "role", r.Name, "err", err, "elapsed", time.Since(start))
		return
	}
	slog.Info("postStartup done", "role", r.Name, "elapsed", time.Since(start))
}
