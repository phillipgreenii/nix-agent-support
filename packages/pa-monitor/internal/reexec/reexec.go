// Package reexec implements the client self-restart mechanism: on a wire
// version mismatch (the daemon was rebuilt under a still-running bridge/TUI),
// the client re-executes itself in place via execve(2) so it picks up the new
// binary while keeping the same PID and controlling TTY.
//
// The decision logic here is pure and build-tag-free so it is table-testable on
// any platform; the single platform-specific call (syscall.Exec) lives behind
// sysExec in exec_unix.go.
package reexec

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// genEnv names the environment variable that carries the current re-exec
// generation (attempt count) across an execve. A parsed value >= MaxAttempts
// means the client has already re-executed that many times without the version
// converging, so the caller gives up.
const genEnv = "PA_MONITOR_REEXEC_GEN"

// MaxAttempts is the small fixed cap on consecutive re-execs. Exhausting it
// reverts the client to the disabled (warn-only) behavior plus a persistent
// error rather than restarting forever during a slow or stuck activation.
const MaxAttempts = 3

// backoff is paused before each execve so that MaxAttempts re-execs span roughly
// MaxAttempts*backoff of wall-clock — wide enough to straddle a normal
// darwin-rebuild profile-symlink flip — instead of firing back-to-back in
// milliseconds. Deliberately unexported: it is an internal pacing constant.
const backoff = 2 * time.Second

// Attempt returns the current re-exec generation parsed from env (a slice of
// "KEY=VALUE" strings, as returned by os.Environ). It is fail-safe: an absent,
// empty, non-integer, or negative value yields 0 ("never re-executed") rather
// than panicking, so a corrupted env can never wedge the client into the
// gave-up state. Per os.Getenv semantics the FIRST occurrence wins.
func Attempt(env []string) int {
	prefix := genEnv + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			n, err := strconv.Atoi(e[len(prefix):])
			if err != nil || n < 0 {
				return 0
			}
			return n
		}
	}
	return 0
}

// Reexec pauses backoff, resolves the exec target via lookPath(base(argv0)) —
// the PATH lookup picks up the darwin-rebuild-flipped profile symlink, NOT the
// running build's resolved /nix/store path (argv0) — and execve's into it with
// the ORIGINAL args (argv[0] pinned) and an env whose genEnv is set to
// attempt+1, replaced in place (never appended: a duplicate key would make the
// child's os.Getenv read the stale first copy).
//
// On success execFn (execve) never returns. It returns an error — and the
// caller then gives up, since a broken exec target will not fix itself — when:
//   - lookPath fails,
//   - the resolved target is not absolute (fail-safe: execve needs an absolute
//     path), or
//   - execFn itself fails.
//
// lookPath, execFn, and sleep are injected so the decision logic is
// unit-testable without touching the process image; Run wires the production
// implementations.
func Reexec(
	argv0 string,
	args, env []string,
	attempt int,
	lookPath func(string) (string, error),
	execFn func(argv0 string, argv, envv []string) error,
	sleep func(time.Duration),
) error {
	sleep(backoff)

	base := filepath.Base(argv0)
	target, err := lookPath(base)
	if err != nil {
		return fmt.Errorf("reexec: resolve %q on PATH: %w", base, err)
	}
	if !filepath.IsAbs(target) {
		return fmt.Errorf("reexec: resolved target %q is not absolute", target)
	}

	return execFn(target, args, replaceGen(env, attempt+1))
}

// replaceGen returns a copy of env with exactly one genEnv entry set to gen: it
// overwrites the first occurrence, drops any duplicates, and appends the entry
// when none was present.
func replaceGen(env []string, gen int) []string {
	prefix := genEnv + "="
	want := prefix + strconv.Itoa(gen)
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			if !replaced {
				out = append(out, want)
				replaced = true
			}
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, want)
	}
	return out
}

// Run is the production entrypoint: it performs a real re-exec using PATH
// resolution (exec.LookPath), the platform execve (sysExec), and time.Sleep. On
// success it never returns; callers treat a returned error as "give up".
func Run(argv0 string, args, env []string, attempt int) error {
	return Reexec(argv0, args, env, attempt, exec.LookPath, sysExec, time.Sleep)
}
