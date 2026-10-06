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
