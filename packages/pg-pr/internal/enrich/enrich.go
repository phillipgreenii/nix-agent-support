// Package enrich computes deterministic, LLM-free enrichment fields for a PR:
// kind, languages, size, and urgency. All functions are pure (no I/O, no clock,
// no network) so they are fully table-testable.
package enrich

import (
	"regexp"
	"strings"
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
