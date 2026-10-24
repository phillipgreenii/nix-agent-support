package backfill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// The three endings of `run`, the first word of its last line, and the exit code
// of each. None is 1 (a Go program's unexpected-error code) or 9 (the router's
// "busy, retry" code). A FAILED that stopped at once (a create limit, a run
// that ended `unauthenticated` or `decide_failed`, an unreadable rate signal) has
// its own exit code, so the operator's script can tell "run it again" from "stop
// and roll back". ExitUsage is a bad invocation, before anything ran.
const (
	OutcomeDone   = "DONE"
	OutcomeFloor  = "FLOOR"
	OutcomeFailed = "FAILED"

	ExitDone    = 0
	ExitFloor   = 10
	ExitFailed  = 11
	ExitStopped = 12
	ExitUsage   = 64
)

// ExitClass is how the driver reads one exit code of the composed command
// (prchange.Table; a test in internal/prchange pins this table to it).
type ExitClass struct {
	Class string
	// Stop ends the run at once. Final means the PR's result is final: a
	// resume does not run it again.
	Stop, Final bool
}

// ExitClasses maps the composed command's failure exit codes to what the driver
// does. A code not in the table is a failure of class `unknown` that the run
// records and continues past; 0 is success.
var ExitClasses = map[int]ExitClass{
	20: {Class: "transient"},
	21: {Class: "degraded"},
	22: {Class: "not_found", Final: true},
	23: {Class: "unauthenticated", Stop: true},
	24: {Class: "hydrate_error"},
	25: {Class: "unhydrated"},
	30: {Class: "decide_partial"},
	31: {Class: "decide_failed", Stop: true},
	40: {Class: "transient"},
	41: {Class: "hydrate_error"},
	64: {Class: "usage", Stop: true},
}

// DefaultPace is the wait between two PRs of one pass.
const DefaultPace = 2 * time.Second

// RunOptions configure Run.
type RunOptions struct {
	Mode string
	Set  *Set
	// RunRecord is the file the composed command appends its records to; the
	// driver reads it to resume and to count creates, and passes it on.
	RunRecord string
	// ChangeCmd is the composed command (default pg-pr-change); ChangeArgs are
	// extra flags put before the mode (the lock directory and wait).
	ChangeCmd  string
	ChangeArgs []string
	Pace       time.Duration
	// Floor is the GraphQL budget below which the run ends FLOOR (default
	// DefaultFloor).
	Floor int
	Rate  RateSource
	// Limits are the operator's expected creates per kind, a STOP criterion:
	// after each PR of an apply pass the creates the pass has made since the flip
	// are compared with them, and the run stops at once when any kind is over
	// (a kind with no entry has limit 0). They are not a cap: nothing prevents a
	// create; the run stops after the PR that exceeded them. An apply run
	// without Limits is refused unless NoLimits says so.
	Limits   map[string]int
	NoLimits bool
	// NewPass starts a new pass of this mode: the records of earlier passes no
	// longer satisfy a resume.
	NewPass  bool
	Env      []string
	Dir      string
	Runner   Runner
	Progress io.Writer
	Sleep    func(ctx context.Context, d time.Duration)
	Now      func() time.Time
}

// Counts are the numbers of the last line.
type Counts struct {
	Total    int
	Done     int
	Skipped  int
	NotFound int
	// Failed counts the PRs that ended in a failure this run; Remaining the PRs
	// not reached because the run ended early.
	Failed    int
	Remaining int
}

// FailedPR is one PR that ended in a failure.
type FailedPR struct {
	PR    string
	Class string
	Code  int
}

// RunResult is what Run did.
type RunResult struct {
	Outcome string
	// Stop is the reason when the run stopped at once, "" otherwise.
	Stop   string
	Counts Counts
	Failed []FailedPR
	// Newest is the newest rate reading the run saw.
	Newest *Reading
	// Line is the last line to print; Code the exit code.
	Line string
	Code int
}

// ErrUsage marks an invocation Run refuses before running anything.
var ErrUsage = errors.New("usage")

func (o *RunOptions) fill() error {
	if o.Mode != ModePlan && o.Mode != ModeApply {
		return fmt.Errorf("%w: --mode must be plan or apply, got %q", ErrUsage, o.Mode)
	}
	if o.Set == nil || len(o.Set.Entries) == 0 {
		return fmt.Errorf("%w: the backfill set is empty", ErrUsage)
	}
	if o.Rate == nil {
		return fmt.Errorf("%w: no rate-limit source is configured", ErrUsage)
	}
	if o.RunRecord == "" {
		return fmt.Errorf("%w: no run record file is configured", ErrUsage)
	}
	if o.Mode == ModeApply && o.Limits == nil && !o.NoLimits {
		return fmt.Errorf("%w: an apply pass needs the operator's create limits per kind (--max-creates kind=n,... or --limits-file), or --no-create-limits to run without the stop criterion", ErrUsage)
	}
	if o.ChangeCmd == "" {
		o.ChangeCmd = "pg-pr-change"
	}
	if o.Floor <= 0 {
		o.Floor = DefaultFloor
	}
	if o.Runner == nil {
		o.Runner = ExecRunner
	}
	if o.Progress == nil {
		o.Progress = io.Discard
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return nil
}

// finished reports whether the log holds a final result of mode for pr after
// the start of the current pass (file position since): a success, or a not-found
// (which a re-run cannot change).
func finished(lg *Log, mode, pr string, since int) bool {
	for _, r := range lg.Records {
		if r.Mode != mode || r.Entity != pr || r.Seq <= since {
			continue
		}
		if r.ExitCode == 0 || ExitClasses[r.ExitCode].Final {
			return true
		}
	}
	return false
}

// Run runs the composed command over the set, paced. It returns an error only for
// a refused invocation (ErrUsage) or an unreadable run record; every ending of the
// run itself is a RunResult.
func Run(ctx context.Context, o RunOptions) (*RunResult, error) {
	if err := o.fill(); err != nil {
		return nil, err
	}
	if o.NewPass {
		m := Marker{Contract: PassStartContract, Kind: "pass-start", At: o.Now().UTC().Format(time.RFC3339), Mode: o.Mode}
		if err := AppendLine(o.RunRecord, m); err != nil {
			return nil, err
		}
	}
	lg, err := ReadLog(o.RunRecord)
	if err != nil {
		return nil, err
	}
	since, _ := lg.PassStart(o.Mode)

	res := &RunResult{Outcome: OutcomeDone}
	res.Counts.Total = len(o.Set.Entries)
	failedAt := map[string]bool{}
	ids := map[string]bool{}
	for _, e := range o.Set.Entries {
		ids[e.PR] = true
	}

	stop := func(reason string) *RunResult {
		res.Outcome, res.Stop = OutcomeFailed, reason
		return res
	}
	last := len(o.Set.Entries) - 1
	for i, e := range o.Set.Entries {
		if ctx.Err() != nil {
			res.Counts.Remaining = len(o.Set.Entries) - i
			stop("interrupted")
			break
		}
		if finished(lg, o.Mode, e.PR, since) {
			res.Counts.Skipped++
			fmt.Fprintf(o.Progress, "skip %s (a %s result is already recorded)\n", e.PR, o.Mode)
			continue
		}
		rd, err := o.Rate.Newest(ctx)
		if err != nil {
			res.Counts.Remaining = len(o.Set.Entries) - i
			stop("rate-signal-unavailable: " + firstLine(err.Error()))
			break
		}
		res.Newest = &rd
		if rd.Remaining < o.Floor {
			res.Outcome = OutcomeFloor
			res.Counts.Remaining = len(o.Set.Entries) - i
			break
		}

		args := append([]string{}, o.ChangeArgs...)
		args = append(args, "--run-record", o.RunRecord, o.Mode, e.PR)
		cr := o.Runner(ctx, Call{Name: o.ChangeCmd, Args: args, Env: o.Env, Dir: o.Dir})
		switch {
		case ctx.Err() != nil:
			res.Counts.Remaining = len(o.Set.Entries) - i
			stop("interrupted")
		case cr.StartErr != nil:
			res.Counts.Remaining = len(o.Set.Entries) - i
			stop("cannot start " + o.ChangeCmd + ": " + firstLine(cr.StartErr.Error()))
		case cr.Code == 0:
			res.Counts.Done++
			fmt.Fprintf(o.Progress, "ok %s\n", e.PR)
		default:
			cls, known := ExitClasses[cr.Code]
			if !known {
				cls = ExitClass{Class: "unknown"}
			}
			fmt.Fprintf(o.Progress, "fail %s: exit %d (%s): %s\n", e.PR, cr.Code, cls.Class, firstLine(cr.Stderr))
			if cls.Final {
				res.Counts.NotFound++
			} else if !failedAt[e.PR] {
				failedAt[e.PR] = true
				res.Counts.Failed++
				res.Failed = append(res.Failed, FailedPR{PR: e.PR, Class: cls.Class, Code: cr.Code})
			}
			if cls.Stop {
				res.Counts.Remaining = len(o.Set.Entries) - i - 1
				stop(fmt.Sprintf("%s: %s ended %s (exit %d)", cls.Class, e.PR, o.Mode, cr.Code))
			}
		}
		if res.Stop != "" {
			break
		}

		// The create limits are checked after every PR of an apply pass.
		if o.Mode == ModeApply && !o.NoLimits {
			lg2, err := ReadLog(o.RunRecord)
			if err != nil {
				return nil, err
			}
			if kind, n, limit, over := OverLimit(CreatesSinceFlip(lg2, ids), o.Limits); over {
				res.Counts.Remaining = len(o.Set.Entries) - i - 1
				stop(fmt.Sprintf("create-limit: kind %s has %d creates, the expected number is %d", kind, n, limit))
				break
			}
		}
		if i < last && o.Pace > 0 {
			o.Sleep(ctx, o.Pace)
		}
	}
	if res.Stop == "" && res.Outcome == OutcomeDone && len(res.Failed) > 0 {
		res.Outcome = OutcomeFailed
	}
	res.finish(o)
	return res, nil
}

// finish renders the last line and picks the exit code.
func (r *RunResult) finish(o RunOptions) {
	c := r.Counts
	var b strings.Builder
	b.WriteString(r.Outcome)
	fmt.Fprintf(&b, " mode=%s total=%d done=%d skipped=%d not_found=%d failed=%d remaining=%d", o.Mode, c.Total, c.Done, c.Skipped, c.NotFound, c.Failed, c.Remaining)
	if r.Outcome == OutcomeFloor && r.Newest != nil {
		fmt.Fprintf(&b, " floor=%d newest=%d source=%s", o.Floor, r.Newest.Remaining, r.Newest.Source)
	}
	if r.Stop != "" {
		fmt.Fprintf(&b, " stop=%q", r.Stop)
	}
	byClass := map[string][]string{}
	for _, f := range r.Failed {
		byClass[f.Class] = append(byClass[f.Class], f.PR)
	}
	for _, cls := range sortedKeys(byClass) {
		sort.Strings(byClass[cls])
		fmt.Fprintf(&b, " %s=%s", cls, strings.Join(byClass[cls], ","))
	}
	r.Line = b.String()
	switch {
	case r.Stop != "":
		r.Code = ExitStopped
	case r.Outcome == OutcomeFloor:
		r.Code = ExitFloor
	case r.Outcome == OutcomeFailed:
		r.Code = ExitFailed
	default:
		r.Code = ExitDone
	}
}
