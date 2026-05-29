package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

func TestDefaultScope_AlwaysSetsPersonal(t *testing.T) {
	d := DefaultScope{}
	got := d.Detect(labels.Session{})
	if got["workspace.scope"] != "personal" {
		t.Errorf("workspace.scope = %q, want personal", got["workspace.scope"])
	}
}

// TestDefaultScope_GascityOverrides verifies the contract that DefaultScope
// must run BEFORE Gascity so Gascity's "gascity" value wins via labels.Set.Merge
// (later-wins semantics).
func TestDefaultScope_GascityOverrides(t *testing.T) {
	merged := DefaultScope{}.Detect(labels.Session{}).
		Merge(Gascity{}.Detect(labels.Session{Env: map[string]string{"GC_RIG": "beads"}}))
	if merged["workspace.scope"] != "gascity" {
		t.Errorf("workspace.scope = %q, want gascity (Gascity should override DefaultScope)", merged["workspace.scope"])
	}
}
