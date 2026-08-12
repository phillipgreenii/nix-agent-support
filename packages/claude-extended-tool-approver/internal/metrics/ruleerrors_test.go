package metrics

import (
	"errors"
	"sync"
	"testing"
)

func TestRuleErrorsCountsPerRuleAndKeepsTheFirstSample(t *testing.T) {
	c := NewRuleErrors()
	c.Record("gh", errors.New("first gh failure"))
	c.Record("gh", errors.New("second gh failure"))
	c.Record("primary-commit", errors.New("resolver timeout"))

	got := c.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot() has %d entries, want 2: %+v", len(got), got)
	}
	// Sorted by rule name — the snapshot is persisted and diffed, so an unstable
	// order would make identical windows look different.
	if got[0].Rule != "gh" || got[1].Rule != "primary-commit" {
		t.Fatalf("Snapshot() not ordered by rule: %+v", got)
	}
	if got[0].Count != 2 {
		t.Errorf("gh Count = %d, want 2 — per-rule counting is the whole point: a rule failing "+
			"SYSTEMATICALLY is what the sink exists to surface", got[0].Count)
	}
	if got[0].Sample != "first gh failure" {
		t.Errorf("gh Sample = %q, want the FIRST failure: a systematically-failing resolver fails the "+
			"same way every time, and the first sample is the one whose timing correlates with the change",
			got[0].Sample)
	}
	if c.Total() != 3 {
		t.Errorf("Total() = %d, want 3", c.Total())
	}
}

func TestRuleErrorsEmptySnapshotIsNil(t *testing.T) {
	c := NewRuleErrors()
	if got := c.Snapshot(); got != nil {
		t.Errorf("Snapshot() on a fresh counter = %+v, want nil so a caller can flush on len()==0", got)
	}
	if c.Total() != 0 {
		t.Errorf("Total() = %d, want 0", c.Total())
	}
}

func TestRuleErrorsRecordsANilError(t *testing.T) {
	// A nil err still means the caller OBSERVED a failure outcome; dropping it would
	// make the count disagree with the decision path.
	c := NewRuleErrors()
	c.Record("mystery", nil)
	got := c.Snapshot()
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("Snapshot() = %+v, want one entry with Count 1", got)
	}
	if got[0].Sample != "<nil>" {
		t.Errorf("Sample = %q, want %q", got[0].Sample, "<nil>")
	}
}

func TestRuleErrorsResetEmptiesTheWindow(t *testing.T) {
	c := NewRuleErrors()
	c.Record("gh", errors.New("x"))
	c.Reset()
	if got := c.Snapshot(); got != nil {
		t.Errorf("Snapshot() after Reset = %+v, want nil", got)
	}
	// A sample recorded before the reset must not leak into the next window.
	c.Record("gh", errors.New("y"))
	if got := c.Snapshot(); got[0].Sample != "y" {
		t.Errorf("Sample after Reset = %q, want the new window's first sample", got[0].Sample)
	}
}

func TestNilRuleErrorsIsUsable(t *testing.T) {
	// The engine's sink is an interface a caller may leave unset; a nil *RuleErrors
	// must degrade to a no-op rather than panic inside the decision path.
	var c *RuleErrors
	c.Record("gh", errors.New("x"))
	c.Reset()
	if got := c.Snapshot(); got != nil {
		t.Errorf("nil Snapshot() = %+v, want nil", got)
	}
	if c.Total() != 0 {
		t.Errorf("nil Total() = %d, want 0", c.Total())
	}
}

// TestRuleErrorsIsConcurrencySafe exists because the engine is reachable from
// recursive evaluation and nothing forbids a future caller from evaluating in
// parallel. A counting sink that data-raced would be worse than no sink: `go test
// -race` would start failing in whatever unrelated test happened to trip it.
func TestRuleErrorsIsConcurrencySafe(t *testing.T) {
	c := NewRuleErrors()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Record("gh", errors.New("boom"))
			_ = c.Snapshot()
			_ = c.Total()
		}()
	}
	wg.Wait()
	if c.Total() != 50 {
		t.Errorf("Total() = %d, want 50", c.Total())
	}
}
