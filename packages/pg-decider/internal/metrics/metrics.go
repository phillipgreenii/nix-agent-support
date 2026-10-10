// Package metrics is the apply hook that reports, once per run, how many
// actions each rule planned, applied, deduped, failed and had skipped, plus how
// many escalations the run created.
//
// # Surface
//
// One JSON line on the run's stderr (apply.Env.Stderr, os.Stderr when nil),
// emitted in Finish, so every run emits exactly one, including a run that
// planned nothing. The line is the versioned contract Contract:
//
//	{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/widgets#42",
//	 "rules":{"<rule id>":{"planned":N,"applied":N,"deduped":N,"failed":N,"skipped":N}},
//	 "escalations":N}
//
// Additive members, each omitted when it does not apply, so a line without
// focus activity keeps the shape above:
//
//   - per rule, "transitions": {"<mint|hold|release|hold_terminal>":
//     {"<outcome>":N}}, the focus.item rule's actions counted by the
//     facts.transition they carry and the outcome word they ended in (applied,
//     deduped, failed, skipped-dependency, skipped-stale), for example
//     {"hold":{"applied":1,"skipped-stale":1}};
//   - per rule, "skips": {"<cause>":N}, every skip of a source that has a focus
//     bead (the skip's facts carry "bead"), counted by facts.cause, so a strike
//     that never takes effect still emits a counter. The rule then appears in
//     "rules" with zero planned;
//   - line level, "seq" and "from_item": the change_log sequence and the id of
//     the routed item that triggered the run (omitted without --from-item), so
//     a pg-desk select, the decider's actions and a bead hold join through
//     run_id and seq;
//   - line level, "failures": {"<reason>":1}, the counted failure of a run
//     that failed closed (EmitFailureLine), which has no rules and writes
//     nothing.
//
// planned is every action of the rule (planned = applied + deduped + failed +
// skipped); skipped counts skipped-dependency outcomes, which are not failures.
// escalations is the run's total. "rules" is an object, never null, and the
// encoder sorts its keys. Members may be added to v1; none will be removed or
// change meaning. The line is a single line so a log shipper can grep for the
// contract string.
//
// The metrics hook is installed AFTER the failure hook, whose escalations it
// counts through Escalated.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/item"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// Contract identifies the line's shape and version.
const Contract = "pg-decider.run-counters/v1"

// FocusRuleID is the rule whose transitions and skips are counted.
const FocusRuleID = "focus.item"

// RuleCounters is one rule's counts for the run. Transitions and Skips are
// additive members, omitted when empty.
type RuleCounters struct {
	Planned int `json:"planned"`
	Applied int `json:"applied"`
	Deduped int `json:"deduped"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	// Transitions counts the rule's actions by facts.transition, then by the
	// outcome word they ended in.
	Transitions map[string]map[string]int `json:"transitions,omitempty"`
	// Skips counts the rule's skips of a source with a focus bead by cause.
	Skips map[string]int `json:"skips,omitempty"`
}

// Line is the emitted JSON object.
type Line struct {
	Contract    string                  `json:"contract"`
	Type        string                  `json:"type"`
	ID          string                  `json:"id"`
	Rules       map[string]RuleCounters `json:"rules"`
	Escalations int                     `json:"escalations"`
	// Seq and FromItem identify the routed item that triggered the run.
	Seq      int64  `json:"seq,omitempty"`
	FromItem string `json:"from_item,omitempty"`
	// Failures counts a run that failed closed, by reason.
	Failures map[string]int `json:"failures,omitempty"`
}

// transitions is the closed set of facts.transition values counted.
var transitions = map[string]bool{"mint": true, "hold": true, "release": true, "hold_terminal": true}

// Hook implements apply.Hook.
type Hook struct {
	typ, id     string
	escalations int
	seq         int64
	fromItem    string
	skipped     []action.Skip
}

var _ apply.Hook = (*Hook)(nil)

// New returns the metrics hook for one run on the entity typ/id. The view is
// accepted for symmetry with the failure hook; the counters are derived from
// the run's events.
func New(_ *view.View, typ, id string) *Hook { return &Hook{typ: typ, id: id} }

// Routed records the routed item that triggered the run (nil without
// --from-item), so the line carries its seq and id.
func (h *Hook) Routed(it *item.Routed) {
	if it == nil {
		return
	}
	h.seq, h.fromItem = it.Metadata.Seq, it.ID
}

// Skipped hands the hook the plan's skips, read once per run: the focus.item
// skips of a source that has a focus bead are counted by cause.
func (h *Hook) Skipped(skips []action.Skip) { h.skipped = skips }

// Escalated records one escalation created by the run.
func (h *Hook) Escalated(string) { h.escalations++ }

// After does nothing.
func (*Hook) After(context.Context, apply.Env, apply.Event) error { return nil }

// Counters aggregates evs per rule.
func Counters(evs []apply.Event) map[string]RuleCounters {
	out := map[string]RuleCounters{}
	for _, ev := range evs {
		c := out[ev.Action.Rule]
		c.Planned++
		if ev.Action.Rule == FocusRuleID {
			if t, _ := ev.Action.Facts["transition"].(string); transitions[t] {
				if c.Transitions == nil {
					c.Transitions = map[string]map[string]int{}
				}
				if c.Transitions[t] == nil {
					c.Transitions[t] = map[string]int{}
				}
				c.Transitions[t][string(ev.Outcome)]++
			}
		}
		switch ev.Outcome {
		case apply.OutcomeApplied:
			c.Applied++
		case apply.OutcomeDeduped:
			c.Deduped++
		case apply.OutcomeFailed:
			c.Failed++
		case apply.OutcomeSkippedDependency, apply.OutcomeSkippedStale:
			c.Skipped++
		}
		out[ev.Action.Rule] = c
	}
	return out
}

// countSkips adds the focus.item skips of a source with a focus bead to out,
// keyed by facts.cause (the skip's own reason when it carries no cause).
func countSkips(out map[string]RuleCounters, skips []action.Skip) {
	for _, sk := range skips {
		if sk.Rule != FocusRuleID {
			continue
		}
		if _, hasBead := sk.Facts["bead"]; !hasBead {
			continue
		}
		cause, _ := sk.Facts["cause"].(string)
		if cause == "" {
			cause = sk.Reason
		}
		c := out[sk.Rule]
		if c.Skips == nil {
			c.Skips = map[string]int{}
		}
		c.Skips[cause]++
		out[sk.Rule] = c
	}
}

func emit(env apply.Env, l Line) error {
	var w io.Writer = os.Stderr
	if env.Stderr != nil {
		w = env.Stderr
	}
	b, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("encode run counters: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
		return fmt.Errorf("write run counters: %w", err)
	}
	return nil
}

// Finish writes the run's counter line.
func (h *Hook) Finish(_ context.Context, env apply.Env, evs []apply.Event) error {
	rules := Counters(evs)
	countSkips(rules, h.skipped)
	return emit(env, Line{
		Contract: Contract, Type: h.typ, ID: h.id, Rules: rules, Escalations: h.escalations,
		Seq: h.seq, FromItem: h.fromItem,
	})
}

// EmitFailureLine writes the counters line of a run that failed closed before
// any rule ran: no rules, no escalations and failures {reason: 1}. The caller
// writes nothing else to the tracker or pg-desk.
func EmitFailureLine(w io.Writer, typ, id, reason string) error {
	return emit(apply.Env{Stderr: w}, Line{
		Contract: Contract, Type: typ, ID: id, Rules: map[string]RuleCounters{},
		Failures: map[string]int{reason: 1},
	})
}
