// runner.go: the bd-CLI exec seam. Re-homed from
// packages/pg-pr/pkg/beads/runner.go's Runner/CLIRunner (NewCLIRunner) —
// the concrete precedent this packet's bead cites for a bd-CLI-wrapper
// carry-over — with no behavior change: still shell out to `bd`, still
// return stdout plus a wrapped error (with a trimmed stderr tail) on
// failure. This backend's own client.go/backend.go build on top of it
// rather than importing packages/pg-pr, which packages/pg-connector's go.mod
// does not depend on at all [design: §5.2].
//
// Workspace resolution is NOT part of that carry-over and was revisited by
// bead pg2-1q9c0 (design review finding A9): the original binding decision
// here was to resolve bd's workspace the same way a human `bd` invocation
// would (cwd auto-discovery), reasoning that "bd itself already owns that
// discovery logic." That is wrong for a backend exec'd by the pg-connector
// umbrella: the umbrella inherits the CALLER's cwd (an ambient property of
// whoever/wherever invoked pg-connector), not a human deliberately sitting
// in a chosen repo, and this workspace's own recorded bd hazard is exactly
// that bd resolves to whichever tracker the cwd happens to land in. A
// caller running pg-connector from inside a different repo's cwd would
// silently create/mutate an issue in THAT repo's tracker while believing
// it addressed this one, with nothing in the response to reveal it. See
// ResolveWorkspaceDir below for the corrected binding decision.
package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner shells out to `bd`. Production code uses CLIRunner; tests inject a
// fake runner that returns canned output without spawning a process —
// mirrors packages/pg-pr/pkg/beads.Runner's identical seam.
type Runner interface {
	// Run invokes `bd args...` and returns stdout. On failure, the returned
	// error wraps the underlying exec error and includes a trimmed stderr
	// tail so callers can surface useful messages. Unlike a typical Go exec
	// wrapper, Run's stdout return value is meaningful EVEN when err != nil:
	// bd's own `--json` flag writes a well-formed JSON error envelope to
	// stdout on many failure paths (not_found chief among them) while also
	// exiting non-zero, so a caller that only checked err would discard a
	// structured error body it could otherwise classify.
	//
	// Run resolves an explicit bd workspace directory before ever invoking
	// `bd` (see ResolveWorkspaceDir) and passes it via bd's own `-C` flag —
	// the workspace bd resolves to is never left to the exec'd process's
	// ambient/inherited cwd. If no workspace can be resolved, Run returns
	// ErrWorkspaceNotConfigured without spawning `bd` at all.
	Run(ctx context.Context, args ...string) (stdout string, err error)

	// Workspace reports the same directory Run would pass via `-C` for its
	// next call (or the same resolution error Run would itself return) —
	// without invoking bd. Backend uses this to echo the tracker a
	// successful call actually hit back into schema.Issue.Tracker [bead:
	// pg2-1q9c0, AC2].
	Workspace() (dir string, err error)
}

// EnvWorkspaceDir is the env var this backend checks first when resolving
// bd's workspace directory — a dedicated, backend-scoped override in the
// same env-var-driven-config style as pg-connector's own $PG_PR_CONFIG
// (cmd/pg-connector/registry.go), rather than a new per-backend registry
// field (the shared connector.<type> registry carries only bare binary
// names today; widening its shape is out of this bead's scope).
const EnvWorkspaceDir = "PG_CONNECTOR_ISSUE_BEADS_DIR"

// envBeadsDir is bd's OWN native workspace override — confirmed present in
// the real bd v1.2.2 binary ("BEADS_DIR is set: %s" / "BEADS_DIR takes
// precedence over contributor routing", read directly from its embedded
// strings, not assumed) — checked second so an operator who has already
// pinned bd globally via $BEADS_DIR does not also have to configure this
// backend separately.
const envBeadsDir = "BEADS_DIR"

// ErrWorkspaceNotConfigured is returned when neither EnvWorkspaceDir nor
// bd's own $BEADS_DIR is set. Refusing outright here — rather than falling
// back to the exec'd process's inherited cwd, the actual defect bead
// pg2-1q9c0 (design finding A9) reports — is deliberate: a caller whose
// cwd happens to land in an unrelated tracker must get a clear,
// classifiable error (surfaced by Backend as scriptout.ErrUnavailable),
// not a wrong-bead mutation with no sign anything went wrong.
var ErrWorkspaceNotConfigured = errors.New(
	"issue-beads: bd workspace not configured; set $" + EnvWorkspaceDir +
		" (or bd's own $" + envBeadsDir + ") to an absolute path so this backend's effective tracker does not depend on the caller's cwd",
)

// ResolveWorkspaceDir resolves the directory every bd invocation is pinned
// to via `-C`, using getenv (production passes os.Getenv; tests inject a
// fixed lookup so resolution never depends on this process's real
// environment). EnvWorkspaceDir wins when both are set.
func ResolveWorkspaceDir(getenv func(string) string) (string, error) {
	if dir := strings.TrimSpace(getenv(EnvWorkspaceDir)); dir != "" {
		return dir, nil
	}
	if dir := strings.TrimSpace(getenv(envBeadsDir)); dir != "" {
		return dir, nil
	}
	return "", ErrWorkspaceNotConfigured
}

// CLIRunner is the default Runner. It invokes the `bd` binary from PATH.
type CLIRunner struct {
	// Dir pins the directory Run passes to bd via `-C` (and also sets as
	// the child process's cwd, belt-and-suspenders). Optional — tests set
	// this directly to a disposable per-test workspace; when empty (the
	// production default, via NewCLIRunner), Run/Workspace resolve it
	// themselves via ResolveWorkspaceDir on every call rather than
	// defaulting to the inherited cwd.
	Dir string
	// Env overrides the env block for the exec'd `bd` process. If nil, the
	// process env is used. Tests use this to point a disposable per-test bd
	// workspace without leaking BEADS_DIR/WORKSPACE_ROOT from the outer
	// environment.
	Env []string
	// Getenv resolves EnvWorkspaceDir/BEADS_DIR when Dir is unset. Optional
	// — nil means os.Getenv. This is deliberately separate from Env (which
	// only affects the exec'd bd child's environment): Getenv controls what
	// THIS Runner reads to pick a workspace, independent of what it later
	// hands the child.
	Getenv func(string) string
}

// NewCLIRunner returns a CLIRunner using the process env. Dir is left
// unset so every call resolves the workspace explicitly via
// ResolveWorkspaceDir rather than inheriting whatever cwd this process
// happens to have been exec'd into.
func NewCLIRunner() *CLIRunner { return &CLIRunner{} }

// resolveDir returns r.Dir if set, else resolves it via
// ResolveWorkspaceDir using r.Getenv (os.Getenv when nil).
func (r *CLIRunner) resolveDir() (string, error) {
	if r.Dir != "" {
		return r.Dir, nil
	}
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return ResolveWorkspaceDir(getenv)
}

// Workspace implements Runner.Workspace.
func (r *CLIRunner) Workspace() (string, error) {
	return r.resolveDir()
}

// Run shells out to `bd`, explicitly pinned via `-C` to a resolved
// workspace directory (see resolveDir) rather than the exec'd process's
// ambient cwd.
func (r *CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	dir, err := r.resolveDir()
	if err != nil {
		return "", err
	}
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "bd", fullArgs...)
	cmd.Dir = dir
	if r.Env != nil {
		cmd.Env = r.Env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("bd %s: %w: %s",
				strings.Join(fullArgs, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), fmt.Errorf("bd %s: %w (is bd on PATH?)",
			strings.Join(fullArgs, " "), err)
	}
	return stdout.String(), nil
}
