// Package cirollup is pg-pr's single source of truth for classifying CI runs
// and rolling them up into an overall state. Every "is CI failed?" decision in
// pg-pr routes through Classify/Compute so the definition lives in exactly one
// place (bead pg2-qs46b).
//
// A run whose Name matches a repo's configured Excluder is treated as Excluded
// in EVERY state (never fails/pends/passes the rollup). ZR uses this to drop
// policy-bot — an approval gate whose FAILURE means "approvals required", which
// is authoritatively reflected by GitHub's mergeStateStatus, not by CI health.
package cirollup

import (
	"log/slog"
	"regexp"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// Disposition is the per-run classification.
type Disposition int

const (
	Pending Disposition = iota
	Passed
	Failed
	Excluded
)

// Excluder matches CI check names that are excluded from the rollup entirely.
type Excluder struct {
	pats []*regexp.Regexp
}

// NewExcluder compiles patterns, skipping (with a warning) any that don't
// compile — a mis-configured pattern must not break the sync loop. Mirrors
// internal/ticketlink.compilePatterns. A nil/empty result is valid and matches
// nothing.
func NewExcluder(patterns []string) *Excluder {
	e := &Excluder{}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			slog.Default().Warn("cirollup: invalid excluded_ci_checks pattern skipped",
				"pattern", p, "err", err.Error())
			continue
		}
		e.pats = append(e.pats, re)
	}
	return e
}

// Match reports whether name matches any excluder pattern. Nil-safe.
func (e *Excluder) Match(name string) bool {
	if e == nil {
		return false
	}
	for _, re := range e.pats {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// Classify returns the disposition of a single run. An excluded name
// short-circuits before any status/conclusion logic. StatusContext runs arrive
// with Status hardcoded to "completed" (see github/enrich.go), so the in-flight
// commit-status states pending/expected are treated as Pending here regardless
// of Status.
func Classify(r api.CIRun, excl *Excluder) Disposition {
	if excl.Match(r.Name) {
		return Excluded
	}
	if r.Status != "completed" || r.Conclusion == "" || r.Conclusion == "pending" || r.Conclusion == "expected" {
		return Pending
	}
	switch r.Conclusion {
	case "success", "neutral", "skipped":
		return Passed
	default:
		// failure, error, cancelled, timed_out, action_required, startup_failure, stale, ...
		return Failed
	}
}

// Rollup is the aggregate over a set of runs.
type Rollup struct {
	State   string // none | pending | success | failure
	Passed  int
	Failed  int
	Pending int
}

// Compute aggregates dispositions. Excluded runs count toward none of the
// three tallies. Precedence: any failed → failure; else any pending → pending;
// else any passed → success; else (no countable runs) → none.
func Compute(runs []api.CIRun, excl *Excluder) Rollup {
	var r Rollup
	for _, run := range runs {
		switch Classify(run, excl) {
		case Passed:
			r.Passed++
		case Failed:
			r.Failed++
		case Pending:
			r.Pending++
		}
	}
	switch {
	case r.Failed > 0:
		r.State = "failure"
	case r.Pending > 0:
		r.State = "pending"
	case r.Passed > 0:
		r.State = "success"
	default:
		r.State = "none"
	}
	return r
}
