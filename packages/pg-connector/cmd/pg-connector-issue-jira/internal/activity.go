// activity.go: Backend implements pkg/provider/activity.Provider against Jira
// via the generic pjira CLI. list_activity is range-shaped and stateless:
// every call resolves the operator's identity, runs ONE pjira search for the
// issues the operator is involved in, and returns the operator's own
// happenings in the requested range; nothing is stored.
//
// Source query (one call per list_activity):
//
//	pjira search --all --expand changelog,comments --jql
//	  "(reporter = currentUser() OR assignee = currentUser() OR watcher = currentUser())
//	   AND updated >= <since date>"
//
// DELIBERATE DEVIATION from the design's JQL literal: the upper
// `AND updated < <before>` clause is omitted. An issue with an in-range
// operator happening that was touched again after `before` has updated >=
// before and would be silently lost on any past-range pull or backfill, while
// the op must return every happening in [since, before) and every in-range
// happening implies updated >= since. Only the widened lower bound is applied
// server-side (rangedJQL renders UTC date minus one day, so the day-granular,
// user-timezone JQL date can never narrow the window); each happening is then
// cut EXACTLY against [since, before) by its own timestamp. An omitted since
// omits the date bound entirely (the full record).
//
// Operator identity source. pjira exposes no "who am I" op (auth-status
// prints only a state), and the issues a search returns include other people's
// (the operator may only be an assignee or watcher), so authorship has to be
// compared against a known operator identity. The identity is, in order:
//
//  1. the JIRA_EMAIL environment variable, read through the backend's getenv
//     seam: it is the same value pjira itself uses (pjira composes defaults,
//     then its config file, then JIRA_EMAIL, so JIRA_EMAIL wins whenever set),
//     and the backend already depends on it (runner.go). No new per-backend
//     config key is introduced.
//  2. when JIRA_EMAIL is unset (pjira may take the account email from its own
//     config file or --config instead), the reporter of the newest issue
//     matching `reporter = currentUser()` (one extra, one-row pjira search),
//     taking that reporter's email and account_id.
//
// Either way, account_ids learned from any author whose email matches are
// remembered for the rest of the call, so a later author record that carries
// only an account_id (no email) still matches. If no identity can be
// established, ListActivity answers `unavailable` naming what is missing and
// emits nothing, never an unscoped result. RESIDUAL GAP: an author who carries
// neither an email nor an account_id this call has learned (display name only,
// or an email hidden by Jira's privacy settings with no account_id learned) is
// never matched, so that happening is omitted rather than over-attributed; if
// issues were returned and NONE of their reporters or comment/changelog authors
// carries a usable identity, the answer is `unavailable` rather than an empty
// success.
//
// Seam for further kinds: ListActivity runs the search once, then calls each
// collector of the form
//
//	func collect...(ctx, me *operatorIdentity, issues []pjiraIssue, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error)
//
// concatenating their items and ORing their truncated flags (and pjira's own
// truncated flag). A new kind family adds one collector and one call in
// ListActivity, builds its items with newActivityItem, and compares
// changelog/comment authors with operatorIdentity.Matches.
//
// Documented item "fields" keys (an object, always present with every key
// listed; the envelope is the contract, these keys are this backend's own):
//
//	issue.created: title, issue_type, priority
//
// Item ids: issue.created happens at most once per issue, so its id is
// "<KEY>#issue.created", identical on every call over any range.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ activity.Provider = (*Backend)(nil)

// EnvEmail is the environment variable pjira (and so this backend) resolves the
// configured Jira account email from; see the identity source above.
const EnvEmail = "JIRA_EMAIL"

// KindIssueCreated is the once-per-issue kind this file emits.
const KindIssueCreated = "issue.created"

// ActivityKinds is the vocabulary this backend contributes to capabilities
// vocabulary.activity_kinds.
var ActivityKinds = []string{KindIssueCreated}

// activityIssueSearchJQL selects every issue the operator is involved in; the
// lower `updated >=` bound is appended by rangedJQL.
const activityIssueSearchJQL = "reporter = currentUser() OR assignee = currentUser() OR watcher = currentUser()"

// operatorIdentityJQL finds the operator's own newest reported issue, used to
// derive the identity when JIRA_EMAIL is unset.
const operatorIdentityJQL = "reporter = currentUser() ORDER BY created DESC"

// operatorIdentity is the resolved operator, compared against issue reporters
// and changelog/comment authors. The zero value matches nothing.
type operatorIdentity struct {
	emails     map[string]bool // lower-cased
	accountIDs map[string]bool
}

func newOperatorIdentity() *operatorIdentity {
	return &operatorIdentity{emails: map[string]bool{}, accountIDs: map[string]bool{}}
}

// empty reports whether the identity holds nothing to match against.
func (o *operatorIdentity) empty() bool {
	return o == nil || (len(o.emails) == 0 && len(o.accountIDs) == 0)
}

// add records a user's email and account_id as the operator's.
func (o *operatorIdentity) add(u pjiraUser) {
	if e := normEmail(u.Email); e != "" {
		o.emails[e] = true
	}
	if id := strings.TrimSpace(u.AccountID); id != "" {
		o.accountIDs[id] = true
	}
}

// learn remembers the account_id of a user whose email is already known to be
// the operator's, so an account_id-only record matches later.
func (o *operatorIdentity) learn(u pjiraUser) {
	if o == nil {
		return
	}
	if e := normEmail(u.Email); e != "" && o.emails[e] {
		if id := strings.TrimSpace(u.AccountID); id != "" {
			o.accountIDs[id] = true
		}
	}
}

// Matches reports whether u is the operator: by email (case-insensitive) or by
// account_id. It NEVER matches an empty identity value or an empty author
// email/account_id.
func (o *operatorIdentity) Matches(u pjiraUser) bool {
	if o.empty() {
		return false
	}
	if e := normEmail(u.Email); e != "" && o.emails[e] {
		return true
	}
	if id := strings.TrimSpace(u.AccountID); id != "" && o.accountIDs[id] {
		return true
	}
	return false
}

// usable reports whether u carries an identity this operator identity could
// match against (an email, or an account_id when account_ids are known).
func (o *operatorIdentity) usable(u pjiraUser) bool {
	if normEmail(u.Email) != "" {
		return true
	}
	return strings.TrimSpace(u.AccountID) != "" && len(o.accountIDs) > 0
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// activityUnavailable builds the unavailable error every activity failure uses.
func activityUnavailable(format string, a ...any) error {
	return scriptout.WrapError(scriptout.ErrUnavailable, "pg-connector-issue-jira: "+fmt.Sprintf(format, a...))
}

// activitySearchError maps a failed pjira run onto `unavailable`: an
// authentication failure names authentication; anything else carries pjira's
// own message.
func activitySearchError(what string, runErr error) error {
	if errors.Is(classifyPJIRAErrorMessage(runErr.Error()), scriptout.ErrUnauthenticated) {
		return activityUnavailable("%s failed: pjira authentication is missing or invalid: %s", what, runErr.Error())
	}
	return activityUnavailable("%s failed: %s", what, runErr.Error())
}

// resolveOperator establishes the operator identity (see the file comment) or
// fails `unavailable`, naming what is missing.
func (b *Backend) resolveOperator(ctx context.Context) (*operatorIdentity, error) {
	me := newOperatorIdentity()
	if email := strings.TrimSpace(b.getenvFunc()(EnvEmail)); email != "" {
		me.add(pjiraUser{Email: email})
		return me, nil
	}

	out, err := b.runner.Run(ctx, "search", "--jql", operatorIdentityJQL, "--limit", "1")
	if err != nil {
		return nil, activitySearchError("operator identity lookup (no "+EnvEmail+" set)", err)
	}
	res, err := decodePJIRASearchResult(out)
	if err != nil {
		return nil, activityUnavailable("operator identity lookup: decode pjira search result: %v", err)
	}
	for _, it := range res.Items {
		if it.Reporter != nil {
			me.add(*it.Reporter)
		}
		if !me.empty() {
			return me, nil
		}
	}
	return nil, activityUnavailable("cannot establish the operator's Jira identity: %s is unset and no issue reported by currentUser() carries a reporter email or account id", EnvEmail)
}

// ListActivity implements activity.Provider. It resolves the operator identity
// first (failing unavailable, never returning an unscoped result), runs the one
// pjira search, then runs each kind collector.
func (b *Backend) ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	me, err := b.resolveOperator(ctx)
	if err != nil {
		return nil, err
	}

	jql := rangedJQL(activityIssueSearchJQL, scriptout.TimeRange{Since: since})
	out, err := b.runner.Run(ctx, "search", "--all", "--expand", "changelog,comments", "--jql", jql)
	if err != nil {
		return nil, activitySearchError("issue search", err)
	}
	result, err := decodePJIRASearchResult(out)
	if err != nil {
		return nil, activityUnavailable("decode pjira search result: %v", err)
	}

	if err := checkAttributable(me, result.Items); err != nil {
		return nil, err
	}

	items := []schema.ActivityItem{}
	truncated := result.Truncated

	created, createdTrunc, err := collectIssueCreated(ctx, me, result.Items, since, before)
	if err != nil {
		return nil, err
	}
	items = append(items, created...)
	truncated = truncated || createdTrunc

	return &schema.ActivityListResult{Items: items, Truncated: truncated}, nil
}

// checkAttributable learns account_ids from matching authors and then fails
// `unavailable` when issues were returned but none carries a usable reporter or
// author identity (a search that returned no issues is a normal empty success).
func checkAttributable(me *operatorIdentity, issues []pjiraIssue) error {
	if len(issues) == 0 {
		return nil
	}
	usable := false
	for i := range issues {
		it := &issues[i]
		if it.Reporter != nil {
			me.learn(*it.Reporter)
		}
		if it.Assignee != nil {
			me.learn(*it.Assignee)
		}
		for _, c := range it.Changelog {
			me.learn(c.Author)
		}
		for _, c := range it.Comments {
			me.learn(c.Author)
		}
	}
	for i := range issues {
		it := &issues[i]
		if it.Reporter != nil && me.usable(*it.Reporter) {
			usable = true
		}
		for _, c := range it.Changelog {
			usable = usable || me.usable(c.Author)
		}
		for _, c := range it.Comments {
			usable = usable || me.usable(c.Author)
		}
	}
	if !usable {
		return activityUnavailable("cannot attribute activity to the operator: %d issue(s) were returned but none carries a reporter or author email or account id", len(issues))
	}
	return nil
}

// newActivityItem builds an item for an issue entity. It does NOT build the
// id: the caller sets item.ID (once-per-entity kinds use "<KEY>#<kind>";
// repeatable kinds append an event id or the occurred_at). fields is marshaled
// as a JSON object ({} when nil or not an object); as_of is the read time in
// RFC3339 UTC and stale is false (this backend keeps no cache).
func newActivityItem(iss *pjiraIssue, kind string, occurredAt time.Time, summary string, fields any) schema.ActivityItem {
	raw := json.RawMessage("{}")
	if fields != nil {
		if b, err := json.Marshal(fields); err == nil && len(b) > 0 && b[0] == '{' {
			raw = b
		}
	}
	return schema.ActivityItem{
		Kind:       kind,
		EntityType: "issue",
		EntityID:   iss.Key,
		OccurredAt: occurredAt.Format(time.RFC3339),
		Summary:    summary,
		URL:        iss.URL,
		Labels:     []string{"project:" + issueProjectKey(iss), "tracker:jira"},
		Fields:     raw,
		AsOf:       time.Now().UTC().Format(time.RFC3339),
		Stale:      false,
	}
}

// issueProjectKey is the issue's Jira project key: pjira's own project field,
// else the key's prefix before its final "-".
func issueProjectKey(iss *pjiraIssue) string {
	if p := strings.TrimSpace(iss.Project); p != "" {
		return p
	}
	if i := strings.LastIndex(iss.Key, "-"); i > 0 {
		return iss.Key[:i]
	}
	return iss.Key
}

// inActivityRange reports whether t lies in [since, before); a zero since
// means no lower bound.
func inActivityRange(t, since, before time.Time) bool {
	if !since.IsZero() && t.Before(since) {
		return false
	}
	return t.Before(before)
}

// collectIssueCreated emits issue.created for issues whose own `created` lies
// in [since, before) and whose reporter is the operator. An issue whose created
// is missing or unparseable cannot be dated and is never emitted. It never
// reports truncated itself: the search's own flag is ORed in by ListActivity.
func collectIssueCreated(_ context.Context, me *operatorIdentity, issues []pjiraIssue, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error) {
	seen := map[string]bool{}
	for i := range issues {
		iss := &issues[i]
		if iss.Key == "" || iss.Reporter == nil || !me.Matches(*iss.Reporter) {
			continue
		}
		created, ok := parseJiraUpdated(iss.Created)
		if !ok || !inActivityRange(created, since, before) {
			continue
		}
		id := iss.Key + "#" + KindIssueCreated
		if seen[id] {
			continue
		}
		seen[id] = true
		summary := "created " + iss.Key
		if t := strings.TrimSpace(iss.Summary); t != "" {
			summary += ": " + t
		}
		it := newActivityItem(iss, KindIssueCreated, created, summary, map[string]any{
			"title":      iss.Summary,
			"issue_type": iss.IssueType,
			"priority":   iss.Priority,
		})
		it.ID = id
		items = append(items, it)
	}
	return items, false, nil
}
