package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
)

// precheck/precheckPrefix/resolveSelf/parseSelfLogin/readBeadsPrefix are the
// three R14 startup pre-flight blockers (bd unreachable, beads-prefix
// mismatch, pg-pr config show self-login) — MOVED HERE VERBATIM from
// packages/pg-router/cmd/pg-router/drain.go (docket pg2-oju6w's Task 5.8,
// ADR 0065's "Source-side boundary" section, closing register row R14 /
// bead pg2-d4gvb): pg-router's own pre-runtime validation (INV-WORKFLOW-1's
// six determinable conditions) no longer runs these three tool-naming/
// connectivity checks ahead of it. This module's `query` subcommand runs
// precheck (bd reachability + prefix match) before it queries beads for
// real — see query.go's runQuery. resolveSelf/parseSelfLogin exist here,
// tested, ready for whichever call site's own production wiring resolves
// SelfLogin next (mirroring internal/wireclient.CommandFor's own precedent
// of an unwired-but-ready seam — cmd/pg-router's bootCore never assigns
// Orchestrator.Handler either).

// resolveSelf shells out to `pg-pr config show --json` and reads .self_login.
func resolveSelf(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "pg-pr", "config", "show", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("pg-pr config show: %w", err)
	}
	return parseSelfLogin(out)
}

// parseSelfLogin extracts self_login from pg-pr config JSON.
func parseSelfLogin(b []byte) (string, error) {
	var cfg struct {
		SelfLogin string `json:"self_login"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("parse pg-pr config: %w", err)
	}
	if cfg.SelfLogin == "" {
		return "", fmt.Errorf("self_login is empty")
	}
	return cfg.SelfLogin, nil
}

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
