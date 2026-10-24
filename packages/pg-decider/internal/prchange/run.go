package prchange

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// DefaultLockWait bounds the wait for the per-PR lock.
const DefaultLockWait = 30 * time.Second

// countersContract marks the decider's run counters line, which is passed
// through to stderr unchanged so a log shipper that matches on it keeps
// working.
const countersContract = "pg-decider.run-counters/v1"

// Options is one invocation of the composed command.
type Options struct {
	// Mode is ModePlan or ModeApply.
	Mode string
	// RawID is the PR id as given: `<repo>#<n>` or `<entity>@<12 hex>`.
	RawID string
	// LockDir holds the per-PR lock files; LockWait bounds the wait for one.
	LockDir  string
	LockWait time.Duration
	// Config is the decider's configuration (the tracker backend and beads
	// directory the not-found escalation writes through); nil means defaults.
	Config *config.Config
	// Runner runs the subprocesses; nil means ExecRunner.
	Runner Runner
	// Stdout receives pg-desk's refresh line and the decider's plan output;
	// Stderr receives the failure line and the decider's diagnostics.
	Stdout, Stderr io.Writer
}

// Run is the composed command: lock, hydrate, check the view, decide. It
// returns the process exit code (see Table); 0 means the PR was hydrated and
// decided.
func Run(ctx context.Context, o Options) int {
	r := &run{o: o, ctx: ctx}
	if r.o.Runner == nil {
		r.o.Runner = ExecRunner
	}
	if r.o.Config == nil {
		r.o.Config = &config.Config{}
	}
	if r.o.LockWait <= 0 {
		r.o.LockWait = DefaultLockWait
	}
	return r.execute()
}

type run struct {
	o   Options
	ctx context.Context
	id  string
}

func (r *run) execute() int {
	if r.o.Mode != ModePlan && r.o.Mode != ModeApply {
		fmt.Fprintf(r.o.Stderr, "pg-pr-change: usage: the mode %q is neither plan nor apply: run pg-pr-change --help\n", r.o.Mode)
		return ExitUsage
	}
	id, err := NormalizeID(r.o.RawID)
	if err != nil {
		fmt.Fprintf(r.o.Stderr, "pg-pr-change: usage: %v: run pg-pr-change --help\n", err)
		return ExitUsage
	}
	r.id = id

	lock, err := AcquireLock(r.ctx, r.o.LockDir, id, r.o.LockWait)
	if err != nil {
		again := fmt.Sprintf("pg-pr-change %s %s", r.o.Mode, id)
		if errors.Is(err, ErrLockTimeout) {
			return r.fail(StageLock, ClassTransient, "lock wait ran out",
				fmt.Sprintf("another run holds the per-PR lock for %s", id), again)
		}
		return r.fail(StageLock, ClassHydrateError, "lock failed", err.Error(), again)
	}
	defer lock.Release()

	if code, stop := r.hydrate(); stop {
		return code
	}
	return r.decide()
}

// fail prints one failure line and returns the exit code of (stage, class).
func (r *run) fail(stage, class, what, detail, next string) int {
	spec := specFor(stage, class)
	fmt.Fprintln(r.o.Stderr, failureLine(spec, what, detail, next))
	return spec.Code
}

func (r *run) call(c Call) Result { return r.o.Runner(r.ctx, c) }

// firstLine is the first non-empty line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return strings.TrimPrefix(l, "Error: ")
		}
	}
	return ""
}

func (r *run) refreshCmd() string { return "pg-desk pr refresh " + r.id }
func (r *run) showCmd() string    { return "pg-desk pr show " + r.id + " --json" }
func (r *run) planCmd() string    { return "pg-decider plan pr " + r.id }

// hydrate refreshes the PR and checks the stored view. It returns stop true
// with the exit code when the run must end here.
func (r *run) hydrate() (int, bool) {
	res := r.call(Call{Name: "pg-desk", Args: []string{"pr", "refresh", r.id}})
	switch {
	case res.StartErr != nil:
		return r.fail(StageHydrate, ClassHydrateError, "refresh could not start", res.StartErr.Error(), r.refreshCmd()), true
	case res.Signaled:
		return r.fail(StageHydrate, ClassTransient, "refresh was stopped", "pg-desk was ended by a signal (signal: killed)", r.refreshCmd()), true
	case res.Code == 0:
		if s := strings.TrimSpace(res.Stdout); s != "" {
			fmt.Fprintln(r.o.Stdout, s)
		}
	case res.Code == 1:
		return r.fail(StageHydrate, ClassHydrateError, "refresh failed", firstLine(res.Stderr+"\n"+res.Stdout), r.refreshCmd()), true
	case res.Code == 2 || res.Code == 3:
		return r.refreshFailed(res), true
	default:
		return r.fail(StageHydrate, ClassHydrateError, "refresh failed",
			fmt.Sprintf("pg-desk pr refresh exited %d: %s", res.Code, firstLine(res.Stderr)), r.refreshCmd()), true
	}

	return r.checkView()
}

// refreshClass reads the one `class=<class>` line pg-desk prints on exit 2 and
// 3. A missing or unknown class is "" (the caller picks the safe default).
func refreshClass(stdout string) string {
	for _, l := range strings.Split(stdout, "\n") {
		c, ok := strings.CutPrefix(strings.TrimSpace(l), "class=")
		if !ok {
			continue
		}
		switch c {
		case ClassNotFound, ClassTransient, ClassDegraded, ClassUnauthenticated, ClassHydrateError:
			return c
		}
	}
	return ""
}

// refreshFailed maps a refresh exit of 2 or 3 to its failure. The decider
// never runs after one.
func (r *run) refreshFailed(res Result) int {
	detail := firstLine(res.Stderr)
	if detail == "" {
		detail = fmt.Sprintf("pg-desk pr refresh exited %d", res.Code)
	}
	class := refreshClass(res.Stdout)
	if class == "" {
		// An older pg-desk prints no class. Exit 2 is always a degraded read; an
		// exit 3 whose cause is unknown is never read as not found.
		class = ClassHydrateError
		if res.Code == 2 {
			class = ClassDegraded
		}
	}
	switch class {
	case ClassNotFound:
		return r.notFound(detail)
	case ClassTransient:
		return r.fail(StageHydrate, ClassTransient, "refresh failed", detail, r.refreshCmd())
	case ClassDegraded:
		return r.fail(StageHydrate, ClassDegraded, "refresh degraded", detail, r.refreshCmd())
	case ClassUnauthenticated:
		return r.fail(StageHydrate, ClassUnauthenticated, "refresh refused",
			detail+" (the credential is missing or invalid; this needs a person)", "pg-connector auth status")
	default:
		return r.fail(StageHydrate, ClassHydrateError, "refresh failed", detail, r.refreshCmd())
	}
}

// readView reads the stored composite view of the PR. On failure it returns the
// class to report and the detail.
func (r *run) readView() (v *view.View, class, detail string) {
	return viewFromResult(r.call(Call{Name: "pg-desk", Args: []string{"pr", "show", r.id, "--json"}}))
}

// viewFromResult decodes the result of `pg-desk pr show <id> --json`.
func viewFromResult(res Result) (v *view.View, class, detail string) {
	switch {
	case res.StartErr != nil:
		return nil, ClassHydrateError, res.StartErr.Error()
	case res.Signaled:
		return nil, ClassTransient, "pg-desk pr show was ended by a signal (signal: killed)"
	case res.Code != 0:
		return nil, ClassTransient, fmt.Sprintf("pg-desk pr show exited %d: %s", res.Code, firstLine(res.Stderr))
	}
	v, err := view.Parse([]byte(res.Stdout))
	if err != nil {
		return nil, ClassHydrateError, err.Error()
	}
	return v, "", ""
}

// checkView fails closed: an unhydrated or stale view is refused, so the decider
// can never mint an anchor from it. A legacy row has version 0.
func (r *run) checkView() (int, bool) {
	v, class, detail := r.readView()
	if v == nil {
		return r.fail(StageHydrate, class, "the stored view cannot be read", detail, r.showCmd()), true
	}
	if v.Version == 0 || v.Stale {
		why := fmt.Sprintf("the stored view of %s is at version %d", r.id, v.Version)
		if v.Stale {
			why += " and marked stale"
		}
		return r.fail(StageHydrate, ClassUnhydrated, "view refused", why+"; the decider was not run", r.refreshCmd()), true
	}
	return 0, false
}

// deciderArgs is the decider's argv for the run.
func (r *run) deciderArgs() []string {
	return []string{r.o.Mode, "pr", r.id}
}

// decide runs the decider and maps its exit. Its stderr is re-wrapped: the run
// counters line passes through unchanged, and every other line becomes part of
// this command's own failure line (or, on success, a prefixed diagnostic).
func (r *run) decide() int {
	res := r.call(Call{Name: "pg-decider", Args: r.deciderArgs()})
	counters, others := splitDeciderStderr(res.Stderr)
	for _, l := range counters {
		fmt.Fprintln(r.o.Stderr, l)
	}
	if res.Stdout != "" {
		fmt.Fprint(r.o.Stdout, res.Stdout)
	}
	switch {
	case res.StartErr != nil:
		return r.fail(StageDecide, ClassDecideFailed, "the decider could not start", res.StartErr.Error(), r.planCmd())
	case res.Signaled:
		return r.fail(StageDecide, ClassDecidePartial, "the decider was stopped", "pg-decider was ended by a signal (signal: killed)", r.planCmd())
	case res.Code == 0:
		for _, l := range others {
			fmt.Fprintf(r.o.Stderr, "pg-pr-change: decider: %s\n", l)
		}
		return ExitOK
	}
	detail := strings.Join(others, "; ")
	if detail == "" {
		detail = fmt.Sprintf("pg-decider exited %d with no diagnostic", res.Code)
	}
	if res.Code == 2 {
		return r.fail(StageDecide, ClassDecidePartial, "the decider applied only part of its plan", detail, r.planCmd())
	}
	return r.fail(StageDecide, ClassDecideFailed, "the decider failed", fmt.Sprintf("exit %d: %s", res.Code, detail), r.planCmd())
}

// splitDeciderStderr separates the run counters line from every other line.
func splitDeciderStderr(stderr string) (counters, others []string) {
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.TrimSpace(l) == "":
		case strings.Contains(l, countersContract):
			counters = append(counters, l)
		default:
			others = append(others, strings.TrimSpace(l))
		}
	}
	return counters, others
}
