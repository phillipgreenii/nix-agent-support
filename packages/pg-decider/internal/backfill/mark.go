package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// The marking step of the cutover (PR lane refactor design 6.3, "Tracking and
// watching"): every PR that has an open anchor is marked WATCHED in the daemon,
// directly, with the watch option on a read (`pg-connector pr show <id> --watch`,
// no query needed); the same PRs are then refreshed with the HYDRATE-ONLY
// `pg-desk pr refresh <id>` (no decider, no apply, no tracker write); and the
// daemon's watched count over them, read with `pg-connector pr inspect`, is
// checked against the tracker's open-anchor count. The mode is `mark-watched` of
// cmd/pg-pr-backfill. It never resets a consumer cursor (packet P1-c's step) and
// never writes the tracker.

// MarkInspectChunk is the most PRs one `pr inspect` call is given (the daemon's
// limit is 2000 per call); the counts of the chunks are added.
const MarkInspectChunk = 2000

// ExitMarkCheckFailed is the exit code of `mark-watched` when every PR was marked
// and refreshed but the count check does not hold (the watched count differs from
// the open-anchor count, or that count is zero). It is the "a check does not hold"
// code `summary` uses.
const ExitMarkCheckFailed = ExitSummaryFailed

// MarkOptions configure MarkWatched.
type MarkOptions struct {
	// Set is the enumerated set (a fresh enumeration, or the persisted one); only
	// the entries that came in through an open anchor are marked.
	Set *Set
	// Connector is the pg-connector command and Desk the pg-desk command, resolved
	// on PATH when not absolute (defaults pg-connector and pg-desk). PRBackend, when
	// set, pins the PR backend instance (--backend) of the show and inspect calls.
	Connector string
	Desk      string
	PRBackend string
	// Pace is the wait between two calls of one pass; Floor and Rate are the
	// GraphQL floor and its signal, as for Run: a call is not made when the newest
	// reading is under the floor.
	Pace  time.Duration
	Floor int
	Rate  RateSource

	Env      []string
	Dir      string
	Runner   Runner
	Progress io.Writer
	Sleep    func(ctx context.Context, d time.Duration)
}

// MarkFailure is one PR a stage of the step did not complete for.
type MarkFailure struct {
	PR string
	// Stage is mark (the watch read) or refresh (the hydrate-only refresh).
	Stage  string
	Detail string
}

// MarkResult is what MarkWatched did.
type MarkResult struct {
	// Outcome is DONE, FLOOR or FAILED, the first word of Line.
	Outcome string
	// Stop is the reason when the step stopped at once, "" otherwise.
	Stop string
	// Targets are the PRs to mark: PRs with an open anchor whose PR is not merged
	// or closed.
	Targets []string
	// SkippedClosed are the open-anchor PRs not marked because the PR is merged or
	// closed; the backfill refreshes them once and the decider closes them in the
	// apply pass.
	SkippedClosed []string
	// Unmapped are the open anchors whose title names no PR: listed, never dropped.
	Unmapped []Unparseable
	// Marked and Refreshed count the PRs the two stages completed for.
	Marked, Refreshed int
	Failed            []MarkFailure
	// OpenAnchors is the tracker's open-anchor count (those mapped to a PR and
	// those not), OpenAnchorPRs the distinct PRs the mapped ones name, and Expected
	// the count the daemon's watched count must equal: OpenAnchorPRs less the
	// SkippedClosed.
	OpenAnchors, OpenAnchorPRs, Expected int
	// Watched is the daemon's watched count over the Targets; Checked says the
	// inspection ran.
	Watched int
	Checked bool
	// CheckError says why the count check does not hold, "" when it holds.
	CheckError string
	Line       string
	Code       int
}

// markEnvelope is the part of a pg-connector JSON answer the step reads.
type markEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type inspectAnswer struct {
	Counts struct {
		Requested int `json:"requested"`
		Watched   int `json:"watched"`
	} `json:"counts"`
}

func (o *MarkOptions) fill() error {
	if o.Set == nil || len(o.Set.Entries) == 0 {
		return fmt.Errorf("%w: the backfill set is empty", ErrUsage)
	}
	if o.Rate == nil {
		return fmt.Errorf("%w: no rate-limit source is configured", ErrUsage)
	}
	if o.Connector == "" {
		o.Connector = "pg-connector"
	}
	if o.Desk == "" {
		o.Desk = "pg-desk"
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
	return nil
}

// MarkTargets splits the set into the PRs to mark (an open anchor, the PR not
// merged or closed) and the open-anchor PRs skipped because the PR is merged or
// closed. An entry that came in only through a query is neither.
func MarkTargets(s *Set) (targets, skipped []string) {
	for _, e := range s.Entries {
		if !e.Has(FromAnchor) {
			continue
		}
		if e.State == StateMerged || e.State == StateClosed {
			skipped = append(skipped, e.PR)
			continue
		}
		targets = append(targets, e.PR)
	}
	return targets, skipped
}

// nothingToMark is the reason of the check when the set has no PR with an open
// anchor to mark: the count check must never pass at 0 equals 0.
const nothingToMark = "no PR with an open anchor to mark: the open-anchor count is zero, and the check never passes at 0 equals 0"

// MarkWatched marks the open-anchor PRs of the set watched, refreshes them
// (hydrate only) and checks the daemon's watched count. It returns an error only
// for a refused invocation (ErrUsage); every ending of the step is a MarkResult.
func MarkWatched(ctx context.Context, o MarkOptions) (*MarkResult, error) {
	if err := o.fill(); err != nil {
		return nil, err
	}
	res := &MarkResult{
		Outcome:       OutcomeDone,
		OpenAnchors:   o.Set.OpenAnchorCount,
		OpenAnchorPRs: o.Set.OpenAnchorPRCount,
		Unmapped:      []Unparseable{},
	}
	res.Targets, res.SkippedClosed = MarkTargets(o.Set)
	for _, u := range o.Set.Unparseable {
		if u.From == FromAnchor {
			res.Unmapped = append(res.Unmapped, u)
		}
	}
	res.Expected = res.OpenAnchorPRs - len(res.SkippedClosed)
	for _, pr := range res.SkippedClosed {
		fmt.Fprintf(o.Progress, "skip %s (its PR is merged or closed: not marked)\n", pr)
	}
	for _, u := range res.Unmapped {
		fmt.Fprintf(o.Progress, "UNMAPPED anchor %s %q: %s\n", u.ID, u.Title, u.Reason)
	}

	failedAt := map[string]bool{}
	fail := func(pr, stage, detail string) {
		res.Failed = append(res.Failed, MarkFailure{PR: pr, Stage: stage, Detail: detail})
		failedAt[pr] = true
		fmt.Fprintf(o.Progress, "fail %s (%s): %s\n", pr, stage, detail)
	}
	stop := func(reason string) {
		res.Outcome, res.Stop = OutcomeFailed, reason
	}
	// gate is the floor check before a call; it reports whether the step may go on.
	gate := func() bool {
		rd, err := o.Rate.Newest(ctx)
		if err != nil {
			stop("rate-signal-unavailable: " + firstLine(err.Error()))
			return false
		}
		if rd.Remaining < o.Floor {
			res.Outcome = OutcomeFloor
			fmt.Fprintf(o.Progress, "floor: the newest reading is %d (%s), under the floor %d\n", rd.Remaining, rd.Source, o.Floor)
			return false
		}
		return true
	}

	// 1. Mark: the watch option on a read, one PR at a time.
	marked := map[string]bool{}
	for i, pr := range res.Targets {
		if ctx.Err() != nil {
			stop("interrupted")
			break
		}
		if !gate() {
			break
		}
		args := []string{"pr", "show", pr, "--watch", "--output", "json"}
		if o.PRBackend != "" {
			args = append(args, "--backend", o.PRBackend)
		}
		cr := o.Runner(ctx, Call{Name: o.Connector, Args: args, Env: o.Env, Dir: o.Dir})
		code, detail := connectorOutcome(cr)
		switch {
		case ctx.Err() != nil:
			stop("interrupted")
		case cr.StartErr != nil:
			stop("cannot start " + o.Connector + ": " + firstLine(cr.StartErr.Error()))
		case code == "unauthenticated":
			fail(pr, "mark", detail)
			stop("unauthenticated: " + pr + " could not be read")
		case detail != "":
			fail(pr, "mark", detail)
		default:
			marked[pr] = true
			res.Marked++
			fmt.Fprintf(o.Progress, "ok %s (marked watched)\n", pr)
		}
		if res.Stop != "" {
			break
		}
		if i < len(res.Targets)-1 && o.Pace > 0 {
			o.Sleep(ctx, o.Pace)
		}
	}

	// 2. Refresh: hydrate only, the PRs the first stage marked.
	if res.Stop == "" && res.Outcome == OutcomeDone {
		var todo []string
		for _, pr := range res.Targets {
			if marked[pr] {
				todo = append(todo, pr)
			}
		}
		for i, pr := range todo {
			if ctx.Err() != nil {
				stop("interrupted")
				break
			}
			if !gate() {
				break
			}
			cr := o.Runner(ctx, Call{Name: o.Desk, Args: []string{"pr", "refresh", pr}, Env: o.Env, Dir: o.Dir})
			switch {
			case ctx.Err() != nil:
				stop("interrupted")
			case cr.StartErr != nil:
				stop("cannot start " + o.Desk + ": " + firstLine(cr.StartErr.Error()))
			case cr.Code != 0:
				fail(pr, "refresh", fmt.Sprintf("%s pr refresh exited %d: %s", o.Desk, cr.Code, firstLine(cr.Stderr+"\n"+cr.Stdout)))
			default:
				res.Refreshed++
				fmt.Fprintf(o.Progress, "ok %s (refreshed)\n", pr)
			}
			if res.Stop != "" {
				break
			}
			if i < len(todo)-1 && o.Pace > 0 {
				o.Sleep(ctx, o.Pace)
			}
		}
	}

	// 3. The count check, over every target (a PR whose mark failed counts against
	// the check, so a gap is never hidden).
	if res.Stop == "" && res.Outcome == OutcomeDone {
		o.countCheck(ctx, res, stop)
	}

	res.finishMark()
	return res, nil
}

// countCheck reads the daemon's watched count over the targets and decides whether
// it equals the tracker's open-anchor count.
func (o *MarkOptions) countCheck(ctx context.Context, res *MarkResult, stop func(string)) {
	if len(res.Targets) == 0 {
		res.CheckError = nothingToMark
		return
	}
	requested := 0
	for i := 0; i < len(res.Targets); i += MarkInspectChunk {
		end := min(i+MarkInspectChunk, len(res.Targets))
		args := append([]string{"pr", "inspect"}, res.Targets[i:end]...)
		args = append(args, "--output", "json")
		if o.PRBackend != "" {
			args = append(args, "--backend", o.PRBackend)
		}
		cr := o.Runner(ctx, Call{Name: o.Connector, Args: args, Env: o.Env, Dir: o.Dir})
		if cr.StartErr != nil {
			stop("cannot start " + o.Connector + ": " + firstLine(cr.StartErr.Error()))
			return
		}
		if _, detail := connectorOutcome(cr); detail != "" {
			res.CheckError = "the inspection failed: " + detail
			return
		}
		var env markEnvelope
		var ans inspectAnswer
		if err := json.Unmarshal([]byte(cr.Stdout), &env); err != nil || json.Unmarshal(env.Result, &ans) != nil {
			res.CheckError = "the inspection answer is not readable: " + firstLine(cr.Stdout)
			return
		}
		res.Watched += ans.Counts.Watched
		requested += ans.Counts.Requested
	}
	res.Checked = true
	switch {
	case requested != len(res.Targets):
		res.CheckError = fmt.Sprintf("the inspection answered for %d PRs, %d were asked", requested, len(res.Targets))
	case res.Expected <= 0:
		res.CheckError = nothingToMark
	case len(res.Targets) != res.Expected:
		res.CheckError = fmt.Sprintf("%d PRs were marked but the tracker's open-anchor count is %d", len(res.Targets), res.Expected)
	case res.Watched != res.Expected:
		res.CheckError = fmt.Sprintf("the daemon's watched count is %d, the tracker's open-anchor count is %d", res.Watched, res.Expected)
	}
}

// connectorOutcome reads one pg-connector JSON answer: the error code and a
// description of the failure, both "" for a success.
func connectorOutcome(cr Result) (code, detail string) {
	var env markEnvelope
	if err := json.Unmarshal([]byte(cr.Stdout), &env); err == nil && env.Error != nil {
		return env.Error.Code, env.Error.Code + ": " + env.Error.Message
	}
	if cr.Code != 0 {
		return "", fmt.Sprintf("exit %d: %s", cr.Code, firstLine(cr.Stderr+"\n"+cr.Stdout))
	}
	return "", ""
}

// finishMark renders the last line and picks the exit code.
func (r *MarkResult) finishMark() {
	if r.Stop == "" && r.Outcome == OutcomeDone && len(r.Failed) > 0 {
		r.Outcome = OutcomeFailed
	}
	var b strings.Builder
	b.WriteString(r.Outcome)
	fmt.Fprintf(&b, " mode=mark-watched open_anchors=%d open_anchor_prs=%d targets=%d skipped_closed=%d unmapped=%d marked=%d refreshed=%d failed=%d",
		r.OpenAnchors, r.OpenAnchorPRs, len(r.Targets), len(r.SkippedClosed), len(r.Unmapped), r.Marked, r.Refreshed, len(r.Failed))
	if r.Checked {
		fmt.Fprintf(&b, " watched=%d expected=%d", r.Watched, r.Expected)
	}
	if r.Stop != "" {
		fmt.Fprintf(&b, " stop=%q", r.Stop)
	}
	if r.CheckError != "" {
		fmt.Fprintf(&b, " check=%q", r.CheckError)
	}
	if len(r.Failed) > 0 {
		prs := map[string]bool{}
		for _, f := range r.Failed {
			prs[f.PR] = true
		}
		names := sortedKeys(prs)
		sort.Strings(names)
		fmt.Fprintf(&b, " failed_prs=%s", strings.Join(names, ","))
	}
	r.Line = b.String()
	switch {
	case r.Stop != "":
		r.Code = ExitStopped
		r.Outcome = OutcomeFailed
	case r.Outcome == OutcomeFloor:
		r.Code = ExitFloor
	case len(r.Failed) > 0:
		r.Code = ExitFailed
	case r.CheckError != "" && !r.Checked:
		r.Code = ExitFailed
		if len(r.Targets) == 0 {
			r.Code = ExitMarkCheckFailed
		}
	case r.CheckError != "":
		r.Code = ExitMarkCheckFailed
	default:
		r.Code = ExitDone
	}
	if r.CheckError != "" && r.Outcome == OutcomeDone {
		r.Outcome = OutcomeFailed
		r.Line = OutcomeFailed + strings.TrimPrefix(r.Line, OutcomeDone)
	}
}
