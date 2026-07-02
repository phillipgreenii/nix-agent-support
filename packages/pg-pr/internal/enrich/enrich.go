// Package enrich computes deterministic, LLM-free enrichment fields for a PR:
// kind, languages, size, and urgency. All functions are pure (no I/O, no clock,
// no network) so they are fully table-testable.
//
// The "project broken on main" urgency signal (pg2-4c5i.25) is injectable via
// Input.ProjectHealthFunc. When nil the signal is disabled and Compute behaves
// identically to before. Use ComputeWithContext to pass a context for the
// health-checker call; Compute is a context-free wrapper (uses context.Background).
package enrich

import (
	"context"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// bucketSize maps a total changed-line count (additions+deletions) to a
// coarse size bucket.
func bucketSize(total int) string {
	switch {
	case total < 10:
		return "XS"
	case total < 30:
		return "S"
	case total < 100:
		return "M"
	case total < 500:
		return "L"
	default:
		return "XL"
	}
}

// conventional-commit header: optional leading space, a type word, optional
// (scope), optional !, then a colon.
var ccTypeRe = regexp.MustCompile(`^\s*([a-zA-Z]+)(\([^)]*\))?!?:`)

// classifyKind returns the single dominant change kind. Precedence: PR-title
// conventional-commit prefix, then branch prefix, then commit-type majority,
// then "other". Title/branch tiers work on every code path; the commit tier
// only has data when commit messages were fetched (GraphQL path).
func classifyKind(title, branch string, commits []string) string {
	if k := kindFromConventional(title); k != "" {
		return k
	}
	if k := kindFromBranch(branch); k != "" {
		return k
	}
	if k := kindFromCommitMajority(commits); k != "" {
		return k
	}
	return "other"
}

func kindFromConventional(s string) string {
	m := ccTypeRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return mapCCType(strings.ToLower(m[1]))
}

func kindFromBranch(b string) string {
	seg := b
	if i := strings.IndexByte(b, '/'); i >= 0 {
		seg = b[:i]
	}
	return mapCCType(strings.ToLower(seg))
}

// kindFromCommitMajority classifies each commit's first line and returns the
// most common classified kind, with a deterministic alphabetical tiebreak.
// Returns "" when no commit is classifiable.
func kindFromCommitMajority(commits []string) string {
	counts := map[string]int{}
	for _, c := range commits {
		first := strings.SplitN(c, "\n", 2)[0]
		if k := kindFromConventional(first); k != "" {
			counts[k]++
		}
	}
	best := ""
	for k, n := range counts {
		if n > counts[best] || (n == counts[best] && (best == "" || k < best)) {
			best = k
		}
	}
	return best
}

func mapCCType(t string) string {
	switch t {
	case "feat", "feature":
		return "feature"
	case "fix", "bugfix", "hotfix":
		return "bugfix"
	case "refactor", "perf":
		return "refactor"
	case "docs", "doc":
		return "docs"
	case "test", "tests":
		return "test"
	case "chore", "build", "ci", "style":
		return "chore"
	}
	return ""
}

// Input is everything the enrichment computation needs, decoupled from how it
// was fetched. Files/Commits/Labels may be empty (partial data degrades, never
// errors).
type Input struct {
	PR      api.PR
	Files   []string    // changed-file paths
	Commits []string    // commit messages
	Labels  []string    // PR label names
	CIRuns  []api.CIRun // the PR's own CI runs

	// ProjectHealthFunc, when non-nil, is called for each app-path derived from
	// Files to determine whether the project is broken on main and whether this
	// PR's branch is green. See broken_project.go. Nil disables the signal.
	// Live verification of this hook is deferred (pg2-4c5i.25 follow-up).
	ProjectHealthFunc ProjectHealthFunc

	// LinkedTicketKeys is the list of external ticket keys linked to this PR
	// (e.g. derived from the branch name, PR title, or body via ticketlink.Parse).
	// Used by JiraLookupFunc to fetch priority/incident info. May be nil or empty
	// when no ticket linkage has been configured or found.
	LinkedTicketKeys []string

	// JiraLookupFunc, when non-nil, is called for each key in LinkedTicketKeys
	// to fetch Jira priority and production-incident information. See
	// jira_priority.go. Nil disables the signal (backward compatible).
	// Live verification deferred (pg2-4c5i.26 follow-up): the real Jira provider
	// and config-driven injection are wired in a future bead.
	JiraLookupFunc JiraLookupFunc
}

var urgencyLabels = map[string]bool{
	"urgent": true, "p0": true, "p1": true, "hotfix": true,
	"security": true, "incident": true, "critical": true, "sev1": true, "sev2": true,
}

var urgencyKeywords = []string{
	"production incident", "outage", "hotfix", "sev1", "sev2",
	"regression", "revert", "asap", "urgent", "critical",
}

// scoreUrgency returns an urgency level (low|medium|high), the additive score
// behind it, and the list of signals that fired (for transparency). Scoring:
// urgency label +3, title/body keyword +2, failing CI +2, bugfix commit +1.
// high at score>=3, medium at score>=1, else low.
func scoreUrgency(in Input) (string, int, []string) {
	score := 0
	var reasons []string

	for _, l := range in.Labels {
		ll := strings.ToLower(strings.TrimSpace(l))
		if urgencyLabels[ll] {
			score += 3
			reasons = append(reasons, "label:"+ll)
			break
		}
	}

	hay := strings.ToLower(in.PR.Title + "\n" + in.PR.Body)
	for _, kw := range urgencyKeywords {
		if strings.Contains(hay, kw) {
			score += 2
			reasons = append(reasons, "keyword:"+kw)
			break
		}
	}

	if anyCIFailing(in.CIRuns) {
		score += 2
		reasons = append(reasons, "ci-failing")
	}

	for _, c := range in.Commits {
		first := strings.SplitN(c, "\n", 2)[0]
		if kindFromConventional(first) == "bugfix" {
			score++
			reasons = append(reasons, "bugfix-commit")
			break
		}
	}

	return urgencyLevel(score), score, reasons
}

// anyCIFailing reports whether any completed run has a non-success conclusion
// (failure/timed_out/cancelled/action_required/...). Pending/neutral/skipped
// runs do not count as failing.
func anyCIFailing(runs []api.CIRun) bool {
	for _, r := range runs {
		if !strings.EqualFold(r.Status, "completed") {
			continue
		}
		switch strings.ToLower(r.Conclusion) {
		case "", "success", "neutral", "skipped":
		default:
			return true
		}
	}
	return false
}

// Result is the computed enrichment for a PR.
type Result struct {
	Kind           string
	Languages      []string
	Size           string
	Urgency        string
	UrgencyScore   int
	UrgencyReasons []string
}

// Compute derives all four enrichment fields from Input. Pure and deterministic.
// When Input.ProjectHealthFunc is nil the "project broken on main" signal is
// skipped. Use ComputeWithContext to pass a context for the health-checker call;
// Compute is a convenience wrapper that uses context.Background.
func Compute(in Input) Result {
	return ComputeWithContext(context.Background(), in)
}

// ComputeWithContext is the context-aware variant of Compute. The context is
// forwarded to Input.ProjectHealthFunc (if set) so health-checker calls can be
// cancelled or have a deadline. All other signals remain pure and context-free.
func ComputeWithContext(ctx context.Context, in Input) Result {
	r := Result{
		Kind:      classifyKind(in.PR.Title, in.PR.Branch, in.Commits),
		Languages: detectLanguages(in.Files),
		Size:      bucketSize(in.PR.Additions + in.PR.Deletions),
	}
	r.Urgency, r.UrgencyScore, r.UrgencyReasons = scoreUrgencyWithHealth(ctx, in)
	return r
}
