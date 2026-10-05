// listrange.go: the ranged-list helpers (bead pg2-ttk9t, work-tracker design
// WT-D18). The umbrella delivers list_since/list_before in the request config
// (scriptout.ListRangeFromContext); this backend narrows each search with a
// day-granular GitHub `updated:` qualifier (GitHub's date qualifiers are
// UTC-date granular, so the qualifier is WIDENED to whole days) and then
// filters the returned PRs PRECISELY by their own updatedAt, so present_ids
// is exactly the bounded match set.
package internal

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const ghDateLayout = "2006-01-02"

// withUpdatedQualifier appends GitHub's `updated:` qualifier for r to q. An
// unbounded range returns q unchanged. Both ends are rendered as UTC dates
// (inclusive), which can only widen the window; prInRange does the precise
// cut.
func withUpdatedQualifier(q string, r scriptout.TimeRange) string {
	var qual string
	switch {
	case r.IsZero():
		return q
	case !r.Since.IsZero() && !r.Before.IsZero():
		qual = fmt.Sprintf("updated:%s..%s", r.Since.UTC().Format(ghDateLayout), r.Before.UTC().Format(ghDateLayout))
	case !r.Since.IsZero():
		qual = fmt.Sprintf("updated:>=%s", r.Since.UTC().Format(ghDateLayout))
	default:
		qual = fmt.Sprintf("updated:<=%s", r.Before.UTC().Format(ghDateLayout))
	}
	return strings.TrimSpace(q) + " " + qual
}

// prInRange applies r precisely to a PR's updatedAt (RFC3339). A PR whose
// timestamp is missing or unparseable cannot be judged, so it is kept and
// flagged imprecise: the caller MUST then report truncated: true rather than
// claim the bound was honored (WT-D18).
func prInRange(updatedAt string, r scriptout.TimeRange) (keep, imprecise bool) {
	if r.IsZero() {
		return true, false
	}
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return true, true
	}
	return r.Contains(t), false
}
