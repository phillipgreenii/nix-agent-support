package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
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
	// parseInterspersed allows flags to follow positionals; collect non-flag args.
	// For the drain subcommand no positional args are expected.
	fs := flag.NewFlagSet("drain", flag.ContinueOnError)
	pos := parseInterspersed(fs, args)
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "usage: pr-pool [drain]")
		return exitUsage
	}

	ctx := context.Background()
	cfg := config.Load()

	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)

	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	selfLogin, err := resolveSelf(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve self:", err)
		return exitPrecheck
	}

	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: roles.NewRegistry(cfg),
		Cfg: cfg,
	}
	if err := o.DrainOnce(ctx, selfLogin); err != nil {
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

// precheck asserts the bead store is the expected one and bd is reachable.
func precheck(ctx context.Context, cfg config.Config, br beads.Runner) error {
	if _, err := os.Stat(cfg.RepoRoot + "/.beads"); err != nil {
		return fmt.Errorf("no .beads under %s", cfg.RepoRoot)
	}
	if err := precheckPrefix(cfg.RepoRoot, cfg.BeadsPrefix); err != nil {
		return err
	}
	if _, err := br.Run(ctx, "list", "--limit", "1", "--json"); err != nil {
		return fmt.Errorf("bd unreachable: %w", err)
	}
	return nil
}

// precheckPrefix reads the beads prefix from <repoRoot>/.beads/config.yaml and
// asserts it equals want. This is a genuine, testable seam: tests set up a temp
// dir with a real config.yaml and call this directly.
func precheckPrefix(repoRoot, want string) error {
	got, err := readBeadsPrefix(repoRoot)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("bead prefix %q != expected %q", got, want)
	}
	return nil
}

// readBeadsPrefix reads issue_prefix from <repoRoot>/.beads/config.yaml.
func readBeadsPrefix(repoRoot string) (string, error) {
	f, err := os.Open(repoRoot + "/.beads/config.yaml")
	if err != nil {
		return "", fmt.Errorf("open beads config: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "issue_prefix:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "issue_prefix:")), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read beads config: %w", err)
	}
	return "", fmt.Errorf("issue_prefix not found in .beads/config.yaml")
}
