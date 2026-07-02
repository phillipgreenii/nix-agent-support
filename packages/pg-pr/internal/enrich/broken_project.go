package enrich

// Package enrich — broken_project.go
//
// "Referenced project broken on main" urgency signal (pg2-4c5i.25).
//
// Design: A PR touches one or more app-paths (derived from changed-file paths,
// e.g. "svc/beta" from "svc/beta/handler.go"). For each
// app-path a configurable ProjectHealthFunc checks (a) whether main is currently
// broken and (b) whether this PR's branch is currently green for that path.
//
// Heuristic — PR-is-the-fix: fires when
//   - main IS broken for the app-path, AND
//   - THIS PR's branch IS green for that app-path.
//
// When the signal fires, urgency is bumped by +4 (puts it firmly at "high" on
// its own) and a reason "project-broken-main:<app-path>" is appended.
//
// Live verification deferred: ProjectHealthFunc is an injectable type alias.
// The real implementation (querying an internal build-health / CI observability
// backend, or any other build-status backend for other deployments) is NOT
// wired here. Callers supply nil to skip the signal entirely; a future bead
// will wire the real backend and inject it via config-driven dependency
// injection.

import (
	"context"
	"path"
	"sort"
)

// ProjectHealthResult holds the health outcome for one app-path + branch pair.
type ProjectHealthResult struct {
	// MainBroken reports whether the app-path's main branch is currently broken
	// (i.e. has recent build/CI failures on main).
	MainBroken bool
	// PRBranchGreen reports whether this PR's branch is currently green for the
	// app-path (i.e. the PR's most recent build/CI run for this path succeeded).
	PRBranchGreen bool
}

// ProjectHealthFunc is the injectable function signature for looking up build
// health for a given app-path and PR branch. Implementations query an external
// build-status source (e.g. an internal build-health / CI observability
// backend). A nil value disables the signal entirely. The context carries
// cancellation / deadline from the calling enrichment pass.
//
// Public-repo hygiene: pg-pr defines only this generic interface. All
// deployment-specific details (backend endpoints, workflow identifiers,
// app-path mappings) MUST live in the consuming config and be injected as a
// concrete implementation of this type.
type ProjectHealthFunc func(ctx context.Context, appPath, prBranch string) (ProjectHealthResult, error)

// appPathsFromFiles derives the sorted, deduplicated set of app-paths from
// changed-file paths. An app-path is the immediate parent directory of a
// changed file (path.Dir). Files at the root (Dir == ".") are excluded because
// they do not belong to a sub-project. The result is sorted for determinism.
func appPathsFromFiles(files []string) []string {
	seen := make(map[string]struct{})
	for _, f := range files {
		dir := path.Dir(f)
		if dir == "." || dir == "" {
			continue
		}
		seen[dir] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// scoreProjectHealth evaluates the "project broken on main" urgency signal for
// each app-path derived from in.Files. For each app-path where main is broken
// AND this PR's branch is green, it appends a
// "project-broken-main:<app-path>" reason and adds +4 to score (PR-is-the-fix
// heuristic). A nil ProjectHealthFunc is a no-op (signal disabled).
//
// Each firing app-path contributes exactly +4 to the score and one reason
// entry. This makes the signal independently sufficient for "high" urgency
// (threshold ≥ 3).
func scoreProjectHealth(ctx context.Context, in Input) (score int, reasons []string) {
	if in.ProjectHealthFunc == nil {
		return 0, nil
	}
	appPaths := appPathsFromFiles(in.Files)
	if len(appPaths) == 0 {
		return 0, nil
	}
	for _, ap := range appPaths {
		result, err := in.ProjectHealthFunc(ctx, ap, in.PR.Branch)
		if err != nil {
			// Treat lookup errors as "unknown" — do not fire the signal.
			continue
		}
		if result.MainBroken && result.PRBranchGreen {
			score += 4
			reasons = append(reasons, "project-broken-main:"+ap)
		}
	}
	return score, reasons
}

// scoreUrgencyWithHealth is the context-aware variant of scoreUrgency that
// incorporates the project-health signal. It delegates the base urgency signals
// to scoreUrgency and then layers in the project-health contribution.
func scoreUrgencyWithHealth(ctx context.Context, in Input) (string, int, []string) {
	level, score, reasons := scoreUrgency(in)

	healthScore, healthReasons := scoreProjectHealth(ctx, in)
	if healthScore > 0 {
		score += healthScore
		reasons = append(reasons, healthReasons...)
		// Re-derive level from updated score.
		level = urgencyLevel(score)
	}
	return level, score, reasons
}

// urgencyLevel maps a numeric score to a named urgency level using the same
// thresholds as scoreUrgency. Extracted so both paths share one authoritative
// definition.
func urgencyLevel(score int) string {
	switch {
	case score >= 3:
		return "high"
	case score >= 1:
		return "medium"
	default:
		return "low"
	}
}
