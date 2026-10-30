package prchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
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
	// Seq is the change sequence argument as the router rendered it into the
	// role's argv: a number (possibly in exponent form), or empty or `<no value>`
	// when the item carries none. See ResolveKey.
	Seq string
	// RunRecordPath is the file one JSON record per run is appended to; empty
	// means no record is written (only a test of the composition logic leaves it
	// empty).
	RunRecordPath string
	// Clock stamps the record; nil means time.Now. NewRunID makes the run id; nil
	// means a random one.
	Clock    func() time.Time
	NewRunID func() string
	// LogSkips raises the per-rule log line of a skipped rule from debug (not
	// printed) to info. Lines of rules that planned or applied actions are always
	// printed.
	LogSkips bool
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
	if r.o.Clock == nil {
		r.o.Clock = time.Now
	}
	if r.o.NewRunID == nil {
		r.o.NewRunID = newRunID
	}
	return r.execute()
}

type run struct {
	o   Options
	ctx context.Context
	id  string

	// key is the delivery key of the run (see ResolveKey).
	key DeliveryKey
	// view is the stored view the decider decides on, read after a successful
	// refresh.
	view *view.View
	// rec is the run record under construction; failStage and failClass are set
	// by fail.
	rec                  RunRecord
	failStage, failClass string
	// residual is the number of actions a read-only plan still lists after the
	// run (apply) or that the plan lists (plan); nil when not learned.
	residual  *int
	decisions []decision
	// lookupItems and lookupCeiling are the decider's work-item lookup figures,
	// when it reported them (see lookupFromDecider).
	lookupItems, lookupCeiling *int64
	// planItems and planCeiling are the same figures as read from a read-only
	// `plan --json` of the run (the plan mode's only source: the decider's plan
	// prints no counters line).
	planItems, planCeiling *int64
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
	key, err := ResolveKey(r.o.Seq, r.o.RawID)
	if err != nil {
		fmt.Fprintf(r.o.Stderr, "pg-pr-change: usage: %v: run pg-pr-change --help\n", err)
		return ExitUsage
	}
	r.key = key
	r.startRecord()

	lock, err := AcquireLock(r.ctx, r.o.LockDir, id, r.o.LockWait)
	if err != nil {
		again := fmt.Sprintf("pg-pr-change %s %s", r.o.Mode, id)
		var code int
		if errors.Is(err, ErrLockTimeout) {
			code = r.fail(StageLock, ClassTransient, "lock wait ran out",
				fmt.Sprintf("another run holds the per-PR lock for %s", id), again)
		} else {
			code = r.fail(StageLock, ClassHydrateError, "lock failed", err.Error(), again)
		}
		r.finish(code)
		return code
	}
	defer lock.Release()

	code := r.hydrateAndDecide()
	// The run is recorded while the lock is still held: the PR's own lane state is
	// written only by the run that holds its lock.
	r.finish(code)
	return code
}

func (r *run) hydrateAndDecide() int {
	if code, stop := r.hydrate(); stop {
		return code
	}
	return r.decide()
}

// fail prints one failure line and returns the exit code of (stage, class).
func (r *run) fail(stage, class, what, detail, next string) int {
	spec := specFor(stage, class)
	r.failStage, r.failClass = stage, class
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
	r.rec.Hydrate.Result = hydrateFailed
	if res.StartErr == nil && !signaled(res) {
		code := res.Code
		r.rec.Hydrate.ExitCode = &code
		r.rec.Hydrate.Line = oneLine(string(res.Stdout), 200)
	}
	switch {
	case res.StartErr != nil:
		return r.fail(StageHydrate, ClassHydrateError, "refresh could not start", res.StartErr.Error(), r.refreshCmd()), true
	case signaled(res):
		return r.fail(StageHydrate, ClassTransient, "refresh was stopped", "pg-desk was ended by a signal (signal: killed)", r.refreshCmd()), true
	case res.Code == 0:
		if s := strings.TrimSpace(string(res.Stdout)); s != "" {
			fmt.Fprintln(r.o.Stdout, s)
		}
	case res.Code == 1:
		return r.fail(StageHydrate, ClassHydrateError, "refresh failed", firstLine(string(res.Stderr)+"\n"+string(res.Stdout)), r.refreshCmd()), true
	case res.Code == 2 || res.Code == 3:
		return r.refreshFailed(res), true
	default:
		return r.fail(StageHydrate, ClassHydrateError, "refresh failed",
			fmt.Sprintf("pg-desk pr refresh exited %d: %s", res.Code, firstLine(string(res.Stderr))), r.refreshCmd()), true
	}

	code, stop := r.checkView()
	if !stop {
		r.rec.Hydrate.Result = hydrateOK
	}
	return code, stop
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
	detail := firstLine(string(res.Stderr))
	if detail == "" {
		detail = fmt.Sprintf("pg-desk pr refresh exited %d", res.Code)
	}
	class := refreshClass(string(res.Stdout))
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
	case signaled(res):
		return nil, ClassTransient, "pg-desk pr show was ended by a signal (signal: killed)"
	case res.Code != 0:
		return nil, ClassTransient, fmt.Sprintf("pg-desk pr show exited %d: %s", res.Code, firstLine(string(res.Stderr)))
	}
	v, err := view.Parse(res.Stdout)
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
	r.view = v
	return 0, false
}

// deciderCall is the decider's call for the run. In apply mode with a delivery
// key the call carries `--from-item -` and the minimal routed item on its
// standard input, so the decider's audit comments carry the delivery key and
// its escalate_after counter counts one delivery once. plan has no such flag,
// and a run with no key passes none (counting every run is then correct).
func (r *run) deciderCall() Call {
	c := Call{Name: "pg-decider", Args: []string{r.o.Mode, "pr", r.id}}
	if r.o.Mode == ModeApply && r.key.Has() {
		c.Args = append(c.Args, "--from-item", "-")
		c.Stdin = []byte(RoutedItemJSON(r.o.RawID, r.id, r.key))
	}
	return c
}

// readPlan runs the decider's read-only `plan pr <id> --json`. It writes
// nothing, so it is safe before and after an apply. The error text is non-empty
// when the plan could not be read.
func (r *run) readPlan() (*action.PlanResult, string) {
	res := r.call(Call{Name: "pg-decider", Args: []string{"plan", "pr", r.id, "--json"}})
	switch {
	case res.StartErr != nil:
		return nil, res.StartErr.Error()
	case signaled(res):
		return nil, "pg-decider plan --json was ended by a signal"
	case res.Code != 0:
		return nil, fmt.Sprintf("pg-decider plan --json exited %d: %s", res.Code, firstLine(string(res.Stderr)))
	}
	p, err := parsePlanJSON(string(res.Stdout))
	if err != nil {
		return nil, "the plan is not readable JSON: " + err.Error()
	}
	if items, ceiling := lookupFromPlanJSON(string(res.Stdout)); items != nil || ceiling != nil {
		r.planItems, r.planCeiling = items, ceiling
	}
	return p, ""
}

// decide runs the decider and maps its exit, then fills in what the run record
// and the lane state need: the plan (the actions and skips, read-only), the
// decider's apply counters, and the residual after the run.
func (r *run) decide() int {
	rec := &DecideRecord{Rules: []RuleOutcome{}, Actions: []PlannedAction{}, ByKind: map[string]int{}}
	r.rec.Decide = rec

	// In apply mode the plan is read BEFORE the apply: it says which operation
	// each action has, which the apply counters (per rule) do not.
	var pre *action.PlanResult
	if r.o.Mode == ModeApply {
		pre, rec.PlanError = r.readPlan()
	}
	res := r.call(r.deciderCall())
	code := r.mapDecide(res)
	rec.ExitCode = res.Code
	if res.StartErr != nil || signaled(res) {
		return code
	}

	switch {
	case r.o.Mode == ModePlan && res.Code == 0:
		// The plan the decider printed is the plan; its JSON form is the record.
		pre, rec.PlanError = r.readPlan()
		if pre != nil {
			n := len(pre.Actions)
			r.residual = &n
		}
	case r.o.Mode == ModeApply && (res.Code == 0 || res.Code == 2):
		// The residual after an apply: a read-only plan, never a recorded run.
		if post, msg := r.readPlan(); post != nil {
			n := len(post.Actions)
			r.residual = &n
		} else {
			r.rec.ResidualError = msg
		}
	}

	counters := parseCounters(string(res.Stderr))
	rec.Actions, rec.ByKind = plannedActions(pre)
	rec.Rules = ruleOutcomes(r.o.Mode, pre, counters)
	r.decisions = decisionsFor(r.o.Mode, pre, counters)
	r.logRules(rec.Rules)
	r.rec.Adoption = adoptionOutcome(r.o.Mode, r.view, pre, counters)
	r.lookupItems, r.lookupCeiling = lookupFromDecider(res)
	if r.lookupItems == nil && r.lookupCeiling == nil {
		r.lookupItems, r.lookupCeiling = r.planItems, r.planCeiling
	}
	return code
}

// lookupFromDecider extracts the decider's work-item lookup item count and its
// ceiling from the deciding call, when it reports them: the `lookup_items` and
// `lookup_ceiling` members of the run counters line on its stderr (an apply, a
// failed-closed plan or apply: that line carries them as far as they are known
// on the failure path too), else the same two members of a `--json` plan on its
// stdout. A plan in text form prints them in prose only, so a plan run takes
// them from the read-only `plan --json` that follows (see readPlan). The lane
// write path call forwards the two values; a test replaces this function to
// show that with a stub.
var lookupFromDecider = func(res Result) (items, ceiling *int64) {
	if c := parseCounters(string(res.Stderr)); c != nil && (c.LookupItems != nil || c.LookupCeiling != nil) {
		return intp64(c.LookupItems), intp64(c.LookupCeiling)
	}
	return lookupFromPlanJSON(string(res.Stdout))
}

func intp64(p *int) *int64 {
	if p == nil {
		return nil
	}
	n := int64(*p)
	return &n
}

// lookupFromPlanJSON reads lookup_items and lookup_ceiling out of a decider
// plan printed with --json; nil, nil for anything else (a text plan, an older
// decider that prints neither member).
func lookupFromPlanJSON(stdout string) (items, ceiling *int64) {
	var p struct {
		Items   *int `json:"lookup_items"`
		Ceiling *int `json:"lookup_ceiling"`
	}
	if json.Unmarshal([]byte(stdout), &p) != nil {
		return nil, nil
	}
	return intp64(p.Items), intp64(p.Ceiling)
}

// mapDecide maps the decider's exit. Its stderr is re-wrapped: the run counters
// line passes through unchanged, and every other line becomes part of this
// command's own failure line (or, on success, a prefixed diagnostic).
func (r *run) mapDecide(res Result) int {
	counters, others := splitDeciderStderr(string(res.Stderr))
	for _, l := range counters {
		fmt.Fprintln(r.o.Stderr, l)
	}
	if len(res.Stdout) > 0 {
		r.o.Stdout.Write(res.Stdout)
	}
	switch {
	case res.StartErr != nil:
		return r.fail(StageDecide, ClassDecideFailed, "the decider could not start", res.StartErr.Error(), r.planCmd())
	case signaled(res):
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
	if res.Code == exitcode.LookupUntrusted && lookupUnavailable(string(res.Stderr)) {
		// The decider failed closed because the tracker could not be read, and
		// wrote nothing: retryable, unlike a listing above the ceiling or a
		// truncated one, which a retry cannot clear.
		return r.fail(StageDecide, ClassTransient, "the decider's work-item lookup could not be read", detail, r.planCmd())
	}
	if res.Code == 2 {
		return r.fail(StageDecide, ClassDecidePartial, "the decider applied only part of its plan", detail, r.planCmd())
	}
	return r.fail(StageDecide, ClassDecideFailed, "the decider failed", fmt.Sprintf("exit %d: %s", res.Code, detail), r.planCmd())
}

// lookupUnavailable reports whether the decider's run counters line names the
// failed-closed reason lookup-unavailable.
func lookupUnavailable(stderr string) bool {
	c := parseCounters(stderr)
	return c != nil && c.Failures[apply.LookupReasonUnavailable] > 0
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

// logRules prints one line per rule of the run (rule, outcome, reason, facts,
// event id, run id) to stderr, so why an action was or was not taken can be read
// without querying pg-desk. Skipped rules are debug level and printed only when
// Options.LogSkips is set; rules that planned or applied actions are info.
func (r *run) logRules(rules []RuleOutcome) {
	for _, ro := range rules {
		if ro.Outcome == outcomeSkipped && ro.Counts == nil && !r.o.LogSkips {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "pg-pr-change: rule=%s outcome=%s", ro.Rule, ro.Outcome)
		if ro.Reason != "" {
			fmt.Fprintf(&b, " reason=%q", ro.Reason)
		}
		if len(ro.Facts) > 0 {
			if j, err := json.Marshal(ro.Facts); err == nil {
				fmt.Fprintf(&b, " facts=%s", j)
			}
		}
		fmt.Fprintf(&b, " event=%s run=%s", r.o.RawID, r.rec.RunID)
		fmt.Fprintln(r.o.Stderr, b.String())
	}
}
