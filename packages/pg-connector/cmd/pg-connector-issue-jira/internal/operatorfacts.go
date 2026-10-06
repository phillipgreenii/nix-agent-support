// operatorfacts.go: the attention facts Show adds for an issue assigned to the
// operator (bead pg2-5l0x4.14): when the issue entered its current status, and
// when the operator last updated it. A rule over "In Progress for a week with
// no update from me" needs both, and neither is in `pjira issue`'s output
// (that op carries no changelog or comments), so Show makes ONE extra
// `pjira search --expand changelog,comments` call for the issue, reusing
// activity.go's operator identity and author matching.
//
// Cost control: the extra call is made only when the issue is assigned to the
// operator, because Show also serves cross-reference lookups of arbitrary
// ticket keys, which must stay one call.
//
// Failure policy: every failure here is soft. The facts stay empty and Show
// still answers, so a consumer sees "unknown", never a wrong value or an
// error that would break an unrelated Show.
package internal

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// issueKeyRE is the shape of a Jira issue key; Show only interpolates a key
// into JQL after it matches.
var issueKeyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// enrichOperatorFacts fills out.StatusChangedAt and out.OperatorUpdatedAt for
// an issue assigned to the operator. It changes nothing for any other issue.
func (b *Backend) enrichOperatorFacts(ctx context.Context, iss *pjiraIssue, out *schema.Issue) {
	if iss.Assignee == nil || !issueKeyRE.MatchString(iss.Key) {
		return
	}
	me, err := b.resolveOperator(ctx)
	if err != nil {
		return
	}
	me.learn(*iss.Assignee)
	if !me.Matches(*iss.Assignee) {
		return
	}
	raw, err := b.runner.Run(ctx, "search", "--jql", `key = "`+iss.Key+`"`, "--expand", "changelog,comments", "--limit", "1")
	if err != nil {
		return
	}
	res, err := decodePJIRASearchResult(raw)
	if err != nil {
		return
	}
	for i := range res.Items {
		if res.Items[i].Key == iss.Key {
			out.StatusChangedAt, out.OperatorUpdatedAt = operatorFacts(me, &res.Items[i], iss.Status)
			return
		}
	}
}

// operatorFacts computes the two facts from an issue carrying its changelog
// and comments. statusNow is the issue's current status name.
//
// statusChangedAt is the latest status transition (any author) whose target is
// the current status, else the issue's creation time. operatorUpdatedAt is
// the later of the operator's latest comment and latest status transition.
// Either is "" when it cannot be dated.
func operatorFacts(me *operatorIdentity, iss *pjiraIssue, statusNow string) (statusChangedAt, operatorUpdatedAt string) {
	for _, e := range iss.Changelog {
		me.learn(e.Author)
	}
	for _, c := range iss.Comments {
		me.learn(c.Author)
	}

	var changed, operator time.Time
	for _, e := range iss.Changelog {
		if !isStatusEntry(e) {
			continue
		}
		at, ok := parseJiraUpdated(e.At)
		if !ok {
			continue
		}
		if statusNow != "" && equalFoldTrim(e.To, statusNow) && at.After(changed) {
			changed = at
		}
		if me.Matches(e.Author) && at.After(operator) {
			operator = at
		}
	}
	for _, c := range iss.Comments {
		if !me.Matches(c.Author) {
			continue
		}
		if at, ok := parseJiraUpdated(c.Created); ok && at.After(operator) {
			operator = at
		}
	}
	if changed.IsZero() {
		if created, ok := parseJiraUpdated(iss.Created); ok {
			changed = created
		}
	}
	return formatUTC(changed), formatUTC(operator)
}

func formatUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func equalFoldTrim(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
