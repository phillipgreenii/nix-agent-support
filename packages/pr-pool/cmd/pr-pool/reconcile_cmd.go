package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/prpoolacl"
	"github.com/phillipgreenii/pr-pool/internal/reconcile"
)

// runReconcile implements `pr-pool reconcile`. It does two things:
//
//  1. reports the open `process-feedback:` cycles that are self-owned (parent
//     merge-request author == self) but LACK the `mine` label — the cycles
//     discovery's `bd ready --label mine` filter silently skips them (pg2-eo4n);
//     the standalone form of drain's pre-flight guard (warnStrandedFeedback).
//  2. runs the pg-pr anti-corruption layer (bead pg2-ynhr.2): reads `pg-pr pr
//     list --json` and idempotently ensures a review-pr work bead (child of the
//     pg-pr-owned merge-request bead) per open PR, gated on pg-pr:active-pr.
//
// Step 1 is read-only; step 2 MUTATES beads (ensures review-pr children + gates)
// and is exit-0-on-partial so a following `pr-pool drain` is never stranded.
func runReconcile() int {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitPrecheck
	}
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)

	// self_login: prefer the [pool] config value; else resolve via pg-pr (the
	// authorship comparison needs it; without it nothing can be classified).
	if cfg.SelfLogin == "" {
		selfLogin, err := resolveSelf(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve self:", err)
			return exitPrecheck
		}
		cfg.SelfLogin = selfLogin
	}

	stranded, err := reconcile.StrandedSelfCycles(ctx, br, cfg.SelfLogin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		return exitGeneric
	}
	renderReconcile(os.Stdout, cfg.SelfLogin, stranded)

	return reconcileACL(ctx, os.Stdout, br, cfg.RepoRoot, prpoolacl.ReadPRList)
}

// reconcileACL runs the pg-pr ACL: read the PR list (via listFn, injectable for
// tests) and ensure review-pr beads/gates. It is exit-0-on-partial (H6): a
// pg-pr read failure is logged and treated as zero PRs, and per-PR errors are
// logged; both still return exitOK so a following drain's discovery is not
// aborted. pr-pool's own preconditions (config/self) remain hard failures in the
// caller.
func reconcileACL(
	ctx context.Context,
	w io.Writer,
	br beads.Runner,
	repoRoot string,
	listFn func(context.Context, string) ([]prpoolacl.PR, error),
) int {
	prs, err := listFn(ctx, repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acl: pg-pr pr list unavailable; skipping ACL this pass:", err)
		return exitOK
	}
	ids, errs := prpoolacl.Reconcile(ctx, br, prs)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "acl:", e)
	}
	renderACLResult(w, ids)
	return exitOK
}

// renderACLResult writes the operator-facing summary of ensured review-pr work
// items (the handoff the downstream review role discovers via bd ready).
func renderACLResult(w io.Writer, reviewIDs []string) {
	if len(reviewIDs) == 0 {
		_, _ = fmt.Fprintln(w, "acl: no review-pr work items ensured")
		return
	}
	_, _ = fmt.Fprintf(w, "acl: %d review-pr work item(s) ensured:\n", len(reviewIDs))
	for _, id := range reviewIDs {
		_, _ = fmt.Fprintf(w, "  - %s\n", id)
	}
}

// renderReconcile writes the human-readable stranded-cycle report. The WARN is
// already emitted by StrandedSelfCycles; this is the operator-facing summary with
// the exact backfill command to run.
func renderReconcile(w io.Writer, self string, stranded []string) {
	if len(stranded) == 0 {
		_, _ = fmt.Fprintf(w, "reconcile: no stranded self-owned feedback cycles (self=%s)\n", self)
		return
	}
	_, _ = fmt.Fprintf(w, "reconcile: %d stranded self-owned feedback cycle(s) (self=%s) missing the `mine` label:\n", len(stranded), self)
	for _, id := range stranded {
		_, _ = fmt.Fprintf(w, "  - %s\n", id)
	}
	_, _ = fmt.Fprintln(w, "stamp them discoverable with: bd update <id> --add-label mine")
}
