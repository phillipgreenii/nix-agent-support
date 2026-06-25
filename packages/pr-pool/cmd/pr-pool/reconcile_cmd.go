package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/reconcile"
)

// runReconcile implements `pr-pool reconcile`: report (read-only) the open
// `process-feedback:` cycles that are self-owned (parent merge-request author ==
// self) but LACK the `mine` label — the cycles discovery's `bd ready --label
// mine` filter silently skips (pg2-eo4n). It is the standalone, on-demand form of
// the same guard drain runs at pre-flight (warnStrandedFeedback). It does NOT
// mutate beads.
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
	return exitOK
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
