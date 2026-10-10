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

	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// Contract identifies the line's shape and version.
const Contract = "pg-decider.run-counters/v1"

// RuleCounters is one rule's counts for the run.
type RuleCounters struct {
	Planned int `json:"planned"`
	Applied int `json:"applied"`
	Deduped int `json:"deduped"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Line is the emitted JSON object.
type Line struct {
	Contract    string                  `json:"contract"`
	Type        string                  `json:"type"`
	ID          string                  `json:"id"`
	Rules       map[string]RuleCounters `json:"rules"`
	Escalations int                     `json:"escalations"`
}

// Hook implements apply.Hook.
type Hook struct {
	typ, id     string
	escalations int
}

var _ apply.Hook = (*Hook)(nil)

// New returns the metrics hook for one run on the entity typ/id. The view is
// accepted for symmetry with the failure hook; the counters are derived from
// the run's events.
func New(_ *view.View, typ, id string) *Hook { return &Hook{typ: typ, id: id} }

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

// Finish writes the run's counter line.
func (h *Hook) Finish(_ context.Context, env apply.Env, evs []apply.Event) error {
	var w io.Writer = os.Stderr
	if env.Stderr != nil {
		w = env.Stderr
	}
	b, err := json.Marshal(Line{Contract: Contract, Type: h.typ, ID: h.id, Rules: Counters(evs), Escalations: h.escalations})
	if err != nil {
		return fmt.Errorf("encode run counters: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
		return fmt.Errorf("write run counters: %w", err)
	}
	return nil
}
