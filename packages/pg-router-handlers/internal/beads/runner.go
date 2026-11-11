// Package beads is pg-router's local bd client. It is a small Runner/CLIRunner
// pattern rather than a heavyweight module import. The CLIRunner's Dir/Env
// carry the env scrub (the bash's top-level `unset BEADS_DIR WORKSPACE_ROOT`),
// so pg-router's own bd resolves the monorepo store from Dir, ignoring any ambient
// BEADS_DIR/WORKSPACE_ROOT inherited from a parent shell.
//
// This package is this module's own INTF-CCH-BEADS boundary crossing (docs/
// behavior/interfaces.md): the query surface (cmd/pg-router-handler-ccpool/
// query.go's queryBeadsReady, via Ready below) a beads-backed source queries
// for events, and the write path (Unclaim/AddHuman/Comment/AddLabel/
// RemoveLabel below) a handler session's completion policy
// (internal/complete, internal/watchdog) uses to write a result back to bd.
package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// DefaultTimeout bounds one bd call. Measured 2026-11-11 against the shared
// store: `bd show` and `bd list` return in about 0.5 s. A claim or write also
// goes through dolt and can take a few seconds under load, so 60 s is more than
// 100x the observed read latency yet still frees a role whose bd has wedged.
// The ccpool dispatch root context is never cancelled by design, so this
// per-call bound is the only thing that ends a hung bd.
const DefaultTimeout = 60 * time.Second

// DefaultWaitDelay is how long Run waits, after the timeout has killed bd, for
// a grandchild that inherited bd's pipes to release them.
const DefaultWaitDelay = 2 * time.Second

// Command is the bd binary this package invokes. It is exported so the pieces
// that must NAME the backing command without running it — the pre-runtime
// absent-backing-command validation of a bead-backed event source — share this
// one literal instead of duplicating it.
const Command = "bd"

// ExitError is the error Run returns when bd ran and exited non-zero. It keeps
// the exit code so callers can branch on it (see IsGuardStale).
type ExitError struct {
	Cmdline string
	Code    int
	Stderr  string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%s: exit status %d: %s", e.Cmdline, e.Code, e.Stderr)
}

// ExitCode returns bd's exit status.
func (e *ExitError) ExitCode() int { return e.Code }

// Runner shells out to `bd`. Production uses CLIRunner; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

// ErrClaimWithoutActor is returned (without spawning bd) when an invocation
// would CLAIM a bead (`--claim`, `--status in_progress`, or a non-empty
// `--assignee`) but no agent identity is resolvable. bd resolves the actor as
// --actor > $BEADS_ACTOR > git user.name > $USER, so an identity-less claim
// is recorded under the operator's name and, if never released, strands the
// bead (pg2-w2jlm, B-5; this refusal is pg2-lhi3b). Releases (`--assignee=`)
// and non-claiming writes are unaffected.
var ErrClaimWithoutActor = errors.New(
	"beads: refusing to claim without an explicit actor; set CLIRunner.Actor, pass --actor, or export BEADS_ACTOR (a claim made without one is recorded in the operator's name and can strand the bead)",
)

// CLIRunner invokes the `bd` binary from PATH.
type CLIRunner struct {
	Dir string   // working dir bd resolves its workspace from ("" = inherit cwd)
	Env []string // env block ("" / nil = inherit process env)
	// Actor is this runner's own agent identity (bead pg2-lhi3b). When
	// non-empty, Run prefixes `--actor <Actor>` to every bd invocation that
	// does not already carry its own --actor, so every write this runner makes
	// (and any claim) is attributed to it rather than to git user.name.
	// Production wiring passes the dispatching role's ccpool actor
	// (roles.CCPoolConfig.Actor) -- the same identity the role's worker
	// sessions claim under via BEADS_ACTOR.
	Actor string
	// Timeout bounds each Run call (0 = DefaultTimeout). It applies even when
	// the caller's context is context.Background().
	Timeout time.Duration
	// WaitDelay is how long to wait for bd's pipes to close after the kill
	// (0 = DefaultWaitDelay).
	WaitDelay time.Duration
	// Exec runs the bd process (nil = cmdrun.Exec); tests inject a fake.
	Exec cmdrun.Runner
}

// NewCLIRunnerForRepo returns a CLIRunner rooted at dir, with BEADS_DIR and
// WORKSPACE_ROOT scrubbed from the inherited environment. actor is the
// caller's own bd identity (may be "" for read-only/housekeeping callers with
// no role; see CLIRunner.Actor and ErrClaimWithoutActor).
func NewCLIRunnerForRepo(dir, actor string) *CLIRunner {
	return &CLIRunner{Dir: dir, Env: scrubEnv(os.Environ()), Actor: actor}
}

// hasActorArg reports whether args (up to the "--" terminator) carry an
// explicit --actor.
func hasActorArg(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--actor" || strings.HasPrefix(a, "--actor=") {
			return true
		}
	}
	return false
}

// isClaim reports whether args (up to the "--" terminator, after which
// everything is a literal positional) claim a bead: `--claim`,
// `--status in_progress`, or a non-empty `--assignee`. An empty assignee is a
// release, not a claim.
func isClaim(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch {
		case a == "--":
			return false
		case a == "--claim":
			return true
		case (a == "--status" || a == "-s") && next == "in_progress":
			return true
		case a == "--status=in_progress":
			return true
		case a == "--assignee" && next != "":
			return true
		case strings.HasPrefix(a, "--assignee=") && a != "--assignee=":
			return true
		}
	}
	return false
}

// envHasActor reports whether env (or the process env when env is nil)
// carries a non-empty BEADS_ACTOR.
func envHasActor(env []string) bool {
	if env == nil {
		env = os.Environ()
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "BEADS_ACTOR="); ok && v != "" {
			return true
		}
	}
	return false
}

// bdArgs returns the argv Run hands to bd: args prefixed with `--actor
// <Actor>` when this runner has an identity and the call carries none of its
// own. It returns ErrClaimWithoutActor when args claim a bead and no identity
// is resolvable from the call, the runner, or the environment.
func (r *CLIRunner) bdArgs(args []string) ([]string, error) {
	if hasActorArg(args) {
		return args, nil
	}
	if r.Actor != "" {
		return append([]string{"--actor", r.Actor}, args...), nil
	}
	if isClaim(args) && !envHasActor(r.Env) {
		return nil, ErrClaimWithoutActor
	}
	return args, nil
}

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "BEADS_DIR=") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Run invokes bd with the per-call timeout. When the timeout (or the caller's
// context) ends first, bd is killed and the error wraps
// context.DeadlineExceeded (or context.Canceled).
func (r *CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	args, err := r.bdArgs(args)
	if err != nil {
		return "", err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := r.Exec
	if run == nil {
		run = cmdrun.Exec
	}
	call := cmdrun.Call{Name: Command, Args: args, Dir: r.Dir, WaitDelay: r.WaitDelay}
	if call.WaitDelay <= 0 {
		call.WaitDelay = DefaultWaitDelay
	}
	if r.Env != nil {
		call.Env = r.Env
		call.EnvMode = cmdrun.EnvExact
	}
	res := run(ctx, call)
	stdout := string(res.Stdout)
	cmdline := "bd " + strings.Join(args, " ")
	switch {
	case res.StartErr != nil:
		return stdout, fmt.Errorf("%s: %w (is bd on PATH?)", cmdline, res.StartErr)
	case res.CtxErr != nil:
		return stdout, fmt.Errorf("%s: aborted after at most %s: %w: %s",
			cmdline, timeout, res.CtxErr, strings.TrimSpace(string(res.Stderr)))
	case res.Signal != "":
		return stdout, fmt.Errorf("%s: signal: %s: %s",
			cmdline, res.Signal, strings.TrimSpace(string(res.Stderr)))
	case res.Code != 0:
		return stdout, &ExitError{Cmdline: cmdline, Code: res.Code, Stderr: strings.TrimSpace(string(res.Stderr))}
	}
	return stdout, nil
}
