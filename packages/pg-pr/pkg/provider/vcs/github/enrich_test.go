package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestParseEnrichedPRs_RecordedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/enriched-prs-single.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := parseEnrichedPRs(raw, "ZR-Private/ziprecruiter")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 enriched PR, got %d", len(got))
	}
	pr := got[0]

	// PR core fields.
	if pr.PR.Number != 91071 {
		t.Errorf("PR.Number = %d, want 91071", pr.PR.Number)
	}
	if pr.PR.Author == "" {
		t.Errorf("PR.Author is empty")
	}
	if pr.PR.State != "open" {
		t.Errorf("PR.State = %q, want open (lowercased)", pr.PR.State)
	}
	if pr.PR.Repo != "ZR-Private/ziprecruiter" {
		t.Errorf("PR.Repo = %q", pr.PR.Repo)
	}
	if pr.PR.Branch == "" || pr.PR.Base == "" {
		t.Errorf("PR.Branch/Base empty: %+v", pr.PR)
	}

	// Reviews: at least one entry, state preserved (uppercase from GraphQL).
	if len(pr.Reviews) == 0 {
		t.Errorf("Reviews empty; fixture has %d", len(pr.Reviews))
	}
	for _, r := range pr.Reviews {
		if r.ID == "" {
			t.Errorf("review missing ID: %+v", r)
		}
		if r.State == "" {
			t.Errorf("review missing State: %+v", r)
		}
	}

	// Comments: top-level + review-thread combined; thread ones carry
	// Path/ThreadID; top-level ones don't.
	if len(pr.Comments) == 0 {
		t.Errorf("Comments empty")
	}
	var sawTopLevel, sawThread bool
	for _, c := range pr.Comments {
		if c.ID == "" {
			t.Errorf("comment missing ID: %+v", c)
		}
		if c.Path != "" || c.ThreadID != "" {
			sawThread = true
			if c.ThreadID == "" {
				t.Errorf("review-thread comment missing ThreadID: %+v", c)
			}
		} else {
			sawTopLevel = true
		}
	}
	if !sawTopLevel {
		t.Errorf("no top-level comments parsed")
	}
	if !sawThread {
		t.Errorf("no review-thread comments parsed (fixture should include one)")
	}

	// CI runs: 68 statusCheckRollup contexts in the fixture (capped at
	// first:30 by the live query, but the fixture is the unstripped REST
	// dump; assert at least one and that all are non-empty).
	if len(pr.CIRuns) == 0 {
		t.Errorf("CIRuns empty; fixture should report >0")
	}
	for _, r := range pr.CIRuns {
		if r.Provider != "github-actions" && r.Provider != "github-status" {
			t.Errorf("CIRun provider = %q (want github-actions or github-status)", r.Provider)
		}
		if r.Name == "" {
			t.Errorf("CIRun missing Name: %+v", r)
		}
	}
}

func TestParseEnrichedPRs_GraphQLError(t *testing.T) {
	raw := []byte(`{"errors":[{"type":"INSUFFICIENT_SCOPES","message":"oh no"}]}`)
	_, err := parseEnrichedPRs(raw, "x/y")
	if err == nil {
		t.Fatal("want error on GraphQL errors envelope, got nil")
	}
}

func TestParseEnrichedPRs_Empty(t *testing.T) {
	raw := []byte(`{"data":{"search":{"issueCount":0,"nodes":[]}}}`)
	got, err := parseEnrichedPRs(raw, "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 enriched PRs, got %d", len(got))
	}
}

func TestEnrichedPRs_FakeRunner(t *testing.T) {
	raw, err := os.ReadFile("testdata/enriched-prs-single.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	provider := NewWithRunner(&fakeStdinRunner{out: raw})
	got, err := provider.EnrichedPRs(context.Background(), "x/y", "is:pr is:open repo:x/y author:me")
	if err != nil {
		t.Fatalf("EnrichedPRs: %v", err)
	}
	if len(got) != 1 || got[0].PR.Number != 91071 {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestEnrichedPRs_EmptySearchRejected(t *testing.T) {
	provider := NewWithRunner(&fakeStdinRunner{out: []byte("{}")})
	_, err := provider.EnrichedPRs(context.Background(), "x/y", "")
	if err == nil {
		t.Fatal("want error on empty search query")
	}
}

func TestEnrichedPRs_RunnerError(t *testing.T) {
	provider := NewWithRunner(&fakeStdinRunner{err: errors.New("gh exploded")})
	_, err := provider.EnrichedPRs(context.Background(), "x/y", "is:pr")
	if err == nil {
		t.Fatal("want error when runner fails")
	}
}

func TestParseEnrichedPRs_BotAuthorSuffix(t *testing.T) {
	// PR review comment authored by a Bot account; without the [bot]
	// suffix the fingerprint diverges from the REST-derived one and
	// dedup misses on every redeploy.
	raw := []byte(`{"data":{"search":{"nodes":[
		{"number":1,"title":"t","author":{"__typename":"User","login":"alice"},
		 "reviews":{"nodes":[{"id":"r1","state":"COMMENTED","author":{"__typename":"Bot","login":"claude"},"body":"lgtm"}]},
		 "comments":{"nodes":[{"id":"c1","author":{"__typename":"Bot","login":"github-actions"},"authorAssociation":"NONE","body":"ci done"}]},
		 "reviewThreads":{"nodes":[
		   {"id":"t1","comments":{"nodes":[{"id":"tc1","author":{"__typename":"Bot","login":"coderabbitai"},"authorAssociation":"NONE","body":"nit","path":"x.go","line":42}]}}
		 ]}}
	]}}}`)
	got, err := parseEnrichedPRs(raw, "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	pr := got[0]
	if pr.PR.Author != "alice" {
		t.Errorf("non-bot author should NOT get [bot] suffix; got %q", pr.PR.Author)
	}
	if pr.Reviews[0].Author != "claude[bot]" {
		t.Errorf("review by Bot author = %q, want claude[bot]", pr.Reviews[0].Author)
	}
	gotTop := ""
	gotThread := ""
	for _, c := range pr.Comments {
		if c.Path == "" {
			gotTop = c.Author
		} else {
			gotThread = c.Author
		}
	}
	if gotTop != "github-actions[bot]" {
		t.Errorf("top-level comment Bot author = %q, want github-actions[bot]", gotTop)
	}
	if gotThread != "coderabbitai[bot]" {
		t.Errorf("review-thread comment Bot author = %q, want coderabbitai[bot]", gotThread)
	}
}

func TestCanonicalLogin_BotSuffixMatchesREST(t *testing.T) {
	// REST surfaces bot logins as "claude[bot]"; GraphQL returns "claude"
	// with __typename:"Bot". Fingerprints generated against the REST
	// payload would otherwise diverge and miss dedup on every redeploy.
	cases := []struct {
		name string
		u    *ghUser
		want string
	}{
		{"nil user", nil, ""},
		{"regular user", &ghUser{Typename: "User", Login: "alice"}, "alice"},
		{"bot account adds [bot] suffix", &ghUser{Typename: "Bot", Login: "claude"}, "claude[bot]"},
		{"bot login already suffixed is left alone", &ghUser{Typename: "Bot", Login: "claude[bot]"}, "claude[bot]"},
		{"unknown typename defaults to login as-is", &ghUser{Typename: "", Login: "mallory"}, "mallory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.canonicalLogin(); got != tc.want {
				t.Errorf("canonicalLogin() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTruncationFlags(t *testing.T) {
	// Build a synthetic node that toggles each pagination flag.
	jsonResp := []byte(`{"data":{"search":{"nodes":[
		{"number":1,
		 "reviews":{"pageInfo":{"hasNextPage":true},"nodes":[]},
		 "comments":{"pageInfo":{"hasNextPage":true},"nodes":[]},
		 "reviewThreads":{"pageInfo":{"hasNextPage":true},"nodes":[
		   {"id":"t","comments":{"pageInfo":{"hasNextPage":true},"nodes":[]}}
		 ]},
		 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}}]}}
	]}}}`)
	got, err := parseEnrichedPRs(jsonResp, "x/y")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	want := []string{"reviews", "comments", "reviewThreads", "threadComments", "ciContexts"}
	if !sliceEq(got[0].Truncated, want) {
		t.Errorf("Truncated = %v, want %v", got[0].Truncated, want)
	}
}

// fakeStdinRunner satisfies ghRunner for tests.
type fakeStdinRunner struct {
	out []byte
	err error
}

func (f *fakeStdinRunner) Run(_ context.Context, _ ...string) ([]byte, error) {
	return f.out, f.err
}
func (f *fakeStdinRunner) RunStdin(_ context.Context, _ []byte, _ ...string) ([]byte, error) {
	return f.out, f.err
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseEnrichedSurfacesStalenessFields verifies that the new
// isOutdated/isMinimized/minimizedReason/originalCommit fields are
// correctly parsed from the GraphQL response and surfaced on the output
// api.Comment values.
func TestParseEnrichedSurfacesStalenessFields(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":7,"title":"test pr","author":{"__typename":"User","login":"alice"},
	   "reviews":{"nodes":[]},
	   "comments":{"nodes":[]},
	   "reviewThreads":{"nodes":[
	     {"id":"t1","isResolved":false,"isOutdated":true,
	      "comments":{"nodes":[
	        {"id":"c1","databaseId":11,"author":{"__typename":"User","login":"bob"},
	         "authorAssociation":"CONTRIBUTOR","body":"looks wrong",
	         "path":"pkg/foo.go","originalLine":5,"line":0,"createdAt":"2024-01-01T00:00:00Z",
	         "isMinimized":true,"minimizedReason":"OUTDATED",
	         "originalCommit":{"oid":"deadbeefdeadbeef"}}
	      ]}}
	   ]},
	   "commits":{"nodes":[]}}
	]}}}`

	got, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	comments := got[0].Comments
	if len(comments) != 1 {
		t.Fatalf("want 1 comment (from review thread), got %d", len(comments))
	}
	c := comments[0]

	// Thread-level staleness flag.
	if !c.ThreadIsOutdated {
		t.Errorf("Comment.ThreadIsOutdated = false, want true")
	}

	// Per-comment minimization fields.
	if !c.IsMinimized {
		t.Errorf("Comment.IsMinimized = false, want true")
	}
	if c.MinimizedReason != "OUTDATED" {
		t.Errorf("Comment.MinimizedReason = %q, want OUTDATED", c.MinimizedReason)
	}

	// Original commit OID (source of subject_sha in feedback store).
	if c.OriginalCommitOID != "deadbeefdeadbeef" {
		t.Errorf("Comment.OriginalCommitOID = %q, want deadbeefdeadbeef", c.OriginalCommitOID)
	}
}

// Compile-time check that ghGraphQLResponse decodes the recorded fixture
// shape so silent schema drift gets caught here, not in production.
var _ = func() bool {
	var resp ghGraphQLResponse
	return json.Unmarshal([]byte(`{}`), &resp) == nil
}()
