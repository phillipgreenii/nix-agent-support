package entity

import "testing"

// step is one observation of mergeable fed through Carry the way the store
// does: the carried value of one step is the prev of the next, but only when
// it is known (an UNKNOWN is never stored).
type step struct {
	observed         string
	headChg, baseChg bool
	wantCarried      string
	wantChanged      bool
}

func runSteps(t *testing.T, prev string, steps []step) {
	t.Helper()
	for i, s := range steps {
		carried, changed := Carry(prev, s.observed, s.headChg, s.baseChg)
		if carried != s.wantCarried || changed != s.wantChanged {
			t.Fatalf("step %d (prev %q, observed %q, head %v, base %v) = (%q, %v), want (%q, %v)",
				i, prev, s.observed, s.headChg, s.baseChg, carried, changed, s.wantCarried, s.wantChanged)
		}
		prev = ""
		if Known(carried) {
			prev = carried
		}
	}
}

func TestCarry_MergeableUnknownConflictingIsOneChange(t *testing.T) {
	runSteps(t, "", []step{
		{observed: "MERGEABLE", wantCarried: "MERGEABLE"},
		{observed: "UNKNOWN", wantCarried: "MERGEABLE"},
		{observed: "CONFLICTING", wantCarried: "CONFLICTING", wantChanged: true},
		{observed: "CONFLICTING", wantCarried: "CONFLICTING"},
	})
}

func TestCarry_UnknownAloneIsNoChange(t *testing.T) {
	runSteps(t, "", []step{
		{observed: "UNKNOWN", wantCarried: "UNKNOWN"},
		{observed: "UNKNOWN", wantCarried: "UNKNOWN"},
		{observed: "", wantCarried: "UNKNOWN"},
	})
	// With a known value carried, repeated UNKNOWN keeps returning it.
	runSteps(t, "MERGEABLE", []step{
		{observed: "UNKNOWN", wantCarried: "MERGEABLE"},
		{observed: "UNKNOWN", wantCarried: "MERGEABLE"},
		{observed: "MERGEABLE", wantCarried: "MERGEABLE"},
	})
}

func TestCarry_FirstKnownValueIsNotAChange(t *testing.T) {
	runSteps(t, "", []step{
		{observed: "UNKNOWN", wantCarried: "UNKNOWN"},
		{observed: "CONFLICTING", wantCarried: "CONFLICTING"},
		{observed: "MERGEABLE", wantCarried: "MERGEABLE", wantChanged: true},
	})
}

func TestCarry_DirectKnownToKnownIsOneChange(t *testing.T) {
	runSteps(t, "MERGEABLE", []step{
		{observed: "CONFLICTING", wantCarried: "CONFLICTING", wantChanged: true},
		{observed: "MERGEABLE", wantCarried: "MERGEABLE", wantChanged: true},
	})
}

func TestCarry_HeadChangeClearsTheCarriedValue(t *testing.T) {
	// The next UNKNOWN is returned as UNKNOWN, not as the old head's value.
	runSteps(t, "MERGEABLE", []step{
		{observed: "UNKNOWN", headChg: true, wantCarried: "UNKNOWN"},
		{observed: "UNKNOWN", wantCarried: "UNKNOWN"},
		{observed: "CONFLICTING", wantCarried: "CONFLICTING"}, // first known for the new head: no change
	})
	// A known value on a new head is no change either.
	runSteps(t, "MERGEABLE", []step{{observed: "CONFLICTING", headChg: true, wantCarried: "CONFLICTING"}})
}

func TestCarry_BaseChangeClearsTheCarriedValue(t *testing.T) {
	runSteps(t, "CONFLICTING", []step{
		{observed: "UNKNOWN", baseChg: true, wantCarried: "UNKNOWN"},
		{observed: "UNKNOWN", wantCarried: "UNKNOWN"},
		{observed: "MERGEABLE", wantCarried: "MERGEABLE"},
	})
	runSteps(t, "CONFLICTING", []step{{observed: "MERGEABLE", baseChg: true, wantCarried: "MERGEABLE"}})
}

func TestKnown(t *testing.T) {
	for v, want := range map[string]bool{"MERGEABLE": true, "CONFLICTING": true, "UNKNOWN": false, "": false} {
		if got := Known(v); got != want {
			t.Errorf("Known(%q) = %v, want %v", v, got, want)
		}
	}
}
