package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
)

// janitorDryRun lists what each pass WOULD reclaim, read-only (INV-CCH-41's
// dry run): no lock, no event row, no mutation. It reuses the passes' own
// read-only filters (isOrphanOf, sweepCandidate, worktreeHeldByRow,
// beadAlreadyClosed, reconcilableState), so it is an upper bound: the guards a
// real pass applies under a lock (transcript quiet window, clean and pushed
// worktree, live processes) are not evaluated here. The lock-file collection is
// not previewed.
func janitorDryRun(ctx context.Context, w io.Writer, jroles []janitorRole, cfg config.Config) int {
	fmt.Fprintf(w, "janitor dry run: %d role(s); candidates are an upper bound, nothing is reclaimed\n", len(jroles))
	poolDone := map[string]bool{}
	for _, jr := range jroles {
		if !poolDone[jr.pool] {
			poolDone[jr.pool] = true
			dryClosedBead(ctx, w, jr)
		}
		dryOrphans(ctx, w, jr)
		dryWorktrees(ctx, w, jr)
	}
	fmt.Fprintln(w, "lock_gc: not previewed")
	return 0
}

func dryClosedBead(ctx context.Context, w io.Writer, jr janitorRole) {
	sessions, err := jr.deps.CC.List(ctx)
	if err != nil {
		fmt.Fprintf(w, "%s (pool %q): list failed: %v\n", passClosedBead, jr.pool, err)
		return
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, jr.cfg.SessionPrefix) || s.PurgePending() || !reconcilableState(s.State) {
			continue
		}
		if beadAlreadyClosed(ctx, jr.deps.BD, s) {
			fmt.Fprintf(w, "%s (pool %q): would close session %s\n", passClosedBead, jr.pool, s.ExternalID)
		}
	}
}

func dryOrphans(ctx context.Context, w io.Writer, jr janitorRole) {
	sessions, err := jr.deps.CC.List(ctx)
	if err != nil {
		fmt.Fprintf(w, "%s (role %s): list failed: %v\n", passOrphan, jr.role.Name, err)
		return
	}
	now := orphanNow(jr.deps)
	for _, s := range sessions {
		if isOrphanOf(s, jr.role, jr.cfg.SessionPrefix, now) {
			fmt.Fprintf(w, "%s (role %s): would reclaim orphan %s\n", passOrphan, jr.role.Name, s.ExternalID)
		}
	}
}

func dryWorktrees(ctx context.Context, w io.Writer, jr janitorRole) {
	if !executor.UsesWorktreeIsolation(jr.role.CCPool.Isolation) || jr.cfg.WorktreeDir == "" {
		return
	}
	entries, err := os.ReadDir(jr.cfg.WorktreeDir)
	if err != nil {
		return
	}
	sessions, err := jr.deps.CC.List(ctx)
	if err != nil {
		fmt.Fprintf(w, "%s (role %s): list failed: %v\n", passWorktree, jr.role.Name, err)
		return
	}
	now, grace := orphanNow(jr.deps), worktreeSweepGrace(jr.deps)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, ok := sweepCandidate(filepath.Join(jr.cfg.WorktreeDir, e.Name()), e.Name(), now, grace)
		if ok && !worktreeHeldByRow(sessions, c) {
			fmt.Fprintf(w, "%s (role %s): would examine worktree %s\n", passWorktree, jr.role.Name, c.path)
		}
	}
}
