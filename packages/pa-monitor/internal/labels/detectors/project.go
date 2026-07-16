package detectors

import "github.com/phillipgreenii/pa-monitor/internal/labels"

// Project picks a workspace.project label value from the WORKSPACE env var
// (used by cmux and other workspace tools); otherwise omitted.
//
// Worktree-basename fallback is intentionally deferred — needs richer
// Session context than today's struct exposes.
type Project struct{}

func (Project) Name() string { return "project" }

func (Project) Detect(s labels.Session) labels.Set {
	if v := s.Env["WORKSPACE"]; v != "" {
		return labels.Set{"workspace.project": v}
	}
	return nil
}
