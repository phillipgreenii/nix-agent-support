// Package workitem is the PR work-item contract (entity-change-flow design
// 9.8) as machine-readable Go data, plus the shared pieces every pg-decider
// rule uses to reason about work items that already exist: the dedup_key
// builders and parser (dedupkey.go) and the work-item Index built from a
// composite view's links[] (index.go).
//
// Nothing here reads or records who closed a work item (S26): lookups see
// only an item's state.
package workitem

// Kind names one work-item kind (spec 9.8). The strings are exact.
type Kind string

// The five work-item kinds.
const (
	KindAnchor          Kind = "anchor"
	KindProcessFeedback Kind = "process-feedback"
	KindReviewPR        Kind = "review-pr"
	KindFixCI           Kind = "fix-ci"
	KindResolveConflict Kind = "resolve-conflict"
)

// KindContract is one row of the spec 9.8 work-item contract table.
//
// Encodings pinned for the packets that read and write these values:
//   - covered_comments is a comma-separated list of comment ids, sorted;
//   - failing_builds is a comma-separated list of <run id>:<attempt>, sorted;
//   - failing_checks is a comma-separated list of check names, sorted;
//   - Labels hold the table's label tokens verbatim, so "pbase:<n>" and
//     "fbsum:<digest>" are templates and "co-owned" / "mine" are conditional
//     on ownership (spec 9.8, S16); roles rely only on what the table lists;
//   - TitleTemplate placeholders are <repo>, <n> and <pr title>;
//   - Parent is "anchor" or "" (the table's "none").
type KindContract struct {
	IssueType, TitleTemplate string
	Labels                   []string
	MetadataKeys             []string // every metadata key of the row, including dedup_key
	Parent                   string   // "anchor" or ""
}

// contracts is the table, as data (spec 9.8). Keep it in the row order of the
// spec; kindOrder below fixes the order Kinds returns.
var contracts = map[Kind]KindContract{
	KindAnchor: {
		IssueType:     "merge-request",
		TitleTemplate: "<repo>#<n>: <pr title>",
		Labels:        []string{"co-owned", "pbase:<n>"},
		MetadataKeys:  []string{"repo", "pr_number", "state", "branch", "base", "author", "url", "draft", "dedup_key"},
	},
	KindProcessFeedback: {
		IssueType:     "task",
		TitleTemplate: "process-feedback: <repo>#<n>",
		Labels:        []string{"mine", "fbsum:<digest>"},
		MetadataKeys:  []string{"repo", "pr_number", "branch", "covered_comments", "dedup_key"},
		Parent:        "anchor",
	},
	KindReviewPR: {
		IssueType:     "task",
		TitleTemplate: "review-pr: <repo>#<n>",
		MetadataKeys:  []string{"repo", "pr_number", "branch", "head_sha", "ownership", "dedup_key"},
		Parent:        "anchor",
	},
	KindFixCI: {
		IssueType:     "task",
		TitleTemplate: "fix-ci: <repo>#<n>",
		Labels:        []string{"mine", "worker-ready"},
		MetadataKeys:  []string{"repo", "pr_number", "branch", "head_sha", "failing_checks", "failing_builds", "dedup_key"},
		Parent:        "anchor",
	},
	KindResolveConflict: {
		IssueType:     "task",
		TitleTemplate: "resolve-conflict: <repo>#<n>",
		Labels:        []string{"mine", "worker-ready"},
		MetadataKeys:  []string{"repo", "pr_number", "branch", "head_sha", "base", "base_sha", "dedup_key"},
		Parent:        "anchor",
	},
}

var kindOrder = []Kind{KindAnchor, KindProcessFeedback, KindReviewPR, KindFixCI, KindResolveConflict}

// Kinds returns the five kinds in spec order.
func Kinds() []Kind { return append([]Kind(nil), kindOrder...) }

// ContractFor returns a copy of k's contract row; the zero value for an
// unknown kind.
func ContractFor(k Kind) KindContract {
	c, ok := contracts[k]
	if !ok {
		return KindContract{}
	}
	c.Labels = cloneStrings(c.Labels)
	c.MetadataKeys = cloneStrings(c.MetadataKeys)
	return c
}

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s...)
}

func knownKind(k Kind) bool {
	_, ok := contracts[k]
	return ok
}
