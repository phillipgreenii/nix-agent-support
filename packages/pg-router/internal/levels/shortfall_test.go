package levels

import (
	"slices"
	"testing"
	"time"
)

func clause(i int, res string, op Op, level string) Clause {
	return Clause{Index: i, Text: res, Resource: res, Allowed: AllowedLevels(testRegistry[res], op, level)}
}

func TestShortfalls(t *testing.T) {
	held := func(v string) Level { return Level{Value: v} }
	stale := func(v string) Level { return Level{Value: v, State: StateStale} }
	tests := []struct {
		name   string
		levels map[string]Level
		req    []Clause
		want   []Shortfall
	}{
		{"admitted yields nothing", map[string]Level{"disk": held("OK")}, []Clause{clause(0, "disk", OpGe, "OK")}, nil},
		{
			"one step below, sole",
			map[string]Level{"disk": held("TIGHT")},
			[]Clause{clause(0, "disk", OpGe, "OK")},
			[]Shortfall{{"disk", 1, true}},
		},
		{
			"two steps below, sole",
			map[string]Level{"disk": held("EXHAUSTED")},
			[]Clause{clause(0, "disk", OpGe, "OK")},
			[]Shortfall{{"disk", 2, true}},
		},
		{
			"three steps below",
			map[string]Level{"disk": held("EXHAUSTED")},
			[]Clause{clause(0, "disk", OpGe, "PLENTY")},
			[]Shortfall{{"disk", 3, true}},
		},
		{
			"two unmet resources are not sole",
			map[string]Level{"disk": held("TIGHT"), "router": held("PAUSED")},
			[]Clause{clause(0, "disk", OpGe, "OK"), clause(1, "router", OpEq, "RUNNING")},
			[]Shortfall{{"disk", 1, false}, {"router", 1, false}},
		},
		{
			"a met clause on another resource does not unset sole",
			map[string]Level{"disk": held("TIGHT"), "router": held("RUNNING")},
			[]Clause{clause(0, "disk", OpGe, "OK"), clause(1, "router", OpEq, "RUNNING")},
			[]Shortfall{{"disk", 1, true}},
		},
		{
			"two unmet clauses on one resource are one resource and sole",
			map[string]Level{"disk": held("EXHAUSTED")},
			[]Clause{clause(0, "disk", OpGe, "OK"), clause(1, "disk", OpNe, "EXHAUSTED")},
			[]Shortfall{{"disk", 2, true}},
		},
		{
			"a met clause on the same resource bounds the distance",
			map[string]Level{"disk": held("OK")},
			[]Clause{clause(0, "disk", OpGe, "PLENTY"), clause(1, "disk", OpLe, "OK")},
			[]Shortfall{{"disk", StepsNone, true}},
		},
		{
			"two-level resource is one step",
			map[string]Level{"router": held("PAUSED")},
			[]Clause{clause(0, "router", OpEq, "RUNNING")},
			[]Shortfall{{"router", 1, true}},
		},
		{
			"absent level is unknown", nil,
			[]Clause{clause(0, "cred", OpGe, "AVAILABLE")},
			[]Shortfall{{"cred", StepsUnknown, true}},
		},
		{
			"absent level on a graded scale is unknown", nil,
			[]Clause{clause(0, "disk", OpLe, "TIGHT")},
			[]Shortfall{{"disk", StepsUnknown, true}},
		},
		{
			"stale level is unknown",
			map[string]Level{"disk": stale("TIGHT")},
			[]Clause{clause(0, "disk", OpGe, "OK")},
			[]Shortfall{{"disk", StepsUnknown, true}},
		},
		{
			"a level off the scale is unknown",
			map[string]Level{"disk": held("BOGUS")},
			[]Clause{clause(0, "disk", OpGe, "OK")},
			[]Shortfall{{"disk", StepsUnknown, true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := Snapshot{Now: time.Now(), Levels: tc.levels}
			req := Requirement{Clauses: tc.req}
			got := Shortfalls(snap, testRegistry, req)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Shortfalls = %+v, want %+v", got, tc.want)
			}
			if d := Decide(snap, testRegistry, req); d.Admit != (len(got) == 0) {
				t.Fatalf("Decide admit=%v disagrees with %d shortfalls", d.Admit, len(got))
			}
		})
	}
}

func TestStepsLabel(t *testing.T) {
	for steps, want := range map[int]string{1: "1", 3: "3", StepsUnknown: "unknown", StepsNone: "none"} {
		if got := StepsLabel(steps); got != want {
			t.Errorf("StepsLabel(%d) = %q, want %q", steps, got, want)
		}
	}
}
