package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
)

// ErrBusy is the sentinel a command role's exit code 9 maps to (Task 2.3,
// pg2-84o3m.22): commandRun.run classifies an *exec.ExitError with
// ExitCode()==9 and wraps it into the error returned through
// Executor.Dispatch, so errors.Is on that error resolves to this sentinel
// through the existing %w chain — no production interface change.
// roleListener.Offer maps it to eventqueue.DeclineBusy: a graceful "not right
// now" PRE-ACCEPT decline (INV-CONC-1), never a delivery failure.
var ErrBusy = errors.New("executor: command exited busy (exit code 9)")

// busyExitCode is the command role's operator-facing "I am busy, retry me"
// signal (perf-F2 in the review digest independently proposes the core reply
// exit 9 on its OWN accept semaphore — the same code, the same meaning).
const busyExitCode = 9

type commandExecutor struct{}

func (commandExecutor) Dispatch(ctx context.Context, d DispatchContext, deps Deps) (report.Result, error) {
	r := &commandRun{deps: deps}
	return report.Result{}, r.run(ctx, d)
}

type commandRun struct{ deps Deps }

// run dispatches a command role: render its argv, run it once, success iff
// exit 0. No ccpool/watchdog. (No built-in command role exists; this path is
// exercised by explicit config.)
func (r *commandRun) run(ctx context.Context, d DispatchContext) error {
	argv, err := r.renderArgv(d.Role.Command.Argv, d)
	if err != nil {
		return fmt.Errorf("command role %q: render argv: %w", d.Role.Name, err)
	}
	if _, err := r.deps.commander().Run(ctx, argv); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == busyExitCode {
			return fmt.Errorf("command role %q item %s: %w: %w", d.Role.Name, d.Item.ID, ErrBusy, err)
		}
		return fmt.Errorf("command role %q item %s: %w%s", d.Role.Name, d.Item.ID, err, stderrSuffix(err))
	}
	return nil
}

// renderArgv interpolates each argv element through the prompt template engine, so a
// command role can reference {{.BeadID}} etc. An element with no template actions
// renders to itself.
func (r *commandRun) renderArgv(argv []string, d DispatchContext) ([]string, error) {
	pctx := prompt.Context{Item: d.Item, WorktreeDir: r.deps.Cfg.WorktreeDir, SelfLogin: r.deps.Cfg.SelfLogin, RepoRoot: r.deps.Cfg.RepoRoot}
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		t, err := prompt.Parse("argv", a)
		if err != nil {
			return nil, err
		}
		s, err := prompt.Render(t, pctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// commandStderrTailMax bounds how many bytes of a failed command role's
// stderr reach the returned error (and so the dispatch WARN line and the
// dispatch_result event). exec.Cmd.Output already caps what it retains in
// memory (first and last 16 KiB), so this is the second, log-facing bound.
const commandStderrTailMax = 2048

// stderrSuffix renders the ": stderr tail: ..." suffix for a failed command
// role's error, or "" when err carries no captured stderr. It reads only the
// *exec.ExitError's own Stderr (populated by Output() because the Commander
// leaves cmd.Stderr nil), so a SUCCESSFUL run's stderr is never read, and
// stdout and the exit code are untouched. The tail is the LAST
// commandStderrTailMax bytes (the failure reason is conventionally written
// last), redacted with failsig.Redact before cutting, then collapsed to a
// single line.
func stderrSuffix(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	tail := stderrTail(exitErr.Stderr)
	if tail == "" {
		return ""
	}
	return ": stderr tail: " + tail
}

func stderrTail(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	// Cut BEFORE redacting would risk splitting a credential in half, so
	// redact first; the redacted text is then cut to the byte bound.
	text := failsig.Redact(string(stderr))
	truncated := false
	if len(text) > commandStderrTailMax {
		text = text[len(text)-commandStderrTailMax:]
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
		truncated = true
	}
	lines := make([]string, 0, 8)
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(strings.Map(dropControl, l)); l != "" {
			lines = append(lines, l)
		}
	}
	out := strings.Join(lines, " | ")
	if out == "" {
		return ""
	}
	// Joining widens each newline to " | ", so re-apply the bound to the final text.
	if len(out) > commandStderrTailMax {
		out = out[len(out)-commandStderrTailMax:]
		for len(out) > 0 && !utf8.RuneStart(out[0]) {
			out = out[1:]
		}
		truncated = true
	}
	if truncated {
		out = "..." + out
	}
	return out
}

// dropControl removes control characters (ANSI escapes' ESC, CR, NUL, ...) so
// the tail stays a single printable line; tabs become spaces.
func dropControl(r rune) rune {
	switch {
	case r == '\t':
		return ' '
	case r < 0x20 || r == 0x7f:
		return -1
	}
	return r
}
