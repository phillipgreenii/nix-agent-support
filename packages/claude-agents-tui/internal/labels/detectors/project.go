package detectors

import "github.com/phillipgreenii/claude-agents-tui/internal/labels"

// Project picks a workspace.project label value via resolution order:
//  1. GC_RIG (gascity rig name)
//  2. WORKSPACE env (used by cmux and other workspace tools)
//  3. Otherwise omitted.
//
// Worktree-basename fallback is intentionally deferred — needs richer
// Session context than today's struct exposes.
type Project struct{}

func (Project) Name() string { return "project" }

func (Project) Detect(s labels.Session) labels.Set {
	if v := s.Env["GC_RIG"]; v != "" {
		return labels.Set{"workspace.project": v}
	}
	if v := s.Env["WORKSPACE"]; v != "" {
		return labels.Set{"workspace.project": v}
	}
	return nil
}
