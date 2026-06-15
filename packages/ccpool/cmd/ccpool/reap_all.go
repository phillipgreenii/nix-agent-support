package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/registry"
)

// runReapAll governs the whole machine in one timer-driven pass: it reaps the
// default pool, then sweeps the permanent registry — GC'ing dangling/invalid
// symlinks (the link ONLY, never the target) and reaping every other registered
// pool, honoring each pool's auto_reap. Per-pool failures are logged but never abort
// the sweep (a timer must not go silent); the exit code is non-zero iff a pool
// errored, so launchd/systemd logs surface it.
func runReapAll(args []string) int {
	_ = args
	rc := 0

	// 1. Default (XDG) pool — preserves today's bare `ccpool reap` behavior,
	//    regardless of any inherited CCPOOL_POOL. The default pool never
	//    self-registers, so it is reaped here exactly once. Honors its own auto_reap.
	if err := reapOnePool(""); err != nil {
		fmt.Fprintln(os.Stderr, "reap-all: default pool:", err)
		rc = 1
	}

	// 2. Every registered pool.
	entries, err := registry.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reap-all: list registry:", err)
		return 1
	}
	for _, e := range entries {
		// GC: a dangling or gone-foreign target fails the read-only pool-dir check,
		// so drop the symlink ONLY (never the target or any pool data). Remove
		// tolerates an already-absent link (ENOENT).
		if verr := config.ValidatePoolDir(e.Target); verr != nil {
			if rerr := registry.Remove(e.Name); rerr != nil {
				fmt.Fprintf(os.Stderr, "reap-all: gc %s: %v\n", e.Name, rerr)
				rc = 1
			}
			continue
		}
		if err := reapOnePool(e.Target); err != nil {
			fmt.Fprintf(os.Stderr, "reap-all: pool %s: %v\n", e.Target, err)
			rc = 1
		}
	}
	return rc
}

// reapOnePool loads a pool's config (empty root = default pool) and reaps it, unless
// the pool opts out via auto_reap = false — that gate is auto-reaper-only, so a
// manual `ccpool reap` still reaps the pool and it stays registered.
func reapOnePool(root string) error {
	cfg, err := config.LoadForPool(root)
	if err != nil {
		return err
	}
	if !cfg.Pool.AutoReap {
		return nil // opted out of the timer-driven sweep
	}
	svc, st, code := buildServiceFor(cfg)
	if code != 0 {
		return fmt.Errorf("build service failed (exit %d)", code)
	}
	defer st.Close()
	return svc.Reap(context.Background(), cfg.Pool.MaxSessions, time.Duration(cfg.Pool.IdleTTL))
}
