// listrange.go: the ranged-list filter (bead pg2-ttk9t, work-tracker design
// WT-D18). The umbrella delivers list_since/list_before in the request config
// (scriptout.ListRangeFromContext). bd's list/ready verbs are caller-supplied
// argument vectors this backend does not extend, so the bound is applied
// CLIENT-SIDE and exactly, to each issue's own updated_at.
package internal

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// issueInRange applies r to a bd issue's updated_at (RFC3339, fractional
// seconds allowed). An issue whose timestamp is missing or unparseable cannot
// be judged: it is kept and flagged imprecise, so the caller MUST report
// truncated: true rather than claim the bound was honored (WT-D18).
func issueInRange(updatedAt string, r scriptout.TimeRange) (keep, imprecise bool) {
	if r.IsZero() {
		return true, false
	}
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return true, true
	}
	return r.Contains(t), false
}
