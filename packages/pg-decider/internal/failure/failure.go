// Package failure is the apply hook that tracks consecutive failing runs per
// (entity, rule) and escalates a persistently failing rule to a person.
//
// # State
//
// The state lives in decider annotations of the entity, written through
// apply.Annotate (origin decider:<type>-decider), under the decider's own
// namespace decider.<type>-decider.<k> (the view shows them as
// annotations.decider["<type>-decider"][<k>]):
//
//	failures.<rule id>     consecutive failing runs of the rule, decimal
//	failure_seq.<rule id>  metadata.seq of the routed item of the last counted run
//	escalated.<rule id>    1 once the streak was escalated, else 0
//
// # Counting
//
// A run counts for a rule when any action of the rule failed. A
// skipped-dependency event is neutral: it neither resets nor increments the
// counter. A run in which the rule's actions all applied or deduped resets the
// counter and the escalated record to 0 (and the recorded seq, so a later
// streak starts clean). A rule that has state in the view but produced no
// event in the run (its condition cleared) is reset the same way.
//
// The decider's own annotation writes re-route the entity, so an immediate
// re-run carrying the SAME routed seq would otherwise count one failure many
// times: the counter increments only when the run's seq differs from the
// recorded failure_seq (a run without --from-item always counts). Once
// escalated is 1 the counter stops incrementing, so a persistently failing rule
// is not rewritten on every poll.
//
// # Escalation
//
// When a failing rule's counter is at or above K (config.Config.K()) and the
// streak is not yet escalated, one human-labeled task is created naming the
// rule, the entity and the last error, parented under the entity's anchor when
// the view links one, and escalated.<rule id> is set to 1. If the create fails
// the error is returned (apply exits 2) and escalated stays 0, so the next
// failing run retries it.
//
// The tracker is the source of truth and nothing here is rolled back. The one
// residual risk is a create that succeeds followed by a failed escalated
// annotation: the next failing run then creates a second item.
package failure

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// Key prefixes (the <k> part of decider.<name>.<k>).
const (
	failuresPrefix   = "failures."
	failureSeqPrefix = "failure_seq."
	escalatedPrefix  = "escalated."
)

// Hook implements apply.Hook.
type Hook struct {
	view        *view.View
	typ, id     string
	state       map[string]string // annotations.decider[<type>-decider]
	onEscalate  func(rule string)
	annotateKey func(k string) string
}

var _ apply.Hook = (*Hook)(nil)

// New returns the failure hook for one run on the entity typ/id whose freshly
// read view is v.
func New(v *view.View, typ, id string) *Hook {
	h := &Hook{view: v, typ: typ, id: id}
	name := typ + "-decider"
	if v != nil {
		h.state = v.Annotations.Decider[name]
	}
	h.annotateKey = func(k string) string { return "decider." + name + "." + k }
	return h
}

// OnEscalate registers a callback invoked once per escalation item created, with
// the rule id (the metrics hook counts them this way).
func (h *Hook) OnEscalate(f func(rule string)) { h.onEscalate = f }

// After does nothing: the decisions are made once per run, in Finish.
func (*Hook) After(context.Context, apply.Env, apply.Event) error { return nil }

type ruleRun struct {
	failed   bool
	succeeds bool // at least one applied or deduped action
	lastErr  error
	seq      int64
	hasSeq   bool
}

// Finish updates the counters, resets cleared rules, and escalates.
func (h *Hook) Finish(ctx context.Context, env apply.Env, evs []apply.Event) error {
	runs := map[string]*ruleRun{}
	var order []string
	for _, ev := range evs {
		r := runs[ev.Action.Rule]
		if r == nil {
			r = &ruleRun{}
			runs[ev.Action.Rule] = r
			order = append(order, ev.Action.Rule)
		}
		r.seq, r.hasSeq = ev.Seq, ev.HasSeq
		switch ev.Outcome {
		case apply.OutcomeFailed:
			r.failed = true
			r.lastErr = ev.Err
		case apply.OutcomeApplied, apply.OutcomeDeduped:
			r.succeeds = true
		}
	}

	var errs []error
	for _, rule := range order {
		r := runs[rule]
		switch {
		case r.failed:
			errs = append(errs, h.fail(ctx, env, rule, r)...)
		case r.succeeds:
			errs = append(errs, h.reset(ctx, env, rule)...)
		}
		// Only skipped-dependency events: neutral.
	}
	// Rules with recorded state but no event in this run.
	for _, rule := range h.recordedRules() {
		if _, ok := runs[rule]; !ok {
			errs = append(errs, h.reset(ctx, env, rule)...)
		}
	}
	return errors.Join(errs...)
}

// recordedRules lists, sorted, the rules the view holds a nonzero counter or
// escalated record for.
func (h *Hook) recordedRules() []string {
	seen := map[string]bool{}
	for k, v := range h.state {
		for _, p := range []string{failuresPrefix, escalatedPrefix} {
			if rest, ok := strings.CutPrefix(k, p); ok && positive(v) {
				seen[rest] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func positive(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

func (h *Hook) count(rule string) int {
	n, err := strconv.Atoi(h.state[failuresPrefix+rule])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (h *Hook) write(ctx context.Context, env apply.Env, k, v string) error {
	if err := apply.Annotate(ctx, env, h.typ, h.id, h.annotateKey(k), v); err != nil {
		return fmt.Errorf("failure state %s=%s: %w", k, v, err)
	}
	return nil
}

// fail handles a run in which a rule's actions failed.
func (h *Hook) fail(ctx context.Context, env apply.Env, rule string, r *ruleRun) []error {
	if positive(h.state[escalatedPrefix+rule]) {
		return nil // already escalated: no rewrite on every poll
	}
	var errs []error
	n := h.count(rule)
	seqStr := strconv.FormatInt(r.seq, 10)
	if !r.hasSeq || h.state[failureSeqPrefix+rule] != seqStr {
		n++
		if err := h.write(ctx, env, failuresPrefix+rule, strconv.Itoa(n)); err != nil {
			return []error{err} // the count is unknown to the tracker: do not escalate on it
		}
		if r.hasSeq {
			if err := h.write(ctx, env, failureSeqPrefix+rule, seqStr); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if n < env.Config.K() {
		return errs
	}
	if err := h.escalate(ctx, env, rule, n, r.lastErr); err != nil {
		return append(errs, err)
	}
	if err := h.write(ctx, env, escalatedPrefix+rule, "1"); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func (h *Hook) escalate(ctx context.Context, env apply.Env, rule string, n int, last error) error {
	lastText := "unknown error"
	if last != nil {
		lastText = last.Error()
	}
	f := action.Fields{
		Title:       fmt.Sprintf("pg-decider: rule %s keeps failing on %s %s", rule, h.typ, h.id),
		IssueType:   "task",
		Description: fmt.Sprintf("Rule %s failed on %d consecutive runs for %s %s.\nLast error: %s\n", rule, n, h.typ, h.id, lastText),
		Labels:      []string{"human"},
	}
	if anchor, ok := workitem.BuildIndex(h.view).Anchor(); ok {
		f.Parent = anchor.ID
	}
	if _, err := apply.CreateIssue(ctx, env, f); err != nil {
		return fmt.Errorf("escalation of rule %s: %w", rule, err)
	}
	if h.onEscalate != nil {
		h.onEscalate(rule)
	}
	return nil
}

// reset clears a rule's streak: only the records that are nonzero are written.
func (h *Hook) reset(ctx context.Context, env apply.Env, rule string) []error {
	var errs []error
	if positive(h.state[failuresPrefix+rule]) {
		if err := h.write(ctx, env, failuresPrefix+rule, "0"); err != nil {
			errs = append(errs, err)
		}
	}
	if positive(h.state[escalatedPrefix+rule]) {
		if err := h.write(ctx, env, escalatedPrefix+rule, "0"); err != nil {
			errs = append(errs, err)
		}
	}
	if s := h.state[failureSeqPrefix+rule]; s != "" && s != "0" && len(errs) == 0 {
		if err := h.write(ctx, env, failureSeqPrefix+rule, "0"); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
