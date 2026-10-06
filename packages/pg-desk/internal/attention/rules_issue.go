package attention

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// KindIssueStaleInProgress is the rule kind of a Jira issue that is assigned
// to the operator, In Progress, and has had no update from the operator for
// the threshold.
const KindIssueStaleInProgress = "issue.stale-in-progress"

// DefaultStaleInProgressAfter is the built-in threshold of
// issue.stale-in-progress: 7 calendar days.
const DefaultStaleInProgressAfter = 7 * 24 * time.Hour

func init() {
	Register(issueStaleInProgress{})
}

// issueStaleInProgress is the first time-based rule: a pure predicate over
// the view's issue facts and the injected clock (View.Now). It reads no panel
// and no interpretation row, because an issue has none.
//
// "Update by the operator" is what the connector supplies as
// interpret.IssueAttentionFacts.OperatorUpdatedAt: the later of the
// operator's latest comment and latest status transition; other users and
// bots never reset the clock. The age is measured from the later of that and
// the time the issue entered In Progress, so an issue another person moved
// into In Progress recently is not reported as long-neglected, and an issue
// the operator never updated is measured from its In Progress entry.
type issueStaleInProgress struct{}

func (issueStaleInProgress) Kind() string                     { return KindIssueStaleInProgress }
func (issueStaleInProgress) DefaultSeverity() Severity        { return SeverityMedium }
func (issueStaleInProgress) DefaultStaleAfter() time.Duration { return DefaultStaleInProgressAfter }

func (r issueStaleInProgress) Raise(v *View, p RuleSettings) ([]Candidate, string) {
	if v.Type != typeIssue {
		return nil, "not an issue"
	}
	if v.Degraded {
		return nil, "interpretation degraded"
	}
	if v.Now.IsZero() {
		return nil, "no clock reading"
	}
	f, ok := v.IssueFacts()
	if !ok {
		return nil, "stored facts unavailable"
	}
	switch {
	case f.StatusCategory != interpret.IssueStatusCategoryInProgress:
		return nil, "issue is not in progress"
	case f.Assignee == "":
		return nil, "issue is unassigned"
	case !f.OperatorFactsKnown:
		// Unknown is not zero: an issue not assigned to the operator carries
		// no operator facts, and neither does one the connector could not
		// date. Never raise on it (INV-ATTNEVAL-6).
		return nil, "not assigned to me, or operator facts unknown"
	}
	since := f.InProgressSince
	if f.OperatorUpdatedAt.After(since) {
		since = f.OperatorUpdatedAt
	}
	if since.IsZero() {
		return nil, "no date to measure from"
	}
	threshold := p.StaleAfter
	if threshold <= 0 {
		threshold = DefaultStaleInProgressAfter
	}
	age := v.Now.Sub(since)
	if age < threshold {
		return nil, fmt.Sprintf("last update %s ago, under the threshold of %s", wholeDays(age), wholeDays(threshold))
	}
	return []Candidate{{
		Kind: r.Kind(), Type: v.Type, ID: v.ID, Severity: p.Severity,
		Reason: fmt.Sprintf("In Progress with no update from me in %s", wholeDays(age)),
		Since:  since,
	}}, ""
}

// wholeDays renders d as a whole number of calendar days ("1 day", "8 days"),
// rounding down.
func wholeDays(d time.Duration) string {
	n := int(d / (24 * time.Hour))
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}
