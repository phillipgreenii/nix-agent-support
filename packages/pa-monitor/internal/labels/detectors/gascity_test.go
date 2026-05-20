package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

func TestGascity_FromGCEnv(t *testing.T) {
	d := Gascity{}
	got := d.Detect(labels.Session{
		Env: map[string]string{
			"GC_RIG":          "beads",
			"GC_AGENT":        "polecat",
			"GC_SESSION_NAME": "beads.polecat",
		},
	})
	if got["workspace.scope"] != "gascity" {
		t.Errorf("scope = %q", got["workspace.scope"])
	}
	if got["workspace.project"] != "beads" {
		t.Errorf("project = %q", got["workspace.project"])
	}
	if got["agent.role"] != "polecat" {
		t.Errorf("role = %q", got["agent.role"])
	}
}

func TestGascity_NoEnvProducesEmptySet(t *testing.T) {
	d := Gascity{}
	got := d.Detect(labels.Session{Env: map[string]string{}})
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

func TestGascity_AgentOnlyWithoutRig(t *testing.T) {
	d := Gascity{}
	got := d.Detect(labels.Session{Env: map[string]string{"GC_AGENT": "mayor"}})
	if got["agent.role"] != "mayor" {
		t.Errorf("role = %q", got["agent.role"])
	}
	if got["workspace.scope"] != "" {
		t.Errorf("scope should be empty without GC_RIG, got %q", got["workspace.scope"])
	}
}
