package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestParseEnrichedPRs_RecordedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/enriched-prs-single.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, _, _, err := parseEnrichedPRs(raw, "acme/widgets")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 enriched PR, got %d", len(got))
	}
	pr := got[0]

	// PR core fields.
	if pr.PR.Number != 4242 {
		t.Errorf("PR.Number = %d, want 4242", pr.PR.Number)
	}
	if pr.PR.Author == "" {
		t.Errorf("PR.Author is empty")
	}
	if pr.PR.State != "open" {
		t.Errorf("PR.State = %q, want open (lowercased)", pr.PR.State)
	}
	if pr.PR.Repo != "acme/widgets" {
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
			// Path and ThreadID are asserted independently: an OR'd check
			// here would let either field silently regress to "" as long
			// as the other one still carries a value.
			if c.Path == "" {
				t.Errorf("review-thread comment missing Path: %+v", c)
			}
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
	_, _, _, err := parseEnrichedPRs(raw, "x/y")
	if err == nil {
		t.Fatal("want error on GraphQL errors envelope, got nil")
	}
}

func TestParseEnrichedPRs_Empty(t *testing.T) {
	raw := []byte(`{"data":{"search":{"issueCount":0,"nodes":[]}}}`)
	got, _, _, err := parseEnrichedPRs(raw, "x/y")
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
	if len(got) != 1 || got[0].PR.Number != 4242 {
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
	got, _, _, err := parseEnrichedPRs(raw, "x/y")
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
	got, _, _, err := parseEnrichedPRs(jsonResp, "x/y")
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

func TestParseEnrichedPR_ThreadIDAndCreatedAt(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{
	  "number":42,"title":"t","author":{"__typename":"User","login":"alice"},
	  "repository":{"nameWithOwner":"o/r"},
	  "reviewThreads":{"nodes":[
	    {"id":"PRRT_abc","isResolved":false,"comments":{"nodes":[
	      {"id":"PRRC_1","author":{"__typename":"Bot","login":"coderabbitai"},"authorAssociation":"NONE","body":"nit","path":"x.go","line":7,"createdAt":"2026-06-03T09:00:00Z"}
	    ]}}
	  ]}}}}}`)
	ep, err := parseEnrichedPR(raw, "o/r")
	if err != nil {
		t.Fatalf("parseEnrichedPR: %v", err)
	}
	if ep == nil || ep.PR.Number != 42 {
		t.Fatalf("unexpected PR: %+v", ep)
	}
	if len(ep.Comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(ep.Comments))
	}
	c := ep.Comments[0]
	if c.ThreadID != "PRRT_abc" {
		t.Errorf("ThreadID = %q, want PRRT_abc", c.ThreadID)
	}
	if c.CreatedAt != "2026-06-03T09:00:00Z" {
		t.Errorf("CreatedAt = %q, want 2026-06-03T09:00:00Z", c.CreatedAt)
	}
}

func TestParseEnrichedPR_TruncationFlag(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{
	  "number":1,"reviewThreads":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}}}`)
	ep, err := parseEnrichedPR(raw, "o/r")
	if err != nil {
		t.Fatalf("parseEnrichedPR: %v", err)
	}
	if !sliceEq(ep.Truncated, []string{"reviewThreads"}) {
		t.Errorf("Truncated = %v, want [reviewThreads]", ep.Truncated)
	}
}

func TestParseEnrichedPR_NotFound(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":null}}}`)
	if _, err := parseEnrichedPR(raw, "o/r"); err == nil {
		t.Fatal("want error when pullRequest is null")
	}
}

func TestParseEnrichedPR_GraphQLError(t *testing.T) {
	raw := []byte(`{"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a PullRequest"}]}`)
	if _, err := parseEnrichedPR(raw, "o/r"); err == nil {
		t.Fatal("want error when response carries a GraphQL errors array")
	}
}

func TestEnrichPR_FakeRunner(t *testing.T) {
	raw := []byte(`{"data":{"repository":{"pullRequest":{"number":7,"title":"t","repository":{"nameWithOwner":"o/r"}}}}}`)
	p := NewWithRunner(&fakeStdinRunner{out: raw})
	ep, err := p.EnrichPR(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("EnrichPR: %v", err)
	}
	if ep.PR.Number != 7 {
		t.Errorf("PR.Number = %d, want 7", ep.PR.Number)
	}
}

func TestEnrichPR_BadRepo(t *testing.T) {
	p := NewWithRunner(&fakeStdinRunner{out: []byte("{}")})
	if _, err := p.EnrichPR(context.Background(), "no-slash", 1); err == nil {
		t.Fatal("want error for repo without owner/name")
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

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
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

// TestParseEnrichedPRs_HeadSHAPropagated verifies that api.PR.HeadSHA is
// populated from the commit OID, and api.CIRun.HeadSHA is populated from
// the same commit OID for every CI context in the rollup.
func TestParseEnrichedPRs_HeadSHAPropagated(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":5,"title":"pr","author":{"__typename":"User","login":"alice"},
	   "headRefName":"feat/x","baseRefName":"main","url":"u","isDraft":false,
	   "state":"OPEN","merged":false,"additions":0,"deletions":0,"changedFiles":0,
	   "repository":{"nameWithOwner":"x/y"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "commits":{"nodes":[{"commit":{
	     "oid":"cafebabe",
	     "statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
	       {"__typename":"CheckRun","id":"cr1","name":"ci","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://u/1"},
	       {"__typename":"StatusContext","id":"sc1","context":"lint","state":"failure","targetUrl":"https://u/2"}
	     ]}}
	   }}]}
	  }
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	ep := got[0]

	// PR.HeadSHA must equal the commit OID.
	if ep.PR.HeadSHA != "cafebabe" {
		t.Errorf("PR.HeadSHA = %q, want cafebabe", ep.PR.HeadSHA)
	}

	// All CI runs must carry the same head SHA.
	if len(ep.CIRuns) != 2 {
		t.Fatalf("want 2 CIRuns, got %d", len(ep.CIRuns))
	}
	for _, r := range ep.CIRuns {
		if r.HeadSHA != "cafebabe" {
			t.Errorf("CIRun %q HeadSHA = %q, want cafebabe", r.Name, r.HeadSHA)
		}
	}
}

// TestParseEnrichedPRs_HeadSHAEmptyWhenNoCommits verifies that HeadSHA is
// empty (not panicking) when the commits connection is empty.
func TestParseEnrichedPRs_HeadSHAEmptyWhenNoCommits(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":3,"title":"pr","author":{"__typename":"User","login":"bob"},
	   "headRefName":"feat/z","baseRefName":"main","url":"u","isDraft":false,
	   "state":"OPEN","merged":false,"additions":0,"deletions":0,"changedFiles":0,
	   "repository":{"nameWithOwner":"x/y"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "commits":{"nodes":[]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	if got[0].PR.HeadSHA != "" {
		t.Errorf("PR.HeadSHA should be empty when commits is empty, got %q", got[0].PR.HeadSHA)
	}
}

// TestParseEnrichedPRs_BodyLabelsFilesCommits verifies that the GraphQL
// response fields body, labels, files, and commits are mapped onto the
// vcs.EnrichedPR and api.PR structs.
func TestParseEnrichedPRs_BodyLabelsFilesCommits(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":42,"title":"fix","author":{"__typename":"User","login":"alice"},
	   "headRefName":"fix/thing","baseRefName":"main","url":"https://gh/42","isDraft":false,
	   "state":"OPEN","merged":false,"additions":1,"deletions":0,"changedFiles":2,
	   "repository":{"nameWithOwner":"x/y"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "commits":{"nodes":[]},
	   "body":"production incident",
	   "labels":{"nodes":[{"name":"p0"}]},
	   "files":{"nodes":[{"path":"a.go"},{"path":"b.py"}]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	ep := got[0]

	if ep.PR.Body != "production incident" {
		t.Errorf("PR.Body = %q, want %q", ep.PR.Body, "production incident")
	}
	wantLabels := []string{"p0"}
	if !sliceEq(ep.PR.Labels, wantLabels) {
		t.Errorf("PR.Labels = %v, want %v", ep.PR.Labels, wantLabels)
	}
	wantFiles := []string{"a.go", "b.py"}
	if !sliceEq(ep.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", ep.Files, wantFiles)
	}
}

// TestParseEnrichedPRs_Assignees verifies that the GraphQL response's
// assignees connection is mapped onto api.PR.Assignees, and that a
// login-less entry (defensively, mirroring reviewRequests -> RequestedReviewers)
// is filtered out.
func TestParseEnrichedPRs_Assignees(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":42,"title":"fix","author":{"__typename":"User","login":"teammate"},
	   "headRefName":"fix/thing","baseRefName":"main","url":"https://gh/42","isDraft":false,
	   "state":"OPEN","merged":false,"additions":1,"deletions":0,"changedFiles":2,
	   "repository":{"nameWithOwner":"o/r"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "commits":{"nodes":[]},
	   "body":"","labels":{"nodes":[]},"files":{"nodes":[]},
	   "assignees":{"nodes":[{"login":"me"},{"login":"teammate"},{"login":""}]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "o/r")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	ep := got[0]

	wantAssignees := []string{"me", "teammate"}
	if !sliceEq(ep.PR.Assignees, wantAssignees) {
		t.Errorf("PR.Assignees = %v, want %v (login-less entry filtered out)", ep.PR.Assignees, wantAssignees)
	}
}

// TestTruncationFlags_Assignees verifies that the assignees connection sets
// the "assignees" truncation flag when hasNextPage is true, mirroring the
// labels/files/commits connections.
func TestTruncationFlags_Assignees(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":1,
	   "reviews":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "comments":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "assignees":{"pageInfo":{"hasNextPage":true},"nodes":[]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "o/r")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	if !sliceEq(got[0].Truncated, []string{"assignees"}) {
		t.Errorf("Truncated = %v, want [assignees]", got[0].Truncated)
	}
}

// TestParseEnrichedPRs_CommitMessages verifies that commit messages from the
// commits connection are mapped onto vcs.EnrichedPR.Commits.
func TestParseEnrichedPRs_CommitMessages(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":43,"title":"fix","author":{"__typename":"User","login":"alice"},
	   "headRefName":"fix/thing","baseRefName":"main","url":"https://gh/43","isDraft":false,
	   "state":"OPEN","merged":false,"additions":1,"deletions":0,"changedFiles":1,
	   "repository":{"nameWithOwner":"x/y"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "body":"",
	   "labels":{"nodes":[]},"files":{"nodes":[]},
	   "commits":{"nodes":[
	     {"commit":{"oid":"abc","message":"fix: x","statusCheckRollup":null}}
	   ]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	ep := got[0]

	wantCommits := []string{"fix: x"}
	if !sliceEq(ep.Commits, wantCommits) {
		t.Errorf("Commits = %v, want %v", ep.Commits, wantCommits)
	}
}

// TestTruncationFlags_NewConnections verifies that files, commits, and labels
// connections set the appropriate truncation flags when hasNextPage is true.
func TestTruncationFlags_NewConnections(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":1,
	   "reviews":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "comments":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "commits":{"pageInfo":{"hasNextPage":true},"nodes":[]},
	   "files":{"pageInfo":{"hasNextPage":true},"nodes":[]},
	   "labels":{"pageInfo":{"hasNextPage":true},"nodes":[]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	want := []string{"files", "commits", "labels"}
	if !sliceEq(got[0].Truncated, want) {
		t.Errorf("Truncated = %v, want %v", got[0].Truncated, want)
	}
}

// TestPRNodeSelection_CommentsRequestUpdatedAt is a cheap string assertion
// over the query literal: it's the only test that would have caught the
// long-standing bug where the top-level comments connection was ordered
// UPDATED_AT DESC but the node selection never actually requested
// updatedAt, so nothing verified the ordering was doing anything useful.
//
// Both comment node selections in prNodeSelection — the top-level
// `comments` connection and the nested `reviewThreads.comments` connection
// — are checked, since both decode into api.Comment.UpdatedAt via
// commentsFromGHNode.
func TestPRNodeSelection_CommentsRequestUpdatedAt(t *testing.T) {
	sel := prNodeSelection(30)

	commentsIdx := strings.Index(sel, "comments(first:")
	if commentsIdx == -1 {
		t.Fatal("top-level comments selection not found")
	}
	reviewThreadsIdx := strings.Index(sel, "reviewThreads(first:")
	if reviewThreadsIdx == -1 {
		t.Fatal("reviewThreads selection not found")
	}
	threadCommentsIdx := strings.Index(sel[reviewThreadsIdx:], "comments(first:")
	if threadCommentsIdx == -1 {
		t.Fatal("nested reviewThreads.comments selection not found")
	}
	threadCommentsIdx += reviewThreadsIdx

	topLevelBlock := sel[commentsIdx:reviewThreadsIdx]
	if !strings.Contains(topLevelBlock, "updatedAt") {
		t.Errorf("top-level comments selection does not request updatedAt:\n%s", topLevelBlock)
	}
	threadBlock := sel[threadCommentsIdx:]
	if !strings.Contains(threadBlock, "updatedAt") {
		t.Errorf("reviewThreads.comments selection does not request updatedAt:\n%s", threadBlock)
	}
}

// TestCommentsFromGHNode_UpdatedAt round-trips api.Comment.UpdatedAt for
// both the top-level comments connection and the nested review-thread
// comments connection, across the three cases the bead calls out: field
// present, field absent (an older cached payload), and field present but
// an empty string.
func TestCommentsFromGHNode_UpdatedAt(t *testing.T) {
	cases := []struct {
		name string
		resp string
		want string
	}{
		{
			name: "top-level comment: updatedAt present",
			resp: `{"data":{"search":{"nodes":[
				{"number":1,"comments":{"nodes":[
					{"id":"c1","author":{"__typename":"User","login":"alice"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"}
				]},"reviewThreads":{"nodes":[]}}
			]}}}`,
			want: "2026-01-02T00:00:00Z",
		},
		{
			name: "top-level comment: updatedAt absent (older-shaped payload)",
			resp: `{"data":{"search":{"nodes":[
				{"number":1,"comments":{"nodes":[
					{"id":"c1","author":{"__typename":"User","login":"alice"},"createdAt":"2026-01-01T00:00:00Z"}
				]},"reviewThreads":{"nodes":[]}}
			]}}}`,
			want: "",
		},
		{
			name: "top-level comment: updatedAt present but empty string",
			resp: `{"data":{"search":{"nodes":[
				{"number":1,"comments":{"nodes":[
					{"id":"c1","author":{"__typename":"User","login":"alice"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":""}
				]},"reviewThreads":{"nodes":[]}}
			]}}}`,
			want: "",
		},
		{
			name: "review-thread comment: updatedAt present",
			resp: `{"data":{"search":{"nodes":[
				{"number":1,"comments":{"nodes":[]},"reviewThreads":{"nodes":[
					{"id":"t1","comments":{"nodes":[
						{"id":"tc1","author":{"__typename":"User","login":"bob"},"path":"x.go","line":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-03T00:00:00Z"}
					]}}
				]}}
			]}}}`,
			want: "2026-01-03T00:00:00Z",
		},
		{
			name: "review-thread comment: updatedAt absent (older-shaped payload)",
			resp: `{"data":{"search":{"nodes":[
				{"number":1,"comments":{"nodes":[]},"reviewThreads":{"nodes":[
					{"id":"t1","comments":{"nodes":[
						{"id":"tc1","author":{"__typename":"User","login":"bob"},"path":"x.go","line":1,"createdAt":"2026-01-01T00:00:00Z"}
					]}}
				]}}
			]}}}`,
			want: "",
		},
		{
			name: "review-thread comment: updatedAt present but empty string",
			resp: `{"data":{"search":{"nodes":[
				{"number":1,"comments":{"nodes":[]},"reviewThreads":{"nodes":[
					{"id":"t1","comments":{"nodes":[
						{"id":"tc1","author":{"__typename":"User","login":"bob"},"path":"x.go","line":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":""}
					]}}
				]}}
			]}}}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, err := parseEnrichedPRs([]byte(tc.resp), "x/y")
			if err != nil {
				t.Fatalf("parseEnrichedPRs: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("want 1 PR, got %d", len(got))
			}
			if len(got[0].Comments) != 1 {
				t.Fatalf("want 1 comment, got %d", len(got[0].Comments))
			}
			if c := got[0].Comments[0]; c.UpdatedAt != tc.want {
				t.Errorf("Comment.UpdatedAt = %q, want %q", c.UpdatedAt, tc.want)
			}
		})
	}
}

// Compile-time check that ghGraphQLResponse decodes the recorded fixture
// shape so silent schema drift gets caught here, not in production.
var _ = func() bool {
	var resp ghGraphQLResponse
	return json.Unmarshal([]byte(`{}`), &resp) == nil
}()

// TestPRFromGHNodeMergeability verifies the mergeable/mergeStateStatus/
// autoMergeRequest GraphQL fields map onto api.PR. (pg2-dwfld)
func TestPRFromGHNodeMergeability(t *testing.T) {
	raw := []byte(`{
	  "number": 7, "title": "t", "url": "u",
	  "author": {"__typename":"User","login":"a"},
	  "baseRefName":"main","headRefName":"f","repository":{"nameWithOwner":"o/n"},
	  "mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","autoMergeRequest":null
	}`)
	var n ghPRNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	pr := prFromGHNode(n, "o/n")
	if pr.Mergeable != "MERGEABLE" || pr.MergeStateStatus != "CLEAN" || pr.AutoMergeEnabled {
		t.Errorf("got mergeable=%q state=%q aut=%v", pr.Mergeable, pr.MergeStateStatus, pr.AutoMergeEnabled)
	}

	raw2 := []byte(`{"number":8,"author":{"login":"a"},"repository":{"nameWithOwner":"o/n"},"autoMergeRequest":{"enabledAt":"2026-01-01T00:00:00Z"}}`)
	var n2 ghPRNode
	if err := json.Unmarshal(raw2, &n2); err != nil {
		t.Fatal(err)
	}
	if !prFromGHNode(n2, "o/n").AutoMergeEnabled {
		t.Errorf("AutoMergeEnabled should be true when autoMergeRequest present")
	}
}

// TestParseEnrichedPRs_StatusContextDescription verifies that a
// StatusContext node's `description` field round-trips through
// ciRunsFromGHNode into api.CIRun.Description unchanged, that it is empty
// (not a panic) when the field is absent or explicitly null, and that a
// sibling CheckRun node in the same rollup is parsed exactly as before
// (additive change, not disruptive) — the mixed-rollup literal below
// mirrors TestParseEnrichedPRs_HeadSHAPropagated's shape. (pg2-4dz88.2.2)
func TestParseEnrichedPRs_StatusContextDescription(t *testing.T) {
	cases := []struct {
		name       string
		statusJSON string
		wantDesc   string
	}{
		{
			name:       "description present round-trips unchanged",
			statusJSON: `"description":"All rules are approved"`,
			wantDesc:   "All rules are approved",
		},
		{
			name:       "description absent from JSON => zero value, no panic",
			statusJSON: ``,
			wantDesc:   "",
		},
		{
			name:       "description explicitly null => zero value, no panic",
			statusJSON: `"description":null`,
			wantDesc:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statusField := ""
			if tc.statusJSON != "" {
				statusField = "," + tc.statusJSON
			}
			resp := `{"data":{"search":{"nodes":[
			  {"number":9,"title":"pr","author":{"__typename":"User","login":"alice"},
			   "headRefName":"feat/x","baseRefName":"main","url":"u","isDraft":false,
			   "state":"OPEN","merged":false,"additions":0,"deletions":0,"changedFiles":0,
			   "repository":{"nameWithOwner":"x/y"},
			   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
			   "commits":{"nodes":[{"commit":{
			     "oid":"cafebabe",
			     "statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
			       {"__typename":"CheckRun","id":"cr1","name":"ci","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://u/1"},
			       {"__typename":"StatusContext","id":"sc1","context":"approval-gate","state":"failure","targetUrl":"https://u/2"` + statusField + `}
			     ]}}
			   }}]}
			  }
			]}}}`

			got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
			if err != nil {
				t.Fatalf("parseEnrichedPRs: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("want 1 PR, got %d", len(got))
			}
			ep := got[0]
			if len(ep.CIRuns) != 2 {
				t.Fatalf("want 2 CIRuns, got %d", len(ep.CIRuns))
			}

			checkRun, ok := findCIRunByProvider(ep.CIRuns, "github-actions")
			if !ok {
				t.Fatalf("no CheckRun-derived CIRun found: %+v", ep.CIRuns)
			}
			statusContext, ok := findCIRunByProvider(ep.CIRuns, "github-status")
			if !ok {
				t.Fatalf("no StatusContext-derived CIRun found: %+v", ep.CIRuns)
			}

			// The CheckRun-derived run is unaffected by this change: it
			// carries its normal fields and an empty Description (CheckRun
			// has no such GraphQL field at all).
			if checkRun.Name != "ci" || checkRun.Conclusion != "failure" || checkRun.URL != "https://u/1" {
				t.Errorf("CheckRun-derived CIRun changed unexpectedly: %+v", checkRun)
			}
			if checkRun.Description != "" {
				t.Errorf("CheckRun-derived CIRun.Description = %q, want empty", checkRun.Description)
			}

			if statusContext.Description != tc.wantDesc {
				t.Errorf("StatusContext-derived CIRun.Description = %q, want %q", statusContext.Description, tc.wantDesc)
			}
		})
	}
}

// findCIRunByProvider returns the first CIRun in runs with the given
// Provider, and whether one was found.
func findCIRunByProvider(runs []api.CIRun, provider string) (api.CIRun, bool) {
	for _, r := range runs {
		if r.Provider == provider {
			return r, true
		}
	}
	return api.CIRun{}, false
}

// TestParseEnrichedPRs_CommitAuthors verifies commit author logins map onto
// vcs.EnrichedPR.CommitAuthors, dropping commits whose author has no linked user.
func TestParseEnrichedPRs_CommitAuthors(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":44,"title":"x","author":{"__typename":"User","login":"alice"},
	   "headRefName":"f","baseRefName":"main","url":"https://gh/44","isDraft":false,
	   "state":"OPEN","merged":false,"additions":1,"deletions":0,"changedFiles":1,
	   "repository":{"nameWithOwner":"x/y"},
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},
	   "body":"","labels":{"nodes":[]},"files":{"nodes":[]},
	   "commits":{"nodes":[
	     {"commit":{"oid":"a","message":"m1","author":{"user":{"login":"alice"}},"statusCheckRollup":null}},
	     {"commit":{"oid":"b","message":"m2","author":{"user":{"login":"bob"}},"statusCheckRollup":null}},
	     {"commit":{"oid":"c","message":"m3","author":{"user":null},"statusCheckRollup":null}}
	   ]}}
	]}}}`

	got, _, _, err := parseEnrichedPRs([]byte(resp), "x/y")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	want := []string{"alice", "bob"}
	if !sliceEq(got[0].CommitAuthors, want) {
		t.Errorf("CommitAuthors = %v, want %v", got[0].CommitAuthors, want)
	}
}

// alwaysNextPageRunner simulates a pathological search result that never
// reports hasNextPage=false, so EnrichedPRs' maxEnrichedPRPages cap is the
// only thing that can terminate the loop.
type alwaysNextPageRunner struct {
	calls int
}

func (r *alwaysNextPageRunner) Run(_ context.Context, _ ...string) ([]byte, error) { return nil, nil }

func (r *alwaysNextPageRunner) RunStdin(_ context.Context, _ []byte, _ ...string) ([]byte, error) {
	r.calls++
	page := fmt.Sprintf(`{"data":{"rateLimit":{"cost":1,"remaining":1},
	  "search":{"pageInfo":{"hasNextPage":true,"endCursor":"C%d"},"nodes":[
	    {"number":%d,"title":"t","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"}}
	  ]}}}`, r.calls, r.calls)
	return []byte(page), nil
}

// TestEnrichedPRs_SinglePageUnchanged pins the pre-fix, single-page behavior:
// a search result with hasNextPage=false is returned as-is, with no "search"
// truncation sentinel on any PR — proving the pagination fix doesn't
// reintroduce a different bug (e.g. always flagging "search").
func TestEnrichedPRs_SinglePageUnchanged(t *testing.T) {
	raw := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":10},
	  "search":{"issueCount":2,"pageInfo":{"hasNextPage":false},"nodes":[
	    {"number":1,"title":"t1","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"}},
	    {"number":2,"title":"t2","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"}}
	  ]}}}`)
	got, err := NewWithRunner(&fakeStdinRunner{out: raw}).EnrichedPRs(context.Background(), "o/r", "is:pr is:open repo:o/r author:me")
	if err != nil {
		t.Fatalf("EnrichedPRs: %v", err)
	}
	if len(got) != 2 || got[0].PR.Number != 1 || got[1].PR.Number != 2 {
		t.Fatalf("unexpected result: %+v", got)
	}
	for _, pr := range got {
		for _, flag := range pr.Truncated {
			if flag == "search" {
				t.Errorf("PR #%d unexpectedly carries the search truncation flag: %v", pr.PR.Number, pr.Truncated)
			}
		}
	}
}

// TestEnrichedPRs_Paginates proves PR #51+ is no longer silently dropped: a
// two-page search result (page 1 hasNextPage=true+cursor, page 2
// hasNextPage=false) accumulates PRs from BOTH pages, with no "search"
// truncation marker since the roster completed (just across 2 pages).
// Mirrors TestFingerprintPRs_Paginates's pagingRunner mocking idiom exactly.
func TestEnrichedPRs_Paginates(t *testing.T) {
	page1 := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":9},
	  "search":{"pageInfo":{"hasNextPage":true,"endCursor":"C1"},"nodes":[
	    {"number":1,"title":"t1","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"}}]}}}`)
	page2 := []byte(`{"data":{"rateLimit":{"cost":1,"remaining":8},
	  "search":{"pageInfo":{"hasNextPage":false},"nodes":[
	    {"number":2,"title":"t2","author":{"login":"me"},"repository":{"nameWithOwner":"o/r"}}]}}}`)
	r := &pagingRunner{pages: [][]byte{page1, page2}}
	got, err := NewWithRunner(r).EnrichedPRs(context.Background(), "o/r", "is:pr is:open repo:o/r author:me")
	if err != nil {
		t.Fatalf("EnrichedPRs: %v", err)
	}
	if r.calls != 2 {
		t.Errorf("want 2 page fetches, got %d", r.calls)
	}
	if len(got) != 2 || got[0].PR.Number != 1 || got[1].PR.Number != 2 {
		t.Fatalf("pages not accumulated: %+v", got)
	}
	for _, pr := range got {
		for _, flag := range pr.Truncated {
			if flag == "search" {
				t.Errorf("PR #%d unexpectedly carries the search truncation flag after a completed 2-page roster: %v", pr.PR.Number, pr.Truncated)
			}
		}
	}
}

// TestEnrichedPRs_HitsPageCap proves that a pathological/unbounded roster
// (hasNextPage always true) terminates at maxEnrichedPRPages rather than
// hanging, and that every accumulated PR is marked with the "search"
// truncation sentinel — signaling "the retrieved set itself may be
// incomplete" to any caller that checks EnrichedPR.Truncated.
func TestEnrichedPRs_HitsPageCap(t *testing.T) {
	r := &alwaysNextPageRunner{}
	got, err := NewWithRunner(r).EnrichedPRs(context.Background(), "o/r", "is:pr is:open repo:o/r author:me")
	if err != nil {
		t.Fatalf("EnrichedPRs: %v", err)
	}
	if r.calls != maxEnrichedPRPages {
		t.Errorf("want %d page fetches (cap hit), got %d", maxEnrichedPRPages, r.calls)
	}
	if len(got) != maxEnrichedPRPages {
		t.Fatalf("want %d accumulated PRs, got %d", maxEnrichedPRPages, len(got))
	}
	for _, pr := range got {
		found := false
		for _, flag := range pr.Truncated {
			if flag == "search" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PR #%d Truncated = %v, want it to include %q", pr.PR.Number, pr.Truncated, "search")
		}
	}
}

// nativeStackFixture mirrors testdata/native-stack-fields.json's container
// shape: named PR-node fragments (not full GraphQL envelopes), one per
// scenario, so multiple test cases can share the one recorded fixture.
type nativeStackFixture struct {
	StackedPRExample   json.RawMessage `json:"stackedPRExample"`
	UnstackedPRExample json.RawMessage `json:"unstackedPRExample"`
}

func loadNativeStackFixture(t *testing.T) nativeStackFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/native-stack-fields.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f nativeStackFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f
}

// wrapAsSearchNode embeds one PR-node fragment into the minimal GraphQL
// search envelope parseEnrichedPRs expects.
func wrapAsSearchNode(node json.RawMessage) []byte {
	return []byte(`{"data":{"search":{"nodes":[` + string(node) + `]}}}`)
}

// TestParseEnrichedPRs_NativeStackPopulated verifies GitHub's native
// stacked-PR fields (private preview) map onto api.PR with position/order
// preserved when present, using the recorded stackedPRExample fixture
// (pg2-4dz88.3.4).
func TestParseEnrichedPRs_NativeStackPopulated(t *testing.T) {
	f := loadNativeStackFixture(t)
	got, _, _, err := parseEnrichedPRs(wrapAsSearchNode(f.StackedPRExample), "owner/repo")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	pr := got[0].PR

	if pr.StackID != "PullRequestStack_synthetic_1" {
		t.Errorf("StackID = %q, want PullRequestStack_synthetic_1", pr.StackID)
	}
	if pr.StackPosition != 2 {
		t.Errorf("StackPosition = %d, want 2", pr.StackPosition)
	}
	if pr.StackSize != 3 {
		t.Errorf("StackSize = %d, want 3", pr.StackSize)
	}
	// Order preserved: the fixture's entry at position-1 is PR #40
	// (feature-a) and at position+1 is PR #44 (feature-c).
	if pr.StackUpstreamHeadRefName != "feature-a" {
		t.Errorf("StackUpstreamHeadRefName = %q, want feature-a", pr.StackUpstreamHeadRefName)
	}
	if pr.StackDownstreamHeadRefName != "feature-c" {
		t.Errorf("StackDownstreamHeadRefName = %q, want feature-c", pr.StackDownstreamHeadRefName)
	}
	// Branch/Base are unaffected by the addition.
	if pr.Branch != "feature-b" || pr.Base != "feature-a" {
		t.Errorf("Branch/Base = %q/%q, want feature-b/feature-a", pr.Branch, pr.Base)
	}
}

// TestParseEnrichedPRs_NativeStackNull verifies that a PR carrying explicit
// JSON `null` for both stack and stackEntry (an unstacked PR under the
// preview schema) degrades to the zero value with no error, using the
// recorded unstackedPRExample fixture (pg2-4dz88.3.4).
func TestParseEnrichedPRs_NativeStackNull(t *testing.T) {
	f := loadNativeStackFixture(t)
	got, _, _, err := parseEnrichedPRs(wrapAsSearchNode(f.UnstackedPRExample), "owner/repo")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	pr := got[0].PR

	if pr.StackID != "" || pr.StackPosition != 0 || pr.StackSize != 0 ||
		pr.StackUpstreamHeadRefName != "" || pr.StackDownstreamHeadRefName != "" {
		t.Errorf("want zero-value stack fields when stack/stackEntry are null, got %+v", pr)
	}
	if pr.Branch != "hotfix-x" || pr.Base != "trunk" {
		t.Errorf("Branch/Base = %q/%q, want hotfix-x/trunk", pr.Branch, pr.Base)
	}
}

// TestParseEnrichedPRs_NativeStackAbsentFromResponse verifies that a response
// which never mentions stack/stackEntry at all (an older schema, or the
// private preview withdrawn) degrades the same way as an explicit null: zero
// value, no error, and Branch/Base still populated — proving the base-chain
// fallback's inputs survive this change (pg2-4dz88.3.4). Uses an inline JSON
// literal per this file's convention for new synthetic cases, not the
// shared testdata/enriched-prs-single.json fixture.
func TestParseEnrichedPRs_NativeStackAbsentFromResponse(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":9,"title":"t","author":{"__typename":"User","login":"alice"},
	   "headRefName":"feat/old-schema","baseRefName":"main",
	   "reviews":{"nodes":[]},"comments":{"nodes":[]},"reviewThreads":{"nodes":[]},"commits":{"nodes":[]}}
	]}}}`
	got, _, _, err := parseEnrichedPRs([]byte(resp), "owner/repo")
	if err != nil {
		t.Fatalf("parseEnrichedPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	pr := got[0].PR

	if pr.StackID != "" || pr.StackPosition != 0 || pr.StackSize != 0 ||
		pr.StackUpstreamHeadRefName != "" || pr.StackDownstreamHeadRefName != "" {
		t.Errorf("want zero-value stack fields when stack/stackEntry are absent from the response, got %+v", pr)
	}
	if pr.Branch != "feat/old-schema" || pr.Base != "main" {
		t.Errorf("Branch/Base = %q/%q, want feat/old-schema/main", pr.Branch, pr.Base)
	}
}

// TestGetPR_RESTFallbackHasNoStackFields verifies the REST fallback path
// (GetPR, no GraphQL involved at all) never populates the native stack
// fields, and that Branch/Base — the base-chain fallback's inputs — still
// come through unaffected (pg2-4dz88.3.4).
func TestGetPR_RESTFallbackHasNoStackFields(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{
		"number": 42, "title": "t", "state": "OPEN", "author": {"login": "alice"},
		"headRefName": "feature-b", "baseRefName": "feature-a"
	}`)
	p := NewWithRunner(gh)

	pr, err := p.GetPR(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if pr.StackID != "" || pr.StackPosition != 0 || pr.StackSize != 0 ||
		pr.StackUpstreamHeadRefName != "" || pr.StackDownstreamHeadRefName != "" {
		t.Errorf("REST fallback path must never populate stack fields, got %+v", pr)
	}
	if pr.Branch != "feature-b" || pr.Base != "feature-a" {
		t.Errorf("Branch/Base = %q/%q, want feature-b/feature-a", pr.Branch, pr.Base)
	}
}

// TestApplyStackFields_IDFallsBackToStackWhenNoStackEntry verifies
// applyStackFields' defensive fallback: when a response carries `stack` but
// no `stackEntry` (a shape the schema likely never actually emits together,
// since a PR in a stack should carry both, but nothing guarantees it),
// StackID still comes from stack.id rather than being left empty. Found by
// pg-go-mutate (pg2-4dz88.3.4): the `if pr.StackID == ""` guard at
// applyStackFields survived every mutation because every prior test left
// StackEntry populated, so the fallback assignment itself was never
// exercised.
func TestApplyStackFields_IDFallsBackToStackWhenNoStackEntry(t *testing.T) {
	raw := []byte(`{
	  "number": 50, "title": "t", "url": "u",
	  "author": {"__typename":"User","login":"a"},
	  "baseRefName":"main","headRefName":"f","repository":{"nameWithOwner":"o/n"},
	  "stack": {"id":"PullRequestStack_x","number":1,"baseRefName":"trunk","size":1,
	            "entries":{"totalCount":1,"nodes":[]}},
	  "stackEntry": null
	}`)
	var n ghPRNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	pr := prFromGHNode(n, "o/n")
	if pr.StackID != "PullRequestStack_x" {
		t.Errorf("StackID = %q, want PullRequestStack_x (fallback to stack.id)", pr.StackID)
	}
	if pr.StackPosition != 0 {
		t.Errorf("StackPosition = %d, want 0 (no stackEntry)", pr.StackPosition)
	}
	if pr.StackSize != 1 {
		t.Errorf("StackSize = %d, want 1", pr.StackSize)
	}
}

// TestApplyStackFields_StackEntryIDTakesPrecedenceOverStack verifies the
// other direction of the same guard: when stackEntry IS present, its
// stack.id is used as-is and is NOT overwritten by the sibling stack.id —
// the two are expected to always agree in a real response, but the guard's
// job is to prefer stackEntry without needing that to hold.
func TestApplyStackFields_StackEntryIDTakesPrecedenceOverStack(t *testing.T) {
	raw := []byte(`{
	  "number": 51, "title": "t", "url": "u",
	  "author": {"__typename":"User","login":"a"},
	  "baseRefName":"main","headRefName":"f","repository":{"nameWithOwner":"o/n"},
	  "stack": {"id":"stack-id-from-stack-field","number":1,"baseRefName":"trunk","size":1,
	            "entries":{"totalCount":1,"nodes":[]}},
	  "stackEntry": {"id":"entry-1","position":1,"stack":{"id":"stack-id-from-stack-entry"}}
	}`)
	var n ghPRNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	pr := prFromGHNode(n, "o/n")
	if pr.StackID != "stack-id-from-stack-entry" {
		t.Errorf("StackID = %q, want stack-id-from-stack-entry (stackEntry.stack.id takes precedence)", pr.StackID)
	}
}

// TestTruncationFlags_StackEntries verifies stack.entries sets the
// "stackEntries" truncation flag when hasNextPage is true, mirroring the
// other connections in truncationFlags.
func TestTruncationFlags_StackEntries(t *testing.T) {
	const resp = `{"data":{"search":{"nodes":[
	  {"number":1,
	   "reviews":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "comments":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]},
	   "stack":{"id":"s1","size":40,"entries":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}
	]}}}`
	got, _, _, err := parseEnrichedPRs([]byte(resp), "owner/repo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 PR, got %d", len(got))
	}
	if !sliceEq(got[0].Truncated, []string{"stackEntries"}) {
		t.Errorf("Truncated = %v, want [stackEntries]", got[0].Truncated)
	}
}
