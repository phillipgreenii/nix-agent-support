package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

const actOperatorEmail = "operator@example.com"

var (
	actSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	actBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

// actIssue renders one pjira issue JSON object. created is Jira's raw text;
// reporter is the reporter email ("" renders a reporter with no email at all).
func actIssue(key, created, reporterEmail, updated string) string {
	reporter := `{"display_name":"Someone"}`
	if reporterEmail != "" {
		reporter = `{"email":"` + reporterEmail + `","account_id":"acct-` + reporterEmail + `","display_name":"Someone"}`
	}
	return `{"key":"` + key + `","summary":"summary of ` + key + `","status":"To Do","issuetype":"Bug",` +
		`"url":"https://example.atlassian.net/browse/` + key + `","priority":"High","project":"PROJ",` +
		`"created":"` + created + `","updated":"` + updated + `","reporter":` + reporter + `}`
}

func actSearch(truncated bool, issues ...string) string {
	t := "false"
	if truncated {
		t = "true"
	}
	return `{"items":[` + strings.Join(issues, ",") + `],"truncated":` + t + `}`
}

// newActivityBackend returns a Backend over a fake pjira whose activity search
// (the one carrying --expand) answers searchOut, and whose identity lookup
// (the --limit 1 search) answers identityOut; JIRA_EMAIL resolves to email.
func newActivityBackend(email, searchOut, identityOut string) (*Backend, *fakeRunner) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "search" {
			return "", errors.New("unexpected pjira op " + args[0])
		}
		for _, a := range args {
			if a == "--expand" {
				return searchOut, nil
			}
		}
		return identityOut, nil
	}}
	b := New(fr)
	b.getenv = func(k string) string {
		if k == EnvEmail {
			return email
		}
		return ""
	}
	return b, fr
}

func activityIDs(res *schema.ActivityListResult) map[string]schema.ActivityItem {
	m := map[string]schema.ActivityItem{}
	for _, it := range res.Items {
		m[it.ID] = it
	}
	return m
}

func TestListActivity_EmitsOnlyOperatorsOwnCreations(t *testing.T) {
	b, fr := newActivityBackend(actOperatorEmail, actSearch(
		false,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
		// Someone else created these; the operator is only an assignee/watcher.
		actIssue("PROJ-2", "2026-09-05T10:00:00.000+0000", "other@example.com", "2026-09-06T00:00:00.000+0000"),
		actIssue("PROJ-3", "2026-09-07T10:00:00.000+0000", "third@example.com", "2026-09-07T00:00:00.000+0000"),
	), "")

	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != "PROJ-1#issue.created" {
		t.Fatalf("items = %+v, want exactly PROJ-1#issue.created (other people's issues excluded)", res.Items)
	}
	if res.Truncated {
		t.Errorf("truncated = true, want false")
	}
	// The search carries --all and --expand changelog,comments; an identity
	// lookup is NOT needed when JIRA_EMAIL is set.
	if len(fr.calls) != 1 {
		t.Fatalf("pjira calls = %v, want one search", fr.calls)
	}
	call := strings.Join(fr.calls[0], " ")
	for _, want := range []string{"search", "--all", "--expand changelog,comments", "--jql"} {
		if !strings.Contains(call, want) {
			t.Errorf("call %q missing %q", call, want)
		}
	}
}

func TestListActivity_ItemShapeAndFieldsPinned(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(
		false,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
	), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	it := res.Items[0]
	if it.ID != "PROJ-1#issue.created" || it.Kind != KindIssueCreated || it.EntityType != "issue" || it.EntityID != "PROJ-1" {
		t.Errorf("identity fields = %+v", it)
	}
	if it.OccurredAt != "2026-09-05T10:00:00Z" {
		t.Errorf("occurred_at = %q, want the issue's own created, RFC3339", it.OccurredAt)
	}
	if it.Summary != "created PROJ-1: summary of PROJ-1" {
		t.Errorf("summary = %q", it.Summary)
	}
	if it.URL != "https://example.atlassian.net/browse/PROJ-1" {
		t.Errorf("url = %q", it.URL)
	}
	if got := strings.Join(it.Labels, ","); got != "project:PROJ,tracker:jira" {
		t.Errorf("labels = %q", got)
	}
	if it.Stale {
		t.Errorf("stale = true, want false")
	}
	if _, perr := time.Parse(time.RFC3339, it.AsOf); perr != nil {
		t.Errorf("as_of %q: %v", it.AsOf, perr)
	}
	// The documented fields keys, pinned.
	if got, want := string(it.Fields), `{"issue_type":"Bug","priority":"High","title":"summary of PROJ-1"}`; got != want {
		t.Errorf("fields = %s, want %s", got, want)
	}
}

func TestListActivity_RangeCutsExactlyByCreated(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(
		false,
		// Matched the search on updated only: created is before since.
		actIssue("PROJ-1", "2026-08-31T23:59:59.000+0000", actOperatorEmail, "2026-09-10T00:00:00.000+0000"),
		// Exactly at since: emitted (since is inclusive).
		actIssue("PROJ-2", "2026-09-01T00:00:00.000+0000", actOperatorEmail, "2026-09-10T00:00:00.000+0000"),
		// Exactly at before: not emitted (before is exclusive).
		actIssue("PROJ-3", "2026-10-01T00:00:00.000+0000", actOperatorEmail, "2026-10-02T00:00:00.000+0000"),
		// Created in range but updated AFTER before: still emitted.
		actIssue("PROJ-4", "2026-09-20T00:00:00Z", actOperatorEmail, "2026-11-01T00:00:00.000+0000"),
		// No created, and an unparseable created: cannot be dated, not emitted.
		actIssue("PROJ-5", "", actOperatorEmail, "2026-09-10T00:00:00.000+0000"),
		actIssue("PROJ-6", "not a time", actOperatorEmail, "2026-09-10T00:00:00.000+0000"),
		// A non-UTC offset in the second layout (no fractional seconds): 23:30 on
		// Aug 31 at -0600 is 05:30 UTC on Sep 1, so in range.
		actIssue("PROJ-7", "2026-08-31T23:30:00-0600", actOperatorEmail, "2026-09-10T00:00:00.000+0000"),
	), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	got := activityIDs(res)
	for _, want := range []string{"PROJ-2#issue.created", "PROJ-4#issue.created", "PROJ-7#issue.created"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("ids = %v, want exactly PROJ-2, PROJ-4, PROJ-7", got)
	}
	if it := got["PROJ-7#issue.created"]; it.OccurredAt != "2026-08-31T23:30:00-06:00" {
		t.Errorf("PROJ-7 occurred_at = %q, want its own created with its own offset", it.OccurredAt)
	}
}

func TestListActivity_JQLHasNoUpperUpdatedBoundAndOmittedSinceHasNoLowerBound(t *testing.T) {
	b, fr := newActivityBackend(actOperatorEmail, actSearch(false), "")
	if _, err := b.ListActivity(context.Background(), actSince, actBefore); err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	jql := jqlOf(t, fr.calls[0])
	if strings.Contains(jql, "updated <") {
		t.Errorf("jql %q carries an upper updated bound", jql)
	}
	// Lower bound widened by one UTC day.
	if !strings.Contains(jql, `updated >= "2026-08-31"`) {
		t.Errorf("jql %q, want updated >= the since date minus one day", jql)
	}
	for _, want := range []string{"reporter = currentUser()", "assignee = currentUser()", "watcher = currentUser()"} {
		if !strings.Contains(jql, want) {
			t.Errorf("jql %q missing %q", jql, want)
		}
	}

	b2, fr2 := newActivityBackend(actOperatorEmail, actSearch(false), "")
	if _, err := b2.ListActivity(context.Background(), time.Time{}, actBefore); err != nil {
		t.Fatalf("ListActivity (since omitted): %v", err)
	}
	if jql2 := jqlOf(t, fr2.calls[0]); strings.Contains(jql2, "updated") {
		t.Errorf("since omitted: jql %q carries a date bound", jql2)
	}
}

func jqlOf(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "--jql" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("no --jql in %v", args)
	return ""
}

func TestListActivity_TruncatedFollowsPjira(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(
		true,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
	), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if !res.Truncated {
		t.Errorf("truncated = false, want true when pjira reports truncated")
	}
}

func TestListActivity_NoInRangeIssuesSerializesEmptyArray(t *testing.T) {
	for name, searchOut := range map[string]string{
		"no issues":     actSearch(false),
		"none in range": actSearch(false, actIssue("PROJ-1", "2026-01-01T00:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000")),
	} {
		b, _ := newActivityBackend(actOperatorEmail, searchOut, "")
		res, err := b.ListActivity(context.Background(), actSince, actBefore)
		if err != nil {
			t.Fatalf("%s: ListActivity: %v", name, err)
		}
		raw, _ := json.Marshal(res)
		if !strings.Contains(string(raw), `"items":[]`) {
			t.Errorf("%s: result = %s, want items as []", name, raw)
		}
	}
}

func TestListActivity_IDStableAcrossOverlappingRanges(t *testing.T) {
	out := actSearch(false, actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"))
	b, _ := newActivityBackend(actOperatorEmail, out, "")
	r1, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := b.ListActivity(context.Background(), actSince.AddDate(0, 0, 3), actBefore.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Items) != 1 || len(r2.Items) != 1 || r1.Items[0].ID != r2.Items[0].ID {
		t.Fatalf("ids differ across overlapping ranges: %+v vs %+v", r1.Items, r2.Items)
	}
}

func TestListActivity_DuplicateIssueEmittedOnce(t *testing.T) {
	one := actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000")
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, one, one), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v, want one", res.Items)
	}
}

func TestListActivity_EmailMatchIsCaseInsensitive(t *testing.T) {
	b, _ := newActivityBackend("Operator@Example.COM", actSearch(
		false,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
	), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("res=%+v err=%v, want one item", res, err)
	}
}

// ----- identity: unavailable paths -----

func requireUnavailable(t *testing.T, err error, mustName ...string) {
	t.Helper()
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	for _, s := range mustName {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(s)) {
			t.Errorf("err %q does not name %q", err, s)
		}
	}
}

func TestListActivity_UnavailableWhenIdentityMissing(t *testing.T) {
	// JIRA_EMAIL unset and the identity lookup finds no issue at all.
	b, fr := newActivityBackend("", actSearch(false, actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000")), actSearch(false))
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err, EnvEmail)
	if res != nil {
		t.Errorf("result = %+v, want none", res)
	}
	for _, c := range fr.calls {
		for _, a := range c {
			if a == "--expand" {
				t.Errorf("activity search ran despite no identity: %v", c)
			}
		}
	}

	// The lookup's reporter carries no email and no account id.
	b2, _ := newActivityBackend("", "", actSearch(false, `{"key":"PROJ-9","reporter":{"display_name":"Someone"}}`))
	_, err = b2.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err, EnvEmail)
}

func TestListActivity_IdentityDerivedFromReporterWhenEmailUnset(t *testing.T) {
	identity := actSearch(false, actIssue("PROJ-9", "2026-01-01T00:00:00.000+0000", actOperatorEmail, "2026-01-01T00:00:00.000+0000"))
	b, fr := newActivityBackend("", actSearch(
		false,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
		actIssue("PROJ-2", "2026-09-05T10:00:00.000+0000", "other@example.com", "2026-09-06T00:00:00.000+0000"),
	), identity)
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != "PROJ-1#issue.created" {
		t.Fatalf("items = %+v, want only PROJ-1", res.Items)
	}
	if jql := jqlOf(t, fr.calls[0]); jql != operatorIdentityJQL {
		t.Errorf("first call jql = %q, want the identity lookup", jql)
	}
}

func TestListActivity_AuthFailureIsUnavailableNamingAuthentication(t *testing.T) {
	// On the activity search.
	b := New(&fakeRunner{handle: func([]string) (string, error) {
		return "", errors.New("pjira: search: status 401 Unauthorized")
	}})
	b.getenv = func(string) string { return actOperatorEmail }
	_, err := b.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err, "authentication")

	// On the identity lookup (JIRA_EMAIL unset).
	b2 := New(&fakeRunner{handle: func([]string) (string, error) {
		return "", errors.New("pjira: search: status 403 Forbidden")
	}})
	b2.getenv = func(string) string { return "" }
	_, err = b2.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err, "authentication")
}

func TestListActivity_OtherPjiraFailureIsUnavailable(t *testing.T) {
	b := New(&fakeRunner{handle: func([]string) (string, error) {
		return "", errors.New("pjira: issue PROJ-1 not found")
	}})
	b.getenv = func(string) string { return actOperatorEmail }
	_, err := b.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err)
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("a failed list_activity search must not answer not_found: %v", err)
	}

	b2, _ := newActivityBackend(actOperatorEmail, "not json", "")
	_, err = b2.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err)
}

func TestListActivity_AllAuthorsWithoutEmailIsUnavailableNotEmpty(t *testing.T) {
	noEmail := `{"key":"PROJ-1","summary":"s","project":"PROJ","created":"2026-09-05T10:00:00.000+0000","reporter":{"display_name":"Someone"}}`
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, noEmail), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	requireUnavailable(t, err, "email")
	if res != nil {
		t.Errorf("result = %+v, want none", res)
	}
}

func TestListActivity_AccountIDOnlyReporterMatchesOnceLearned(t *testing.T) {
	// Issue A proves operator@example.com's account id; issue B's reporter is
	// the same person but Jira returned only the account id for it.
	idOnly := `{"key":"PROJ-2","summary":"s","project":"PROJ","created":"2026-09-06T10:00:00.000+0000","reporter":{"account_id":"acct-` + actOperatorEmail + `"}}`
	b, _ := newActivityBackend(actOperatorEmail, actSearch(
		false,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
		idOnly,
	), "")
	res, err := b.ListActivity(context.Background(), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	got := activityIDs(res)
	if _, ok := got["PROJ-2#issue.created"]; !ok || len(got) != 2 {
		t.Fatalf("ids = %v, want PROJ-1 and the account-id-only PROJ-2", got)
	}
}

func TestOperatorIdentity_Matches(t *testing.T) {
	me := newOperatorIdentity()
	me.add(pjiraUser{Email: " Operator@Example.com ", AccountID: "a1"})
	cases := []struct {
		name string
		u    pjiraUser
		want bool
	}{
		{"email", pjiraUser{Email: "operator@example.com"}, true},
		{"account id", pjiraUser{AccountID: "a1"}, true},
		{"other email", pjiraUser{Email: "x@example.com"}, false},
		{"empty", pjiraUser{}, false},
		{"display name only", pjiraUser{DisplayName: "operator@example.com"}, false},
	}
	for _, c := range cases {
		if got := me.Matches(c.u); got != c.want {
			t.Errorf("%s: Matches = %v, want %v", c.name, got, c.want)
		}
	}
	// An empty identity matches nothing, including an empty author.
	empty := newOperatorIdentity()
	empty.add(pjiraUser{})
	for _, u := range []pjiraUser{{}, {Email: "a@b.c"}, {AccountID: "a1"}} {
		if empty.Matches(u) {
			t.Errorf("empty identity matched %+v", u)
		}
	}
	var nilMe *operatorIdentity
	if nilMe.Matches(pjiraUser{Email: "a@b.c"}) {
		t.Error("nil identity matched")
	}
}

func TestListActivity_ConformsToListActivityCase(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(
		false,
		actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"),
		actIssue("PROJ-2", "2026-09-05T10:00:00.000+0000", "other@example.com", "2026-09-06T00:00:00.000+0000"),
	), "")
	backend := conformance.TableBackend{Table: activity.NewDispatchTable(b)}
	for _, since := range []time.Time{actSince, {}} {
		for _, r := range conformance.RunListActivityCase(context.Background(), backend, since, actBefore) {
			if r.Err != nil {
				t.Errorf("%s (since=%v): %v", r.Name, since, r.Err)
			}
		}
	}
}

// ----- issue.transitioned and issue.commented -----

const actOtherEmail = "other@example.com"

func actUser(email string) string {
	return `{"email":"` + email + `","account_id":"acct-` + email + `","display_name":"Someone"}`
}

// actChange renders one pjira changelog entry.
func actChange(id, from, to, authorEmail, at string) string {
	return `{"id":"` + id + `","field":"status","from":"` + from + `","to":"` + to + `","author":` + actUser(authorEmail) + `,"at":"` + at + `"}`
}

// actComment renders one pjira comment.
func actComment(id, authorEmail, body, created string) string {
	return `{"id":"` + id + `","author":` + actUser(authorEmail) + `,"body":"` + body + `","created":"` + created + `"}`
}

// actIssueWith renders an issue (created long before any range, reported by
// someone else) carrying the given changelog and comment entries.
func actIssueWith(key string, changelog, comments []string) string {
	base := actIssue(key, "2026-01-01T00:00:00.000+0000", actOtherEmail, "2026-09-30T00:00:00.000+0000")
	return strings.TrimSuffix(base, "}") +
		`,"changelog":[` + strings.Join(changelog, ",") + `],"comments":[` + strings.Join(comments, ",") + `]}`
}

func listIDs(t *testing.T, b *Backend, since, before time.Time) map[string]schema.ActivityItem {
	t.Helper()
	res, err := b.ListActivity(context.Background(), since, before)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	m := activityIDs(res)
	if len(m) != len(res.Items) {
		t.Fatalf("duplicate ids in %+v", res.Items)
	}
	return m
}

func TestListActivity_TransitionsAndCommentsOnlyOperators(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, actIssueWith("PROJ-1",
		[]string{
			actChange("100", "To Do", "In Progress", actOperatorEmail, "2026-09-05T10:00:00.000+0000"),
			actChange("101", "In Progress", "Done", actOtherEmail, "2026-09-06T10:00:00.000+0000"),
			actChange("102", "Done", "To Do", actOperatorEmail, "2026-09-07T10:00:00.000+0000"),
		},
		[]string{
			actComment("200", actOperatorEmail, "mine", "2026-09-05T11:00:00.000+0000"),
			actComment("201", actOtherEmail, "theirs", "2026-09-05T12:00:00.000+0000"),
		})), "")
	got := listIDs(t, b, actSince, actBefore)
	want := []string{"PROJ-1#issue.transitioned#100", "PROJ-1#issue.transitioned#102", "PROJ-1#issue.commented#200"}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("missing %s in %v", id, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("ids = %v, want exactly %v (other people's entries produce nothing)", got, want)
	}
}

func TestListActivity_TransitionAndCommentShapeAndFieldsPinned(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, actIssueWith("PROJ-1",
		[]string{actChange("100", "To Do", "In Progress", actOperatorEmail, "2026-09-05T10:00:00.000+0000")},
		[]string{actComment("200", actOperatorEmail, "hello there", "2026-09-05T11:30:00.000+0000")})), "")
	got := listIDs(t, b, actSince, actBefore)

	tr := got["PROJ-1#issue.transitioned#100"]
	if tr.Kind != KindIssueTransitioned || tr.EntityType != "issue" || tr.EntityID != "PROJ-1" {
		t.Errorf("transition identity = %+v", tr)
	}
	if tr.OccurredAt != "2026-09-05T10:00:00Z" {
		t.Errorf("transition occurred_at = %q, want the entry's own at (not the issue's updated)", tr.OccurredAt)
	}
	if got, want := string(tr.Fields), `{"from":"To Do","to":"In Progress"}`; got != want {
		t.Errorf("transition fields = %s, want %s (no resolution key invented)", got, want)
	}
	if strings.Contains(string(tr.Fields), "resolution") {
		t.Errorf("transition fields carry a resolution key: %s", tr.Fields)
	}
	if got := strings.Join(tr.Labels, ","); got != "project:PROJ,tracker:jira" {
		t.Errorf("labels = %q", got)
	}

	cm := got["PROJ-1#issue.commented#200"]
	if cm.Kind != KindIssueCommented || cm.EntityType != "issue" || cm.EntityID != "PROJ-1" {
		t.Errorf("comment identity = %+v", cm)
	}
	if cm.OccurredAt != "2026-09-05T11:30:00Z" {
		t.Errorf("comment occurred_at = %q, want the comment's own created", cm.OccurredAt)
	}
	if got, want := string(cm.Fields), `{"body_excerpt":"hello there","comment_id":"200"}`; got != want {
		t.Errorf("comment fields = %s, want %s", got, want)
	}
}

func TestListActivity_CommentExcerptIsCapped(t *testing.T) {
	long := strings.Repeat("x", commentExcerptRunes+50)
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, actIssueWith("PROJ-1", nil,
		[]string{actComment("200", actOperatorEmail, long, "2026-09-05T11:30:00.000+0000")})), "")
	it := listIDs(t, b, actSince, actBefore)["PROJ-1#issue.commented#200"]
	var f struct {
		Excerpt string `json:"body_excerpt"`
	}
	if err := json.Unmarshal(it.Fields, &f); err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("x", commentExcerptRunes) + "..."; f.Excerpt != want {
		t.Errorf("excerpt = %q, want capped and ellipsized", f.Excerpt)
	}
}

func TestListActivity_TwoTransitionsOfOneIssueDistinctAndStable(t *testing.T) {
	out := actSearch(false, actIssueWith("PROJ-1", []string{
		actChange("100", "To Do", "In Progress", actOperatorEmail, "2026-09-05T10:00:00.000+0000"),
		actChange("101", "In Progress", "To Do", actOperatorEmail, "2026-09-08T10:00:00.000+0000"),
	}, nil))
	b, _ := newActivityBackend(actOperatorEmail, out, "")
	r1 := listIDs(t, b, actSince, actBefore)
	if len(r1) != 2 {
		t.Fatalf("items = %v, want two distinct", r1)
	}
	// An overlapping range that still contains both yields identical ids.
	r2 := listIDs(t, b, actSince.AddDate(0, 0, 2), actBefore.AddDate(0, 1, 0))
	for id := range r1 {
		if _, ok := r2[id]; !ok {
			t.Errorf("id %s not stable across overlapping ranges: %v", id, r2)
		}
	}
}

func TestListActivity_SharedHistoryIDDisambiguatedStablyRegardlessOfFilters(t *testing.T) {
	// Two status entries inside one history (id 100), plus a unique entry 101.
	// The second shared entry is by someone else / outside a narrower range.
	out := actSearch(false, actIssueWith("PROJ-1", []string{
		actChange("100", "To Do", "In Progress", actOtherEmail, "2026-09-05T10:00:00.000+0000"),
		actChange("100", "In Progress", "In Review", actOperatorEmail, "2026-09-05T10:00:00.000+0000"),
		actChange("101", "In Review", "Done", actOperatorEmail, "2026-09-09T10:00:00.000+0000"),
	}, nil))
	b, _ := newActivityBackend(actOperatorEmail, out, "")

	// The other person's entry (position 0) is filtered out by author, yet the
	// operator's keeps position 1, and the unique id carries no suffix.
	r1 := listIDs(t, b, actSince, actBefore)
	if _, ok := r1["PROJ-1#issue.transitioned#100#1"]; !ok {
		t.Errorf("want PROJ-1#issue.transitioned#100#1 in %v", r1)
	}
	if _, ok := r1["PROJ-1#issue.transitioned#101"]; !ok {
		t.Errorf("want unsuffixed unique id 101 in %v", r1)
	}
	if len(r1) != 2 {
		t.Errorf("ids = %v, want exactly two", r1)
	}

	// A range that excludes the unique entry leaves the shared one unchanged.
	r2 := listIDs(t, b, actSince, time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	if _, ok := r2["PROJ-1#issue.transitioned#100#1"]; !ok || len(r2) != 1 {
		t.Errorf("narrower range ids = %v, want only ...#100#1", r2)
	}

	// Both operator-authored: two distinct ids #0 and #1.
	both := actSearch(false, actIssueWith("PROJ-1", []string{
		actChange("100", "To Do", "In Progress", actOperatorEmail, "2026-09-05T10:00:00.000+0000"),
		actChange("100", "In Progress", "In Review", actOperatorEmail, "2026-09-05T10:00:00.000+0000"),
	}, nil))
	b2, _ := newActivityBackend(actOperatorEmail, both, "")
	r3 := listIDs(t, b2, actSince, actBefore)
	for _, id := range []string{"PROJ-1#issue.transitioned#100#0", "PROJ-1#issue.transitioned#100#1"} {
		if _, ok := r3[id]; !ok {
			t.Errorf("missing %s in %v", id, r3)
		}
	}
}

func TestListActivity_TransitionAndCommentRangeIsExactAndOwnTimestamp(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, actIssueWith("PROJ-1",
		[]string{
			actChange("1", "A", "B", actOperatorEmail, "2026-08-31T23:59:59.000+0000"), // before since
			actChange("2", "B", "C", actOperatorEmail, "2026-09-01T00:00:00.000+0000"), // at since: in
			actChange("3", "C", "D", actOperatorEmail, "2026-10-01T00:00:00.000+0000"), // at before: out
			actChange("4", "D", "E", actOperatorEmail, ""),                             // undatable
			actChange("5", "E", "F", actOperatorEmail, "not a time"),                   // unparseable
		},
		[]string{
			actComment("10", actOperatorEmail, "a", "2026-08-31T23:59:59.000+0000"),
			actComment("11", actOperatorEmail, "b", "2026-09-01T00:00:00.000+0000"),
			actComment("12", actOperatorEmail, "c", "2026-10-01T00:00:00.000+0000"),
			actComment("13", actOperatorEmail, "d", ""),
			actComment("14", actOperatorEmail, "e", "garbage"),
		})), "")
	got := listIDs(t, b, actSince, actBefore)
	if _, ok := got["PROJ-1#issue.transitioned#2"]; !ok {
		t.Errorf("entry exactly at since not emitted: %v", got)
	}
	if _, ok := got["PROJ-1#issue.commented#11"]; !ok {
		t.Errorf("comment exactly at since not emitted: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("ids = %v, want only the two at since (issue updated is in range but must not stand in)", got)
	}
}

func TestListActivity_TransitionIgnoresNonStatusAndIDLessEntries(t *testing.T) {
	out := actSearch(false, actIssueWith("PROJ-1", []string{
		`{"id":"7","field":"resolution","from":"","to":"Fixed","author":` + actUser(actOperatorEmail) + `,"at":"2026-09-05T10:00:00.000+0000"}`,
		`{"field":"status","from":"A","to":"B","author":` + actUser(actOperatorEmail) + `,"at":"2026-09-05T10:00:00.000+0000"}`,
	}, []string{actComment("", actOperatorEmail, "no id", "2026-09-05T10:00:00.000+0000")}))
	b, _ := newActivityBackend(actOperatorEmail, out, "")
	if got := listIDs(t, b, actSince, actBefore); len(got) != 0 {
		t.Errorf("ids = %v, want none (no stable id can be built)", got)
	}
}

func TestListActivity_AccountIDOnlyTransitionAuthorMatches(t *testing.T) {
	// The reporter proves the operator's account id (via the matching email);
	// the comment author then carries only the account id.
	issue := strings.TrimSuffix(actIssue("PROJ-1", "2026-09-05T10:00:00.000+0000", actOperatorEmail, "2026-09-06T00:00:00.000+0000"), "}") +
		`,"comments":[{"id":"9","author":{"account_id":"acct-` + actOperatorEmail + `"},"body":"b","created":"2026-09-06T10:00:00.000+0000"}]}`
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, issue), "")
	got := listIDs(t, b, actSince, actBefore)
	if _, ok := got["PROJ-1#issue.commented#9"]; !ok {
		t.Errorf("account-id-only comment author not matched: %v", got)
	}
}

func TestListActivity_RepeatedIssueEmitsEntriesOnce(t *testing.T) {
	one := actIssueWith("PROJ-1",
		[]string{actChange("100", "A", "B", actOperatorEmail, "2026-09-05T10:00:00.000+0000")},
		[]string{actComment("200", actOperatorEmail, "x", "2026-09-05T11:00:00.000+0000")})
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, one, one), "")
	if got := listIDs(t, b, actSince, actBefore); len(got) != 2 {
		t.Errorf("ids = %v, want one transition and one comment", got)
	}
}

func TestListActivity_ConformanceWithTransitionsAndComments(t *testing.T) {
	b, _ := newActivityBackend(actOperatorEmail, actSearch(false, actIssueWith("PROJ-1",
		[]string{actChange("100", "A", "B", actOperatorEmail, "2026-09-05T10:00:00.000+0000")},
		[]string{actComment("200", actOperatorEmail, "x", "2026-09-05T11:00:00.000+0000")})), "")
	backend := conformance.TableBackend{Table: activity.NewDispatchTable(b)}
	for _, since := range []time.Time{actSince, {}} {
		for _, r := range conformance.RunListActivityCase(context.Background(), backend, since, actBefore) {
			if r.Err != nil {
				t.Errorf("%s (since=%v): %v", r.Name, since, r.Err)
			}
		}
	}
}
