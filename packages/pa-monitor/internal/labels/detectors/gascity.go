package detectors

import "github.com/phillipgreenii/pa-monitor/internal/labels"

// Gascity maps GC_* envs to generic label keys. Gas City is detected via
// GC_RIG presence; GC_AGENT supplies agent.role (polecat/mayor/witness/...).
type Gascity struct{}

func (Gascity) Name() string { return "gascity" }

func (Gascity) Detect(s labels.Session) labels.Set {
	out := labels.Set{}
	if rig := s.Env["GC_RIG"]; rig != "" {
		out["workspace.scope"] = "gascity"
		out["workspace.project"] = rig
	}
	if agent := s.Env["GC_AGENT"]; agent != "" {
		out["agent.role"] = agent
	}
	return out
}
