package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/query"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router/procenv"
)

// ErrBusy is the sentinel a command role's exit code 9 maps to (Task 2.3,
// pg2-84o3m.22): commandRun.run classifies an *exec.ExitError with
// ExitCode()==9 and wraps it into the error returned through
// Executor.Dispatch, so errors.Is on that error resolves to this sentinel
// through the existing %w chain — no production interface change.
// cmd/pg-router-ccpool-handler's dispatch.go busyDeclineReason maps it to the
// wire's pre-accept busy decline (conformance.ExitBusy, reason command-busy,
// bead pg2-358u3) and the core's roleListener.Offer turns that into
// eventqueue.DeclineBusy: a graceful "not right now" PRE-ACCEPT decline
// (INV-CONC-1), never a delivery failure. The returned error carries no
// stderr tail (that suffix is for failures only).
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

// baseEnvExtras are the participant markers a strict base env keeps, by exact
// name (design section 4): run-role and the handler's own launch set them.
var baseEnvExtras = []string{"BEADS_ACTOR", "BEADS_DIR", "WORKSPACE_ROOT", "PG_ROUTER_TEST_MODE"}

// commandPayloadEnv builds the environment a command role's payload starts
// with. With r.StrictBaseEnv on, env is procenv.BaseEnv over the handler's
// environment (the minimal base env plus baseEnvExtras where set) plus the
// three PG_ROUTER_* item-context entries, and baseNames is the sorted list of
// the variable names in env. With it off, env is os.Environ() plus the three
// PG_ROUTER_* entries (they come last, so they win a duplicate) and baseNames
// is nil.
func commandPayloadEnv(r roles.Role, it item.Item) (env []string, baseNames []string) {
	itemCtx := itemContextEnv(r.Name, it)
	keys := make([]string, 0, len(itemCtx))
	for k := range itemCtx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var itemEnv []string
	for _, k := range keys {
		itemEnv = append(itemEnv, k+"="+itemCtx[k])
	}
	if !r.StrictBaseEnv {
		return append(os.Environ(), itemEnv...), nil
	}
	env = append(procenv.BaseEnv(os.Environ(), baseEnvExtras), itemEnv...)
	baseNames = make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		baseNames = append(baseNames, name)
	}
	sort.Strings(baseNames)
	return env, baseNames
}

// run dispatches a command role: render its argv, run it once in its own
// process group with the payload env, success iff exit 0. No ccpool/watchdog.
// (No built-in command role exists; this path is exercised by explicit config.)
func (r *commandRun) run(ctx context.Context, d DispatchContext) error {
	argv, err := r.renderArgv(d.Role.Command.Argv, d)
	if err != nil {
		return fmt.Errorf("command role %q: render argv: %w", d.Role.Name, err)
	}
	// The next packet (dispatch_start) records argv and the base env names from
	// here; env is the payload env, baseNames the strict env's variable names.
	env, _ := commandPayloadEnv(d.Role, d.Item)
	if _, err := r.runPayload(ctx, argv, env); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == busyExitCode {
			return fmt.Errorf("command role %q item %s: %w: %w", d.Role.Name, d.Item.ID, ErrBusy, err)
		}
		return fmt.Errorf("command role %q item %s: %w%s", d.Role.Name, d.Item.ID, err, stderrSuffix(err))
	}
	return nil
}

// runPayload runs argv with env through the commander. A Commander that can
// take an environment (query.EnvCommander, the default OSCommander) runs it with
// exactly env, in its own process group; any other Commander (a test double)
// runs through Run.
func (r *commandRun) runPayload(ctx context.Context, argv, env []string) ([]byte, error) {
	c := r.deps.commander()
	if ec, ok := c.(query.EnvCommander); ok {
		return ec.RunEnv(ctx, argv, env)
	}
	return c.Run(ctx, argv)
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
