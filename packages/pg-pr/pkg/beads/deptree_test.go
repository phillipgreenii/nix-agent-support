package beads

import (
	"testing"
)

func TestApplyHumanLabels(t *testing.T) {
	deps := []DepNode{
		{ID: "x-1", Status: "open"},
		{ID: "x-2", Status: "open", Labels: []string{"other"}},
		{ID: "x-3", Status: "closed"},
	}
	ApplyHumanLabels(deps, map[string]bool{"x-1": true, "x-2": true})
	if !hasLabel(deps[0].Labels, "human") {
		t.Errorf("x-1 should have human label, got %+v", deps[0].Labels)
	}
	if !hasLabel(deps[1].Labels, "human") {
		t.Errorf("x-2 should have human label, got %+v", deps[1].Labels)
	}
	if !hasLabel(deps[1].Labels, "other") {
		t.Errorf("x-2 should preserve existing label, got %+v", deps[1].Labels)
	}
	if hasLabel(deps[2].Labels, "human") {
		t.Errorf("x-3 not in set; should not have human label, got %+v", deps[2].Labels)
	}
}

func TestApplyHumanLabels_Idempotent(t *testing.T) {
	deps := []DepNode{{ID: "x-1", Status: "open", Labels: []string{"human"}}}
	ApplyHumanLabels(deps, map[string]bool{"x-1": true})
	count := 0
	for _, l := range deps[0].Labels {
		if l == "human" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want exactly one human label, got %d (labels=%+v)", count, deps[0].Labels)
	}
}

func TestAllNonClosedHumanLabeled_EmptyNonClosed(t *testing.T) {
	deps := []DepNode{
		{ID: "x", Status: "closed", Labels: []string{}},
	}
	if AllNonClosedHumanLabeled(deps) {
		t.Error("expected false on empty non-closed set")
	}
}

func TestAllNonClosedHumanLabeled_NilInput(t *testing.T) {
	if AllNonClosedHumanLabeled(nil) {
		t.Error("expected false on nil input (no non-closed deps)")
	}
}
