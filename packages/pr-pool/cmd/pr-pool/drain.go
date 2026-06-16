package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// Exit codes (ccpool convention: 1 generic, 2 usage, ≥3 specific).
const (
	exitOK       = 0
	exitGeneric  = 1
	exitUsage    = 2
	exitPrecheck = 3
)

func runDrain(args []string) int {
	// Parse first, with NO side effects: a help request or any parse error must
	// short-circuit here so we never dispatch Claude sessions or tear down
	// pr-pool-* tmux sessions on a bad invocation (pg2-52rn).
	switch p := parseDrainArgs(args); p.kind {
	case routeHelp:
		fmt.Println(helpText)
		return exitOK
	case routeUsageErr:
		printUsageErr(p.msg)
		return exitUsage
	}

	ctx := context.Background()
	cfg := config.Load()

	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)

	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	if _, err := resolveSelf(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "resolve self:", err)
		return exitPrecheck
	}

	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: roles.NewRegistry(cfg),
		Cfg: cfg,
	}
	if lw, err := eventlog.New(filepath.Join(cfg.LogDir, "events.jsonl")); err != nil {
		slog.Warn("eventlog unavailable; watchdog events will not be written", "err", err)
	} else {
		defer func() { _ = lw.Close() }()
		o.Log = lw
	}
	if err := o.DrainOnce(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "drain:", err)
		return exitGeneric
	}
	return exitOK
}

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

// precheck asserts bd is reachable from RepoRoot and resolves the expected
// store. It does NOT require a local .beads dir at RepoRoot: bd is
// git-worktree-aware (it resolves the store from the cwd, the git common dir, or
// the Dolt server), so RepoRoot may be a monorepo worktree/slot with no local
// .beads — which is the normal case for workers. Everything is verified through
// bd itself rather than by stat-ing a path.
func precheck(ctx context.Context, cfg config.Config, br beads.Runner) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := br.Run(ctx, "list", "--limit", "1", "--json"); err != nil {
		return fmt.Errorf("bd unreachable from %s: %w", cfg.RepoRoot, err)
	}
	if err := precheckPrefix(ctx, br, cfg.BeadsPrefix); err != nil {
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
