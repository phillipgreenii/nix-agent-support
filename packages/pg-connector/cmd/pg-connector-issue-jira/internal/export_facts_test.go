package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// factsFetched decodes an expanded search item as the detail fetcher holds it.
func factsFetched(t *testing.T, assigneeEmail, status, changelog, comments string) *PjiraIssue {
	t.Helper()
	assignee := ""
	if assigneeEmail != "" {
		assignee = `"assignee":{"email":"` + assigneeEmail + `"},`
	}
	raw := `{"items":[{"key":"PROJ-1",` + assignee + `"summary":"s","status":"` + status + `","created":"2026-08-01T00:00:00.000+0000","changelog":[` + changelog + `],"comments":[` + comments + `]}],"truncated":false}`
	res, err := DecodeSearchResult(raw)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("decode: %v (%d items)", err, len(res.Items))
	}
	return &res.Items[0]
}

func TestOperatorFactsComputesBothFactsFromTheIssueAsFetchedWithNoSearch(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("OperatorFacts must make no pjira call: " + args[0])
	}}
	b := New(fr).WithGetenv(fakeEnv(map[string]string{EnvEmail: factsOperator}))
	iss := factsFetched(t, factsOperator, "In Progress",
		factsTransition("1", "In Progress", "other@example.com", "2026-09-01T10:00:00.000+0000"),
		factsComment("9", factsOperator, "2026-09-10T08:30:00.000+0000"))
	changed, updated := b.OperatorFacts(context.Background(), iss)
	if changed != "2026-09-01T10:00:00Z" || updated != "2026-09-10T08:30:00Z" {
		t.Errorf("facts = %q, %q, want 2026-09-01T10:00:00Z, 2026-09-10T08:30:00Z", changed, updated)
	}
	if len(fr.calls) != 0 {
		t.Errorf("pjira was called: %v", fr.calls)
	}
}

func TestOperatorFactsIsEmptyForEveryOtherIssue(t *testing.T) {
	b := New(&fakeRunner{}).WithGetenv(fakeEnv(map[string]string{EnvEmail: factsOperator}))
	ctx := context.Background()
	cases := map[string]*PjiraIssue{
		"assigned to someone else": factsFetched(t, "other@example.com", "In Progress", "", ""),
		"unassigned":               factsFetched(t, "", "In Progress", "", ""),
		"nil":                      nil,
	}
	for name, iss := range cases {
		if c, u := b.OperatorFacts(ctx, iss); c != "" || u != "" {
			t.Errorf("%s: facts = %q, %q, want both empty", name, c, u)
		}
	}
	bad := factsFetched(t, factsOperator, "In Progress", "", "")
	bad.Key = `PROJ-1" OR 1=1`
	if c, u := b.OperatorFacts(ctx, bad); c != "" || u != "" {
		t.Errorf("a key outside the key shape: facts = %q, %q, want both empty", c, u)
	}
}

func TestOperatorFactsMatchesTheShowFactsForTheSameIssue(t *testing.T) {
	cl := factsTransition("1", "In Progress", factsOperator, "2026-09-02T10:00:00.000+0000")
	co := factsComment("9", "OPERATOR@example.com", "2026-09-10T08:30:00.000+0000")
	search := factsSearchIssue("PROJ-1", "In Progress", "2026-08-01T00:00:00.000+0000", cl, co)
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "issue" {
			return factsIssueJSON("PROJ-1", "In Progress", factsOperator), nil
		}
		return search, nil
	}}
	b := New(fr).WithGetenv(fakeEnv(map[string]string{EnvEmail: factsOperator}))
	shown, err := b.Show(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := DecodeSearchResult(search)
	if err != nil {
		t.Fatal(err)
	}
	// The assignee rides on the expanded item the daemon fetches.
	res.Items[0].Assignee = &pjiraUser{Email: factsOperator}
	changed, updated := b.OperatorFacts(context.Background(), &res.Items[0])
	if changed != shown.StatusChangedAt || updated != shown.OperatorUpdatedAt {
		t.Errorf("OperatorFacts = %q, %q; Show = %q, %q", changed, updated, shown.StatusChangedAt, shown.OperatorUpdatedAt)
	}
}

func TestOperatorFactsResolvesTheIdentityOnceAndCachesIt(t *testing.T) {
	identitySearches := 0
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		identitySearches++
		out, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
			"key": "PROJ-9", "reporter": map[string]any{"email": factsOperator},
		}}})
		return string(out), nil
	}}
	// No JIRA email in the environment: the identity comes from one search.
	b := New(fr).WithGetenv(fakeEnv(nil))
	ctx := context.Background()
	for range 3 {
		iss := factsFetched(t, factsOperator, "In Progress", "", factsComment("9", factsOperator, "2026-09-10T08:30:00.000+0000"))
		if _, u := b.OperatorFacts(ctx, iss); u != "2026-09-10T08:30:00Z" {
			t.Fatalf("operator fact = %q", u)
		}
	}
	if identitySearches != 1 {
		t.Errorf("identity searches = %d, want exactly 1", identitySearches)
	}
}

func TestOperatorFactsDoesNotCacheAFailedResolutionAndNeverLeaksLearnedIDs(t *testing.T) {
	fail := true
	fr := &fakeRunner{handle: func([]string) (string, error) {
		if fail {
			return "", errors.New("pjira: search: status 500")
		}
		return `{"items":[{"key":"PROJ-9","reporter":{"email":"` + factsOperator + `","account_id":"acct-1"}}]}`, nil
	}}
	b := New(fr).WithGetenv(fakeEnv(nil))
	ctx := context.Background()
	iss := factsFetched(t, factsOperator, "In Progress", "", factsComment("9", factsOperator, "2026-09-10T08:30:00.000+0000"))
	if c, u := b.OperatorFacts(ctx, iss); c != "" || u != "" {
		t.Fatalf("an unresolvable operator gave facts %q, %q", c, u)
	}
	fail = false
	if _, u := b.OperatorFacts(ctx, iss); u == "" {
		t.Error("the failure was cached: the next call did not retry")
	}
	// An account id learned from one issue stays out of the cached identity.
	cached, err := b.cachedOperator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cached.add(pjiraUser{AccountID: "scratch"})
	again, _ := b.cachedOperator(ctx)
	if again.accountIDs["scratch"] {
		t.Error("a caller's change to its identity copy reached the cache")
	}
}
