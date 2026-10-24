package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
)

// precheck/precheckPrefix/readBeadsPrefix are the two R14 startup pre-flight
// blockers (bd unreachable, beads-prefix mismatch) — moved here from
// packages/pg-router/cmd/pg-router/drain.go (docket pg2-oju6w's Task 5.8,
// ADR 0065's "Source-side boundary" section, closing register row R14 / bead
// pg2-d4gvb): pg-router's own pre-runtime validation (INV-WORKFLOW-1's six
// determinable conditions) no longer runs these tool-naming/connectivity
// checks ahead of it. This module's `query` subcommand runs precheck (bd
// reachability + prefix match) before it queries beads for real — see
// query.go's runQuery. The former third R14 check (a `<prTool> config show`
// self-login lookup) was dead code with no caller and was removed with the
// retired PR tool (bead pg2-k9vmd).

// precheck asserts bd is reachable from repoRoot and resolves the expected
// store. It does NOT require a local .beads dir at repoRoot: bd is
// git-worktree-aware (it resolves the store from the cwd, the git common dir, or
// the Dolt server), so repoRoot may be a monorepo worktree/slot with no local
// .beads — which is the normal case for workers. Everything is verified through
// bd itself rather than by stat-ing a path.
func precheck(ctx context.Context, repoRoot, beadsPrefix string, br beads.Runner) error {
	if _, err := br.Run(ctx, "list", "--limit", "1", "--json"); err != nil {
		return fmt.Errorf("bd unreachable from %s: %w", repoRoot, err)
	}
	if err := precheckPrefix(ctx, br, beadsPrefix); err != nil {
		return err
	}
	return nil
}

// precheckPrefix asserts the store bd resolves carries the expected issue
// prefix (a guard against pointing at the wrong store). Testable seam: tests
// pass a fake runner returning the prefix.
func precheckPrefix(ctx context.Context, br beads.Runner, want string) error {
	got, err := readBeadsPrefix(ctx, br)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("bead prefix %q != expected %q", got, want)
	}
	return nil
}

// readBeadsPrefix asks bd for the resolved issue prefix (`bd config get
// issue_prefix`). This works in a monorepo worktree where there is no local
// .beads/config.yaml — bd resolves it git-aware, exactly as every other bd call
// here does.
func readBeadsPrefix(ctx context.Context, br beads.Runner) (string, error) {
	out, err := br.Run(ctx, "config", "get", "issue_prefix")
	if err != nil {
		return "", fmt.Errorf("bd config get issue_prefix: %w", err)
	}
	prefix := strings.TrimSpace(out)
	if prefix == "" {
		return "", fmt.Errorf("bd config get issue_prefix returned no prefix")
	}
	return prefix, nil
}
