package rules

// The focus.item rule's plan through the run-counters hook: a skip of a source
// that has a focus bead is counted by cause even though no action was planned,
// and an action is counted by the facts.transition the rule set on it.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/metrics"
)

func focusCounterLine(t *testing.T, s focusSetup, outcome apply.Outcome) string {
	t.Helper()
	v := s.view(t)
	res := decide.DecideWith(v, v.Type, nil)
	h := metrics.New(v, v.Type, v.ID)
	h.Skipped(res.Skipped)
	var evs []apply.Event
	for _, a := range res.Actions {
		if a.Rule == focusRuleID {
			evs = append(evs, apply.Event{Action: a, Outcome: outcome})
		}
	}
	var buf bytes.Buffer
	if err := h.Finish(context.Background(), apply.Env{Stderr: &buf}, evs); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestFocusCountersForTheRealRule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup focusSetup
		want  string // a member of the focus.item rule object
	}{
		{
			name:  "a selected source whose open bead is in play counts a skip and plans nothing",
			setup: focusSetup{fixture: "pr_selected", annotation: focusPeriod, bead: &focusBead{state: "open"}},
			want:  `"focus.item":{"planned":0,"applied":0,"deduped":0,"failed":0,"skipped":0,"skips":{"in-play":1}}`,
		},
		{
			name:  "a claimed bead counts a skip",
			setup: focusSetup{fixture: "pr_selected", annotation: nil, bead: &focusBead{state: "in_progress", assignee: "w"}},
			want:  `"skips":{"claimed":1}`,
		},
		{
			name:  "a strike of an open bead counts a hold",
			setup: focusSetup{fixture: "pr_selected", annotation: nil, bead: &focusBead{state: "open"}},
			want:  `"transitions":{"hold":{"applied":1}}`,
		},
		{
			name:  "a reselect of a held bead counts a release",
			setup: focusSetup{fixture: "pr_selected", annotation: focusPeriod, bead: &focusBead{state: "deferred", marker: "struck"}},
			want:  `"transitions":{"release":{"applied":1}}`,
		},
		{
			name:  "a selection with no bead counts a mint",
			setup: focusSetup{fixture: "pr_selected", annotation: focusPeriod},
			want:  `"transitions":{"mint":{"applied":1}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := focusCounterLine(t, tc.setup, apply.OutcomeApplied)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q\nwant it to contain %q", got, tc.want)
			}
		})
	}
}

// A source with no focus bead and no selection plans nothing and counts no
// skip: the volume of the line is one per entity, not one per rule.
func TestFocusCountersOmitSkipsOfASourceWithNoFocusBead(t *testing.T) {
	got := focusCounterLine(t, focusSetup{fixture: "pr_selected", annotation: nil}, apply.OutcomeApplied)
	if strings.Contains(got, "skips") || strings.Contains(got, "focus.item") {
		t.Fatalf("got %q", got)
	}
}
