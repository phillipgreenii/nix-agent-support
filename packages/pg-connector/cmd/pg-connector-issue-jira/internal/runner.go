// runner.go: the Jira-CLI exec seam. Mirrors
// cmd/pg-connector-issue-beads/internal/runner.go's CLI-exec-seam
// convention (an injectable Runner interface; production execs a real
// binary, tests inject a fake) [carry-over basis:
// cmd/pg-connector-issue-beads/internal/runner.go] — adapted for Jira:
// unlike bd, the generic Jira CLI this backend resolves
// (phillipg-nix-repo-base's pjira, modules/jira/pkg/pjira +
// modules/jira/cmd/pjira) has no per-invocation workspace/"-C directory"
// concept of its own. It resolves its OWN tenant (base_url/email) and
// credential (an env var, or a keychain-backed secret source per its own
// modules/jira/pkg/pjira/config.go and secret.go) entirely through its own
// config file / env vars (JIRA_BASE_URL, JIRA_EMAIL) — so this backend does
// not pin or override a workspace directory the way
// pg-connector-issue-beads' EnvWorkspaceDir does for bd; there is exactly
// one ambient Jira site pjira itself resolves to. This packet's own binding
// decision leaves Jira scoping, if it were ever needed, as a discovery
// rather than a design requirement: pkg/provider.AuthChecker's own package
// doc comment states each backend "resolves its own credentials entirely
// on its own," without pinning a workspace concept, and this backend's
// own discovery (verified against the real pjira binary/source, see
// backend.go's doc comment) is that no such scoping is needed: a single
// ambient Jira Cloud site is sufficient.
package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Runner execs the resolved Jira CLI binary. Production code uses
// CLIRunner; tests inject a fake runner that returns canned output without
// spawning a process — mirrors packages/pg-connector-issue-beads/internal.Runner's
// identical seam, minus its workspace-directory concept (see this file's
// package doc comment for why that does not carry over).
type Runner interface {
	// Run invokes the resolved binary with args and returns stdout. On
	// failure, the returned error wraps the underlying exec error and
	// includes a trimmed stderr tail so callers can classify it (see
	// backend.go's classifyPJIRAErrorMessage).
	Run(ctx context.Context, args ...string) (stdout string, err error)

	// Binary reports the resolved CLI binary name Run would exec next,
	// without invoking it — mirrors issue-beads' Workspace() precedent,
	// adapted to a binary name rather than a directory.
	Binary() string
}

// EnvBinary is the env var this backend checks to override the resolved
// Jira CLI binary name — a dedicated, backend-scoped override in the same
// env-var-driven-config style as pg-connector-issue-beads' own
// EnvWorkspaceDir, and mirroring
// packages/pg-pr/pkg/provider/issues/jira/jira.go's PGPR_JIRA_BINARY
// (renamed to this backend's own env var per its package/binary name, per
// this packet's binding decision).
const EnvBinary = "PG_CONNECTOR_ISSUE_JIRA_BINARY"

// defaultBinary is "pjira" — VERIFIED against the real tool actually on
// PATH in this workspace (2026-09-08): `command -v jira` resolves to
// nothing, `command -v pjira` resolves to a real installed binary, and its
// own --help output plus its source
// (phillipg-nix-repo-base/modules/jira/cmd/pjira/main.go's NewRootCmd)
// confirm this is phillipg-nix-repo-base's generic, tenant-agnostic Jira
// CLI. This is deliberately NOT
// "jira" — packages/pg-pr/pkg/provider/issues/jira/jira.go's own default —
// that name is stale relative to the real tool's current binary name
// (the tool's module was renamed jira -> pjira; only bin/jira, a dev-only
// `go run` shim in phillipg-nix-repo-base, still uses the old name, and it
// is not installed on PATH).
const defaultBinary = "pjira"

// ResolveBinary resolves the Jira CLI binary name Run execs, using getenv
// (production passes os.Getenv; tests inject a fixed lookup so resolution
// never depends on this process's real environment).
func ResolveBinary(getenv func(string) string) string {
	if bin := strings.TrimSpace(getenv(EnvBinary)); bin != "" {
		return bin
	}
	return defaultBinary
}

// CLIRunner is the default Runner. It execs the resolved Jira CLI binary
// from PATH.
type CLIRunner struct {
	// BinaryOverride pins the binary name directly, bypassing env
	// resolution entirely. Optional — tests set this to a disposable fake
	// binary; when empty (the production default via NewCLIRunner), Run
	// resolves it itself via ResolveBinary on every call.
	BinaryOverride string
	// Env overrides the exec'd process's env block. Nil means the process
	// env — which is where the real pjira binary itself reads
	// JIRA_BASE_URL/JIRA_EMAIL/its own secret-source config from; tests use
	// this to avoid leaking a real tenant's credentials into a fake-binary
	// invocation.
	Env []string
	// Getenv resolves EnvBinary when BinaryOverride is unset. Optional —
	// nil means os.Getenv.
	Getenv func(string) string
}

// NewCLIRunner returns a CLIRunner using the process env.
func NewCLIRunner() *CLIRunner { return &CLIRunner{} }

func (r *CLIRunner) resolveBinary() string {
	if r.BinaryOverride != "" {
		return r.BinaryOverride
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return ResolveBinary(getenv)
}

// Binary implements Runner.Binary.
func (r *CLIRunner) Binary() string { return r.resolveBinary() }

// command builds the *exec.Cmd Run below actually executes, split out
// (mirroring the bd/gh backends' own Command/command choke points) so a
// test can assert on WaitDelay/Env directly without spawning a real
// process.
func (r *CLIRunner) command(ctx context.Context, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.resolveBinary(), args...)
	if r.Env != nil {
		cmd.Env = r.Env
	}
	// See scriptout.DefaultWaitDelay's doc comment for why this is needed
	// even though ctx already carries a deadline: it bounds Cmd.Wait's own
	// residual wait for the stdout/stderr pipes to close, independent of
	// killing the direct child — the same hang guard
	// cmd/pg-connector-issue-beads/internal/runner.go applies to `bd`.
	cmd.WaitDelay = scriptout.DefaultWaitDelay
	return cmd
}

// Run execs the resolved binary with args, returning stdout. On failure it
// wraps the underlying exec error with a trimmed, capped stderr tail so
// callers can classify it without an unbounded error string.
func (r *CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	bin := r.resolveBinary()
	cmd := r.command(ctx, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("%s %s: %w: %s",
				bin, strings.Join(args, " "), err, scriptout.TruncateForFold(stderr.Bytes()))
		}
		return stdout.String(), fmt.Errorf("%s %s: %w (is %s on PATH?)",
			bin, strings.Join(args, " "), err, bin)
	}
	return stdout.String(), nil
}
