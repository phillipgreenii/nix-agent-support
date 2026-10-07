package attention

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

const (
	// KindIssueDueSoon is the rule kind of an open issue whose due date is
	// near but not yet passed.
	KindIssueDueSoon = "issue.due-soon"
	// KindIssueOverdue is the rule kind of an open issue whose due date has
	// passed.
	KindIssueOverdue = "issue.overdue"
)

// DefaultDueSoonWithin is the built-in look-ahead window of issue.due-soon:
// 2 calendar days.
const DefaultDueSoonWithin = 2 * 24 * time.Hour

func init() {
	Register(issueDueSoon{})
	Register(issueOverdue{})
}

// issueDueSoon and issueOverdue are the due-date rules over issue entities
// (Jira issues and beads alike: both reach the desk as `issue` entities
// through the generic entity pipeline, watch.issue.queries). They read the
// issue's DueDate fact and the injected clock (View.Now), nothing else, so
// they raise for exactly the issues the deployment's watch queries select.
//
// The two are mutually exclusive: an issue is overdue once its due moment has
// passed, and due soon only before that. A bare calendar date (Jira's
// duedate) is due at the END of that day in the clock's own zone, so an issue
// due "today" is due soon, not overdue, until the day ends. The look-ahead
// window of issue.due-soon is measured to that due moment, so an issue due on
// the date two days ahead has more than two days left until its day ends and
// is inside a 2 day window only once the day before. An issue in a done
// status (jira.done_statuses) never raises, and neither does one with no due
// date or one the desk cannot read (INV-ATTNEVAL-6).
type issueDueSoon struct{}

func (issueDueSoon) Kind() string                  { return KindIssueDueSoon }
func (issueDueSoon) DefaultSeverity() Severity     { return SeverityMedium }
func (issueDueSoon) DefaultDueSoon() time.Duration { return DefaultDueSoonWithin }

func (r issueDueSoon) Raise(v *View, p RuleSettings) ([]Candidate, string) {
	due, why := dueMoment(v)
	if why != "" {
		return nil, why
	}
	remaining := due.Sub(v.Now)
	if remaining <= 0 {
		return nil, "already overdue (issue.overdue's)"
	}
	window := p.DueSoon
	if window <= 0 {
		window = DefaultDueSoonWithin
	}
	if remaining > window {
		return nil, fmt.Sprintf("due in %s, beyond the window of %s", wholeDays(remaining), wholeDays(window))
	}
	return []Candidate{{
		Kind: r.Kind(), Type: v.Type, ID: v.ID, Severity: p.Severity,
		Reason: "Due " + dueIn(remaining),
		Since:  due,
	}}, ""
}

type issueOverdue struct{}

func (issueOverdue) Kind() string              { return KindIssueOverdue }
func (issueOverdue) DefaultSeverity() Severity { return SeverityHigh }

func (r issueOverdue) Raise(v *View, p RuleSettings) ([]Candidate, string) {
	due, why := dueMoment(v)
	if why != "" {
		return nil, why
	}
	late := v.Now.Sub(due)
	if late < 0 {
		return nil, "not yet due"
	}
	return []Candidate{{
		Kind: r.Kind(), Type: v.Type, ID: v.ID, Severity: p.Severity,
		Reason: "Overdue by " + overdueBy(late),
		Since:  due,
	}}, ""
}

// dueMoment is the shared guard of both due-date rules: it returns the moment
// the issue falls due, or a non-empty reason why no due-date rule applies.
func dueMoment(v *View) (time.Time, string) {
	if v.Type != typeIssue {
		return time.Time{}, "not an issue"
	}
	if v.Degraded {
		return time.Time{}, "interpretation degraded"
	}
	if v.Now.IsZero() {
		return time.Time{}, "no clock reading"
	}
	f, ok := v.IssueFacts()
	if !ok {
		return time.Time{}, "stored facts unavailable"
	}
	switch {
	case f.Status == "":
		return time.Time{}, "issue status unknown"
	case f.Done:
		return time.Time{}, "issue is done"
	case !f.DueDateKnown:
		return time.Time{}, "no readable due date"
	}
	return dueInstant(f, v.Now.Location()), ""
}

// dueInstant is the moment f's due date falls due: the due instant itself, or
// the end of the due calendar day in loc for a bare date.
func dueInstant(f interpret.IssueAttentionFacts, loc *time.Location) time.Time {
	if !f.DueDateOnly {
		return f.DueDate
	}
	y, m, d := f.DueDate.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// dueIn renders a positive time remaining for a summary: "in 3 days",
// "in 1 day", or "in under a day" when less than a whole day is left.
func dueIn(d time.Duration) string {
	if d < 24*time.Hour {
		return "in under a day"
	}
	return "in " + wholeDays(d)
}

// overdueBy renders a positive lateness for a summary: "3 days", "1 day", or
// "under a day".
func overdueBy(d time.Duration) string {
	if d < 24*time.Hour {
		return "under a day"
	}
	return wholeDays(d)
}
