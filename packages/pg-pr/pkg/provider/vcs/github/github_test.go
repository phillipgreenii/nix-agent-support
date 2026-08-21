package github

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// fakeGH replays canned responses keyed by the first two arguments
// ("pr list", "pr view"). Each call records args for assertion.
type fakeGH struct {
	responses map[string][]byte
	errs      map[string]error
	calls     [][]string
	stdins    [][]byte // captured RunStdin payloads (for asserting POST bodies)
}

func newFakeGH() *fakeGH {
	return &fakeGH{
		responses: map[string][]byte{},
		errs:      map[string]error{},
	}
}

func (f *fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := strings.Join(args[:min(2, len(args))], " ")
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if resp, ok := f.responses[key]; ok {
		return resp, nil
	}
	return []byte("[]"), nil
}

func (f *fakeGH) RunStdin(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	f.stdins = append(f.stdins, append([]byte(nil), stdin...))
	key := strings.Join(args[:min(2, len(args))], " ")
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if resp, ok := f.responses[key]; ok {
		return resp, nil
	}
	return []byte("{}"), nil
}

const samplePRList = `[
  {
    "number": 42,
    "title": "feat: do x",
    "headRefName": "feat/x",
    "baseRefName": "main",
    "url": "https://github.com/foo/bar/pull/42",
    "author": {"login": "phillipg", "name": "Phillip"},
    "isDraft": false,
    "state": "OPEN",
    "mergedAt": "",
    "closedAt": ""
  },
  {
    "number": 43,
    "title": "feat: do y",
    "headRefName": "feat/y",
    "baseRefName": "main",
    "url": "https://github.com/foo/bar/pull/43",
    "author": {"login": "phillipg", "name": "Phillip"},
    "isDraft": true,
    "state": "OPEN",
    "mergedAt": "",
    "closedAt": ""
  }
]`

const samplePRView = `{
  "number": 42,
  "title": "feat: do x",
  "headRefName": "feat/x",
  "baseRefName": "main",
  "url": "https://github.com/foo/bar/pull/42",
  "author": {"login": "phillipg", "name": "Phillip"},
  "isDraft": false,
  "state": "MERGED",
  "mergedAt": "2026-05-19T12:00:00Z",
  "closedAt": "2026-05-19T12:00:00Z"
}`

func TestListMyPRs_ParsesAndConverts(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr list"] = []byte(samplePRList)
	p := NewWithRunner(gh)

	prs, err := p.ListMyPRs(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("ListMyPRs: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %d", len(prs))
	}
	if prs[0].Repo != "foo/bar" || prs[0].Number != 42 || prs[0].Author != "phillipg" {
		t.Fatalf("PR[0]: %+v", prs[0])
	}
	if prs[0].State != "open" {
		t.Fatalf("state should be lowercased: %q", prs[0].State)
	}
	if !prs[1].Draft {
		t.Fatalf("expected PR[1] draft=true")
	}

	// Verify gh args.
	last := gh.calls[len(gh.calls)-1]
	want := []string{"pr", "list", "--repo", "foo/bar", "--state", "open", "--author", "@me"}
	for i, w := range want {
		if last[i] != w {
			t.Fatalf("gh args[%d]: got %q want %q (full: %v)", i, last[i], w, last)
		}
	}
}

func TestListTeamPRs_MergesByNumber(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr list"] = []byte(samplePRList)
	p := NewWithRunner(gh)

	prs, err := p.ListTeamPRs(context.Background(), "foo/bar", []string{"phillipg", "alice", ""})
	if err != nil {
		t.Fatalf("ListTeamPRs: %v", err)
	}
	// Both calls return the same PRs; dedup keeps two entries.
	if len(prs) != 2 {
		t.Fatalf("expected 2 deduped PRs, got %d", len(prs))
	}
	// Two non-empty members → two gh invocations.
	authorArgs := 0
	for _, c := range gh.calls {
		for i, a := range c {
			if a == "--author" && i+1 < len(c) && c[i+1] != "" {
				authorArgs++
			}
		}
	}
	if authorArgs != 2 {
		t.Fatalf("expected 2 --author invocations, got %d (calls: %v)", authorArgs, gh.calls)
	}
}

func TestGetPR_ParsesView(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(samplePRView)
	p := NewWithRunner(gh)

	pr, err := p.GetPR(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if pr.Number != 42 {
		t.Fatalf("number: %d", pr.Number)
	}
	if pr.State != "merged" {
		t.Fatalf("state: %q", pr.State)
	}
	if !pr.Merged {
		t.Fatalf("expected Merged=true when mergedAt is set")
	}
	if pr.MergedAt != "2026-05-19T12:00:00Z" {
		t.Fatalf("MergedAt: got %q want %q (pg2-ew4kf: the snapshot layer's retention window is measured from this)", pr.MergedAt, "2026-05-19T12:00:00Z")
	}
}

func TestGetPR_ParsesReviewRequests(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{
		"number": 7, "title": "t", "state": "OPEN", "author": {"login": "zara"},
		"reviewRequests": [
			{"__typename": "User", "login": "phillipg"},
			{"__typename": "Team", "name": "findev", "slug": "findev"}
		]
	}`)
	p := NewWithRunner(gh)

	pr, err := p.GetPR(context.Background(), "foo/bar", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	// Users become RequestedReviewers; teams (no login) are excluded.
	if len(pr.RequestedReviewers) != 1 || pr.RequestedReviewers[0] != "phillipg" {
		t.Fatalf("RequestedReviewers = %v, want [phillipg] (team excluded)", pr.RequestedReviewers)
	}
	// The --json field set must request reviewRequests, or gh returns nothing.
	if len(gh.calls) == 0 || !strings.Contains(strings.Join(gh.calls[0], " "), "reviewRequests") {
		t.Errorf("gh pr view must request the reviewRequests field; args=%v", gh.calls)
	}
}

func TestGetPR_ValidatesInput(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if _, err := p.GetPR(context.Background(), "", 1); err == nil {
		t.Fatalf("expected error for empty repo")
	}
	if _, err := p.GetPR(context.Background(), "no-slash", 1); err == nil {
		t.Fatalf("expected error for non-owner/name repo")
	}
	if _, err := p.GetPR(context.Background(), "a/b", 0); err == nil {
		t.Fatalf("expected error for PR number=0")
	}
}

func TestListMyPRs_PropagatesGHError(t *testing.T) {
	gh := newFakeGH()
	gh.errs["pr list"] = errors.New("boom: auth required")
	p := NewWithRunner(gh)

	_, err := p.ListMyPRs(context.Background(), "foo/bar")
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should wrap underlying: got %q", err.Error())
	}
}

func TestListMyPRs_EmptyArrayIsZeroPRs(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)

	prs, err := p.ListMyPRs(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("ListMyPRs: %v", err)
	}
	if len(prs) != 0 {
		t.Fatalf("expected 0 PRs, got %d", len(prs))
	}
}

func TestErrStubSentinelStillCompiles(t *testing.T) {
	// errStub is kept as a sentinel after Phase 3. Just verify the value
	// is non-nil so accidental removal flags here.
	if errStub == nil {
		t.Fatalf("errStub sentinel must remain non-nil")
	}
}

// ----------------------------------------------------------------------
// body + labels on the REST list path
// ----------------------------------------------------------------------

const samplePRListWithBodyAndLabels = `[
  {
    "number": 99,
    "title": "feat: body and labels",
    "headRefName": "feat/body-labels",
    "headRefOid": "abc123",
    "baseRefName": "main",
    "url": "https://github.com/foo/bar/pull/99",
    "author": {"login": "phillipg", "name": "Phillip"},
    "isDraft": false,
    "state": "OPEN",
    "mergedAt": "",
    "closedAt": "",
    "additions": 5,
    "deletions": 2,
    "changedFiles": 1,
    "body": "some text",
    "labels": [{"name": "p0"}, {"name": "bug"}]
  }
]`

func TestListMyPRs_BodyAndLabels(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr list"] = []byte(samplePRListWithBodyAndLabels)
	p := NewWithRunner(gh)

	prs, err := p.ListMyPRs(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("ListMyPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	pr := prs[0]
	if pr.Body != "some text" {
		t.Fatalf("Body: got %q, want %q", pr.Body, "some text")
	}
	want := []string{"p0", "bug"}
	if len(pr.Labels) != len(want) {
		t.Fatalf("Labels: got %v, want %v", pr.Labels, want)
	}
	for i, w := range want {
		if pr.Labels[i] != w {
			t.Fatalf("Labels[%d]: got %q, want %q", i, pr.Labels[i], w)
		}
	}
}

// ----------------------------------------------------------------------
// ListComments tests
// ----------------------------------------------------------------------

const sampleIssueComments = `[
  {
    "node_id": "IC_kwDO_top1",
    "body": "Looks good overall.",
    "user": {"login": "alice"},
    "author_association": "MEMBER",
    "created_at": "2026-06-01T10:00:00Z"
  },
  {
    "node_id": "IC_kwDO_top2",
    "body": "Please add tests.",
    "user": {"login": "bob"},
    "author_association": "COLLABORATOR"
  }
]`

const sampleReviewComments = `[
  {
    "node_id": "RC_kwDO_a",
    "body": "use ctx",
    "user": {"login": "alice"},
    "path": "main.go",
    "line": 42,
    "original_line": 42,
    "author_association": "MEMBER",
    "created_at": "2026-06-02T11:00:00Z"
  },
  {
    "node_id": "RC_kwDO_b",
    "body": "nit: rename",
    "user": {"login": "alice"},
    "path": "main.go",
    "line": 0,
    "original_line": 17,
    "in_reply_to_id": 12345,
    "author_association": "MEMBER"
  }
]`

func TestListComments_CombinesTopLevelAndInline(t *testing.T) {
	gh := newFakeGH()
	// Keys are first two args: "api repos/...".
	gh.responses["api repos/foo/bar/issues/42/comments"] = []byte(sampleIssueComments)
	gh.responses["api repos/foo/bar/pulls/42/comments"] = []byte(sampleReviewComments)

	// Use a custom key extractor: since our fakeGH joins first 2 args,
	// adapt: we look at full path. Rebuild fakeGH to key on args[1] (the path).
	// Simpler: switch fakeGH to look up via exact path match below.
	p := NewWithRunner(&pathFakeGH{
		responses: map[string][]byte{
			"repos/foo/bar/issues/42/comments": []byte(sampleIssueComments),
			"repos/foo/bar/pulls/42/comments":  []byte(sampleReviewComments),
		},
	})

	cs, err := p.ListComments(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(cs) != 4 {
		t.Fatalf("expected 4 comments, got %d: %+v", len(cs), cs)
	}

	// First two are top-level (no path/line).
	if cs[0].Author != "alice" || cs[0].Path != "" || cs[0].Line != 0 {
		t.Fatalf("top-level[0]: %+v", cs[0])
	}
	if cs[0].AuthorRole != "member" {
		t.Fatalf("author_role lowercased: %q", cs[0].AuthorRole)
	}
	// Last two are inline with path/line/threadID.
	if cs[2].Path != "main.go" || cs[2].Line != 42 || cs[2].ThreadID == "" {
		t.Fatalf("inline[0]: %+v", cs[2])
	}
	// Line=0 falls back to original_line.
	if cs[3].Line != 17 {
		t.Fatalf("inline[1] should fall back to original_line=17, got %d", cs[3].Line)
	}

	// Silence unused linters on gh.
	_ = gh
}

func TestListComments_PopulatesCreatedAt(t *testing.T) {
	p := NewWithRunner(&pathFakeGH{
		responses: map[string][]byte{
			"repos/foo/bar/issues/42/comments": []byte(sampleIssueComments),
			"repos/foo/bar/pulls/42/comments":  []byte(sampleReviewComments),
		},
	})
	cs, err := p.ListComments(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	byID := map[string]string{}
	for _, c := range cs {
		byID[c.ID] = c.CreatedAt
	}
	if byID["IC_kwDO_top1"] != "2026-06-01T10:00:00Z" {
		t.Errorf("issue comment CreatedAt = %q, want 2026-06-01T10:00:00Z", byID["IC_kwDO_top1"])
	}
	if byID["RC_kwDO_a"] != "2026-06-02T11:00:00Z" {
		t.Errorf("review comment CreatedAt = %q, want 2026-06-02T11:00:00Z", byID["RC_kwDO_a"])
	}
}

func TestListComments_ValidatesInput(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if _, err := p.ListComments(context.Background(), "", 1); err == nil {
		t.Fatalf("expected error for empty repo")
	}
	if _, err := p.ListComments(context.Background(), "a/b", 0); err == nil {
		t.Fatalf("expected error for PR number=0")
	}
}

// ----------------------------------------------------------------------
// Write path tests (AddComment, PostReview)
// ----------------------------------------------------------------------

func TestAddComment_PostsToIssueEndpoint(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api repos/foo/bar/issues/42/comments"] = []byte(
		`{"node_id":"IC_kw","user":{"login":"phillipg"},"body":"hi"}`,
	)
	p := NewWithRunner(gh)

	c, err := p.AddComment(context.Background(), "foo/bar", 42, "hi")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c.ID != "IC_kw" || c.Author != "phillipg" {
		t.Fatalf("unexpected comment: %+v", c)
	}
}

func TestAddComment_EmptyBodyRejected(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if _, err := p.AddComment(context.Background(), "foo/bar", 42, "  "); err == nil {
		t.Fatalf("expected error for whitespace-only body")
	}
}

// decodeLastReviewPayload unmarshals the most recent captured RunStdin payload.
func decodeLastReviewPayload(t *testing.T, gh *fakeGH) map[string]any {
	t.Helper()
	if len(gh.stdins) == 0 {
		t.Fatalf("no RunStdin payload captured")
	}
	var payload map[string]any
	if err := json.Unmarshal(gh.stdins[len(gh.stdins)-1], &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v (%s)", err, gh.stdins[len(gh.stdins)-1])
	}
	return payload
}

// TestPostReview_PayloadShape is the pg2-pipw 422 fix, verified against the
// replayed POST payload: commit_id anchors inline comments to the reviewed
// commit; a line comment is sent with line+side; an un-anchorable (PR-level or
// whole-file) comment is FOLDED into the body (NOT sent as an invalid entry with
// subject_type, which GitHub rejected with 422).
func TestPostReview_PayloadShape(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api repos/foo/bar/pulls/42/reviews"] = []byte(
		`{"node_id":"RV_kw","state":"PENDING","body":"the body"}`,
	)
	p := NewWithRunner(gh)

	rev, err := p.PostReview(context.Background(), "foo/bar", 42, "deadbeef",
		"top-level body",
		[]api.Comment{
			{Path: "main.go", Line: 12, Body: "rename x"},
			{Path: "", Body: "PR-level note"},
			{Path: "readme.md", Body: "missing section"}, // whole-file (no line)
		})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if rev.ID != "RV_kw" || rev.State != "pending" {
		t.Fatalf("unexpected review: %+v", rev)
	}

	payload := decodeLastReviewPayload(t, gh)
	raw := string(gh.stdins[len(gh.stdins)-1])

	if payload["commit_id"] != "deadbeef" {
		t.Errorf("commit_id must anchor to the reviewed commit; got %v", payload["commit_id"])
	}
	if strings.Contains(raw, "subject_type") {
		t.Errorf("payload must NOT contain subject_type (422 cause): %s", raw)
	}
	// The un-anchorable comments must be folded into the body.
	body, _ := payload["body"].(string)
	if !strings.Contains(body, "PR-level note") || !strings.Contains(body, "missing section") {
		t.Errorf("PR-level + whole-file comments must fold into body; body=%q", body)
	}
	// Exactly one inline comment (main.go:12) survives as a comments[] entry.
	comments, _ := payload["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("expected exactly 1 inline comment, got %d: %s", len(comments), raw)
	}
	c0 := comments[0].(map[string]any)
	if c0["path"] != "main.go" || c0["line"].(float64) != 12 || c0["side"] != "RIGHT" {
		t.Errorf("inline comment shape wrong: %+v", c0)
	}
	// A single-line comment must carry NO span keys, so adding multi-line support
	// left this payload byte-identical (pg2-3c8mo).
	if strings.Contains(raw, "start_line") || strings.Contains(raw, "start_side") {
		t.Errorf("a single-line comment must not send start_line/start_side: %s", raw)
	}
}

// TestPostReview_MultiLineCommentSendsStartLine is pg2-3c8mo AC1/AC4: a finding
// that spans several lines posts as a GitHub MULTI-line review comment
// (start_line..line), rather than being truncated to one endpoint.
func TestPostReview_MultiLineCommentSendsStartLine(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api repos/foo/bar/pulls/42/reviews"] = []byte(`{"node_id":"RV_kw","state":"PENDING"}`)
	p := NewWithRunner(gh)

	if _, err := p.PostReview(context.Background(), "foo/bar", 42, "deadbeef", "",
		[]api.Comment{{Path: "main.go", StartLine: 10, Line: 12, Body: "this whole block leaks"}}); err != nil {
		t.Fatalf("PostReview: %v", err)
	}

	payload := decodeLastReviewPayload(t, gh)
	raw := string(gh.stdins[len(gh.stdins)-1])
	comments, _ := payload["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("expected exactly 1 inline comment, got %d: %s", len(comments), raw)
	}
	c0 := comments[0].(map[string]any)
	if c0["start_line"] == nil {
		t.Fatalf("a multi-line finding must post as a multi-line comment (start_line), got %+v", c0)
	}
	if c0["start_line"].(float64) != 10 || c0["line"].(float64) != 12 {
		t.Errorf("span = start_line %v/line %v, want 10/12", c0["start_line"], c0["line"])
	}
	if c0["side"] != "RIGHT" || c0["start_side"] != "RIGHT" {
		t.Errorf("both sides must be RIGHT for a new-file span: %+v", c0)
	}
	// The body must not have been folded into the review body instead.
	if body, _ := payload["body"].(string); strings.Contains(body, "leaks") {
		t.Errorf("a multi-line comment must post inline, not fold into the body: %q", body)
	}
}

// TestPostReview_DegenerateStartLineIsDropped: `review post` reads the staged
// FILE with a plain json.Unmarshal, so a hand-edited draft can carry a span
// GitHub would 422 (start_line not strictly before line). The provider drops the
// span and still posts the comment, instead of failing the whole review.
func TestPostReview_DegenerateStartLineIsDropped(t *testing.T) {
	for name, c := range map[string]api.Comment{
		"start_line equals line": {Path: "main.go", StartLine: 12, Line: 12, Body: "x"},
		"start_line after line":  {Path: "main.go", StartLine: 20, Line: 12, Body: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGH()
			gh.responses["api repos/foo/bar/pulls/42/reviews"] = []byte(`{"node_id":"RV_kw","state":"PENDING"}`)
			p := NewWithRunner(gh)
			if _, err := p.PostReview(context.Background(), "foo/bar", 42, "", "", []api.Comment{c}); err != nil {
				t.Fatalf("PostReview: %v", err)
			}
			raw := string(gh.stdins[len(gh.stdins)-1])
			if strings.Contains(raw, "start_line") {
				t.Errorf("a degenerate span must not reach the wire (422): %s", raw)
			}
			comments, _ := decodeLastReviewPayload(t, gh)["comments"].([]any)
			if len(comments) != 1 {
				t.Fatalf("the comment itself must still post, got %d: %s", len(comments), raw)
			}
		})
	}
}

// TestPostReview_EmptyReviewSkipsPost: a review with neither body nor
// anchorable comments must NOT POST (an empty pending review is a 422); it
// returns a no-op review so the caller clears the staged draft.
func TestPostReview_EmptyReviewSkipsPost(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	rev, err := p.PostReview(context.Background(), "foo/bar", 42, "deadbeef", "", nil)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if rev == nil || rev.State != "pending" {
		t.Errorf("expected a no-op pending review, got %+v", rev)
	}
	for _, c := range gh.calls {
		if len(c) >= 2 && c[0] == "api" && strings.Contains(c[1], "/reviews") {
			t.Errorf("must NOT POST an empty review (422); saw call %v", c)
		}
	}
}

// TestPostReview_NoCommitIDOmitsField: the CLI post path passes commitID="",
// which must omit commit_id (GitHub anchors to the latest commit).
func TestPostReview_NoCommitIDOmitsField(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api repos/foo/bar/pulls/42/reviews"] = []byte(`{"node_id":"RV_1","state":"PENDING"}`)
	p := NewWithRunner(gh)
	if _, err := p.PostReview(context.Background(), "foo/bar", 42, "", "body", nil); err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if _, present := decodeLastReviewPayload(t, gh)["commit_id"]; present {
		t.Errorf("commit_id must be omitted when commitID is empty")
	}
}

// ----------------------------------------------------------------------
// Phase 3 write-path tests
// ----------------------------------------------------------------------

func TestCreatePR_PostsTitleBodyHeadBaseAndDraft(t *testing.T) {
	gh := newFakeGH()
	// Stub stdout from `gh pr create` (URL line).
	gh.responses["pr create"] = []byte("https://github.com/foo/bar/pull/42\n")
	gh.responses["pr view"] = []byte(samplePRView)
	p := NewWithRunner(gh)

	pr, err := p.CreatePR(context.Background(), "foo/bar", true, "feat: x", "the body", "feat/x", "main", nil, nil)
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 42 {
		t.Fatalf("pr.Number: got %d want 42", pr.Number)
	}
	// Verify the underlying args.
	found := false
	for _, c := range gh.calls {
		if len(c) >= 2 && c[0] == "pr" && c[1] == "create" {
			found = true
			joined := strings.Join(c, " ")
			if !strings.Contains(joined, "--draft") {
				t.Fatalf("expected --draft flag: %v", c)
			}
			if !strings.Contains(joined, "--base main") {
				t.Fatalf("expected --base main: %v", c)
			}
			if !strings.Contains(joined, "--head feat/x") {
				t.Fatalf("expected --head feat/x: %v", c)
			}
			if !strings.Contains(joined, "--body-file -") {
				t.Fatalf("expected --body-file -: %v", c)
			}
		}
	}
	if !found {
		t.Fatalf("expected pr create call, got %v", gh.calls)
	}
}

func TestCreatePR_WithoutDraft(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr create"] = []byte("https://github.com/foo/bar/pull/7\n")
	gh.responses["pr view"] = []byte(samplePRView)
	p := NewWithRunner(gh)

	if _, err := p.CreatePR(context.Background(), "foo/bar", false, "t", "b", "h", "main", nil, nil); err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	for _, c := range gh.calls {
		if len(c) >= 2 && c[0] == "pr" && c[1] == "create" {
			joined := strings.Join(c, " ")
			if strings.Contains(joined, "--draft") {
				t.Fatalf("expected NO --draft flag when draft=false: %v", c)
			}
		}
	}
}

func TestCreatePR_PushesReviewersAndLabels(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr create"] = []byte("https://github.com/foo/bar/pull/55\n")
	gh.responses["pr view"] = []byte(samplePRView)
	p := NewWithRunner(gh)

	_, err := p.CreatePR(context.Background(), "foo/bar", true, "t", "b", "feat/x", "main",
		[]string{"alice", "bob"},
		[]string{"area/cli", " priority/p2 "})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	var createArgs []string
	for _, c := range gh.calls {
		if len(c) >= 2 && c[0] == "pr" && c[1] == "create" {
			createArgs = c
			break
		}
	}
	if createArgs == nil {
		t.Fatalf("expected pr create call, got %v", gh.calls)
	}
	joined := strings.Join(createArgs, " ")
	// One --reviewer per entry.
	if strings.Count(joined, "--reviewer alice") != 1 {
		t.Errorf("expected --reviewer alice once: %v", createArgs)
	}
	if strings.Count(joined, "--reviewer bob") != 1 {
		t.Errorf("expected --reviewer bob once: %v", createArgs)
	}
	if strings.Count(joined, "--label area/cli") != 1 {
		t.Errorf("expected --label area/cli: %v", createArgs)
	}
	if strings.Count(joined, "--label priority/p2") != 1 {
		t.Errorf("expected --label priority/p2 (trimmed): %v", createArgs)
	}
}

func TestCreatePR_OmitsReviewerAndLabelFlagsWhenEmpty(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr create"] = []byte("https://github.com/foo/bar/pull/77\n")
	gh.responses["pr view"] = []byte(samplePRView)
	p := NewWithRunner(gh)

	if _, err := p.CreatePR(context.Background(), "foo/bar", true, "t", "b", "h", "main",
		nil, nil); err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	for _, c := range gh.calls {
		if len(c) >= 2 && c[0] == "pr" && c[1] == "create" {
			joined := strings.Join(c, " ")
			if strings.Contains(joined, "--reviewer") {
				t.Fatalf("expected no --reviewer flag when reviewers is empty: %v", c)
			}
			if strings.Contains(joined, "--label") {
				t.Fatalf("expected no --label flag when labels is empty: %v", c)
			}
		}
	}
}

func TestCreatePR_ValidatesInputs(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	ctx := context.Background()
	if _, err := p.CreatePR(ctx, "", true, "t", "b", "h", "m", nil, nil); err == nil {
		t.Fatalf("expected error for empty repo")
	}
	if _, err := p.CreatePR(ctx, "a/b", true, "", "b", "h", "m", nil, nil); err == nil {
		t.Fatalf("expected error for empty title")
	}
	if _, err := p.CreatePR(ctx, "a/b", true, "t", "b", "", "m", nil, nil); err == nil {
		t.Fatalf("expected error for empty branch")
	}
	if _, err := p.CreatePR(ctx, "a/b", true, "t", "b", "h", "", nil, nil); err == nil {
		t.Fatalf("expected error for empty base")
	}
}

func TestUpdatePR_SendsBodyOnStdin(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	if err := p.UpdatePR(context.Background(), "foo/bar", 42, "new body"); err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	if last[0] != "pr" || last[1] != "edit" || last[2] != "42" {
		t.Fatalf("expected pr edit 42: %v", last)
	}
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "--body-file -") {
		t.Fatalf("expected --body-file -: %v", last)
	}
}

func TestSetDraft_TogglesViaPrReady(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	// draft=false → mark ready (no --undo)
	if err := p.SetDraft(context.Background(), "foo/bar", 42, false); err != nil {
		t.Fatalf("SetDraft(false): %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	if strings.Join(last, " ") != "pr ready 42 --repo foo/bar" {
		t.Fatalf("SetDraft(false) args: %v", last)
	}
	// draft=true → undo back to draft
	if err := p.SetDraft(context.Background(), "foo/bar", 42, true); err != nil {
		t.Fatalf("SetDraft(true): %v", err)
	}
	last = gh.calls[len(gh.calls)-1]
	if !strings.Contains(strings.Join(last, " "), "--undo") {
		t.Fatalf("SetDraft(true) should include --undo: %v", last)
	}
}

func TestSetAutomerge_OnOff(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	if err := p.SetAutomerge(context.Background(), "foo/bar", 42, true); err != nil {
		t.Fatalf("SetAutomerge(true): %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "--auto") || !strings.Contains(joined, "--squash") {
		t.Fatalf("expected --auto --squash: %v", last)
	}
	if err := p.SetAutomerge(context.Background(), "foo/bar", 42, false); err != nil {
		t.Fatalf("SetAutomerge(false): %v", err)
	}
	last = gh.calls[len(gh.calls)-1]
	if !strings.Contains(strings.Join(last, " "), "--disable-auto") {
		t.Fatalf("expected --disable-auto: %v", last)
	}
}

func TestMerge_PostsSquash(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	if err := p.Merge(context.Background(), "foo/bar", 42); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "--squash") {
		t.Fatalf("expected --squash: %v", last)
	}
}

func TestClose_InvokesPrClose(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	if err := p.Close(context.Background(), "foo/bar", 42); err != nil {
		t.Fatalf("Close: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	if last[0] != "pr" || last[1] != "close" || last[2] != "42" {
		t.Fatalf("expected pr close 42: %v", last)
	}
}

func TestReplyToThread_PostsGraphQL(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api graphql"] = []byte(
		`{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"PRC_1","body":"hi","author":{"login":"phillipg"}}}}}`,
	)
	p := NewWithRunner(gh)
	c, err := p.ReplyToThread(context.Background(), "foo/bar", "PRRT_xxx", "hi")
	if err != nil {
		t.Fatalf("ReplyToThread: %v", err)
	}
	if c.ID != "PRC_1" || c.Author != "phillipg" || c.ThreadID != "PRRT_xxx" {
		t.Fatalf("unexpected comment: %+v", c)
	}
}

func TestReplyToThread_ValidatesInputs(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if _, err := p.ReplyToThread(context.Background(), "foo/bar", "", "body"); err == nil {
		t.Fatalf("expected error for empty thread id")
	}
	if _, err := p.ReplyToThread(context.Background(), "foo/bar", "x", "  "); err == nil {
		t.Fatalf("expected error for empty body")
	}
}

func TestResolveThread_PostsGraphQL(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	if err := p.ResolveThread(context.Background(), "foo/bar", "PRRT_xxx"); err != nil {
		t.Fatalf("ResolveThread: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	if last[0] != "api" || last[1] != "graphql" {
		t.Fatalf("expected api graphql: %v", last)
	}
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "threadId=PRRT_xxx") {
		t.Fatalf("expected threadId arg: %v", last)
	}
}

func TestMinimizeComment_PostsGraphQL(t *testing.T) {
	gh := newFakeGH()
	p := NewWithRunner(gh)
	if err := p.MinimizeComment(context.Background(), "IC_xxx", "OUTDATED"); err != nil {
		t.Fatalf("MinimizeComment: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	if last[0] != "api" || last[1] != "graphql" {
		t.Fatalf("expected api graphql: %v", last)
	}
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "id=IC_xxx") {
		t.Fatalf("expected id arg: %v", last)
	}
	if !strings.Contains(joined, "classifier=OUTDATED") {
		t.Fatalf("expected classifier arg: %v", last)
	}
}

func TestListComments_EmptyArrays(t *testing.T) {
	p := NewWithRunner(&pathFakeGH{
		responses: map[string][]byte{
			"repos/foo/bar/issues/42/comments": []byte("[]"),
			"repos/foo/bar/pulls/42/comments":  []byte("[]"),
		},
	})
	cs, err := p.ListComments(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("expected 0 comments, got %d", len(cs))
	}
}

// ----------------------------------------------------------------------
// ListReviews tests
// ----------------------------------------------------------------------

const sampleReviews = `{
  "reviews": [
    {
      "id": 12345,
      "author": {"login": "alice"},
      "state": "APPROVED",
      "body": ""
    },
    {
      "id": 67890,
      "author": {"login": "claude[bot]"},
      "state": "COMMENTED",
      "body": "Verdict: approve — looks good"
    }
  ]
}`

func TestListReviews_ParsesAndConverts(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(sampleReviews)
	p := NewWithRunner(gh)

	got, err := p.ListReviews(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 reviews, got %d", len(got))
	}
	if got[0].Author != "alice" {
		t.Fatalf("got[0].Author: got %q want %q", got[0].Author, "alice")
	}
	if got[0].State != "APPROVED" {
		t.Fatalf("got[0].State: got %q want %q", got[0].State, "APPROVED")
	}
	if got[1].Author != "claude[bot]" {
		t.Fatalf("got[1].Author: got %q want %q", got[1].Author, "claude[bot]")
	}
	if !strings.Contains(got[1].Body, "Verdict: approve") {
		t.Fatalf("got[1].Body should contain %q, got %q", "Verdict: approve", got[1].Body)
	}
	if got[0].ID != "12345" {
		t.Fatalf("got[0].ID: got %q want %q (numeric id should be stringified)", got[0].ID, "12345")
	}
	// Verify the gh args included --json reviews.
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "--json reviews") {
		t.Fatalf("expected --json reviews in gh args: %v", last)
	}
}

func TestListReviews_ValidatesInput(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if _, err := p.ListReviews(context.Background(), "", 1); err == nil {
		t.Fatalf("expected error for empty repo")
	}
	if _, err := p.ListReviews(context.Background(), "no-slash", 1); err == nil {
		t.Fatalf("expected error for non-owner/name repo")
	}
	if _, err := p.ListReviews(context.Background(), "a/b", 0); err == nil {
		t.Fatalf("expected error for PR number=0")
	}
}

func TestListReviews_EmptyReviewsArray(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{"reviews": []}`)
	p := NewWithRunner(gh)

	got, err := p.ListReviews(context.Background(), "foo/bar", 1)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 reviews, got %d", len(got))
	}
}

// ----------------------------------------------------------------------
// Title + diff-stats population tests (Task 3)
// ----------------------------------------------------------------------

func TestGetPRPopulatesTitleAndDiffStats(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{
		"number":7,"state":"OPEN","title":"Fix bar",
		"headRefName":"f","baseRefName":"main",
		"author":{"login":"me"},"url":"u",
		"isDraft":false,"mergedAt":"","closedAt":"",
		"additions":10,"deletions":3,"changedFiles":2
	}`)
	p := NewWithRunner(gh)
	got, err := p.GetPR(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got.Title != "Fix bar" {
		t.Fatalf("Title: got %q want %q", got.Title, "Fix bar")
	}
	if got.Additions != 10 {
		t.Fatalf("Additions: got %d want 10", got.Additions)
	}
	if got.Deletions != 3 {
		t.Fatalf("Deletions: got %d want 3", got.Deletions)
	}
	if got.ChangedFiles != 2 {
		t.Fatalf("ChangedFiles: got %d want 2", got.ChangedFiles)
	}
	// Verify that the --json field list includes the new fields.
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	for _, field := range []string{"additions", "deletions", "changedFiles", "title"} {
		if !strings.Contains(joined, field) {
			t.Fatalf("expected %q in gh args: %v", field, last)
		}
	}
}

func TestListMyPRsPopulatesTitle(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr list"] = []byte(`[{
		"number":11,"title":"Add feature","headRefName":"feat/add",
		"baseRefName":"main","url":"https://github.com/foo/bar/pull/11",
		"author":{"login":"alice"},"isDraft":false,"state":"OPEN",
		"mergedAt":"","closedAt":"",
		"additions":5,"deletions":1,"changedFiles":1
	}]`)
	p := NewWithRunner(gh)

	prs, err := p.ListMyPRs(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("ListMyPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].Title != "Add feature" {
		t.Fatalf("Title: got %q want %q", prs[0].Title, "Add feature")
	}
	if prs[0].Additions != 5 {
		t.Fatalf("Additions: got %d want 5", prs[0].Additions)
	}
	if prs[0].Deletions != 1 {
		t.Fatalf("Deletions: got %d want 1", prs[0].Deletions)
	}
	if prs[0].ChangedFiles != 1 {
		t.Fatalf("ChangedFiles: got %d want 1", prs[0].ChangedFiles)
	}
}

func TestGetPRPopulatesHeadSHA(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr view"] = []byte(`{
		"number":7,"state":"OPEN","title":"feat","headRefName":"feat/x",
		"headRefOid":"cafebabe1234","baseRefName":"main",
		"author":{"login":"alice"},"url":"u",
		"isDraft":false,"mergedAt":"","closedAt":"",
		"additions":0,"deletions":0,"changedFiles":0
	}`)
	p := NewWithRunner(gh)
	got, err := p.GetPR(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got.HeadSHA != "cafebabe1234" {
		t.Errorf("HeadSHA: got %q want cafebabe1234", got.HeadSHA)
	}
	// Verify headRefOid is in the --json field list.
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "headRefOid") {
		t.Fatalf("expected headRefOid in gh --json fields: %v", last)
	}
}

func TestListMyPRsPopulatesHeadSHA(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr list"] = []byte(`[{
		"number":11,"title":"Add feature","headRefName":"feat/add",
		"headRefOid":"beefdead","baseRefName":"main",
		"url":"https://github.com/foo/bar/pull/11",
		"author":{"login":"alice"},"isDraft":false,"state":"OPEN",
		"mergedAt":"","closedAt":"",
		"additions":0,"deletions":0,"changedFiles":0
	}]`)
	p := NewWithRunner(gh)
	prs, err := p.ListMyPRs(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("ListMyPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].HeadSHA != "beefdead" {
		t.Errorf("HeadSHA: got %q want beefdead", prs[0].HeadSHA)
	}
}

func TestListTeamPRsPopulatesTitle(t *testing.T) {
	gh := newFakeGH()
	gh.responses["pr list"] = []byte(`[{
		"number":99,"title":"Team PR","headRefName":"team/feat",
		"baseRefName":"main","url":"https://github.com/foo/bar/pull/99",
		"author":{"login":"bob"},"isDraft":false,"state":"OPEN",
		"mergedAt":"","closedAt":"",
		"additions":20,"deletions":4,"changedFiles":3
	}]`)
	p := NewWithRunner(gh)

	prs, err := p.ListTeamPRs(context.Background(), "foo/bar", []string{"bob"})
	if err != nil {
		t.Fatalf("ListTeamPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if prs[0].Title != "Team PR" {
		t.Fatalf("Title: got %q want %q", prs[0].Title, "Team PR")
	}
	if prs[0].Additions != 20 {
		t.Fatalf("Additions: got %d want 20", prs[0].Additions)
	}
}

// pathFakeGH dispatches on the second arg (the path) — cleaner for `api`.
type pathFakeGH struct {
	responses map[string][]byte
}

func (f *pathFakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) < 2 {
		return []byte("[]"), nil
	}
	if r, ok := f.responses[args[1]]; ok {
		return r, nil
	}
	return []byte("[]"), nil
}

func (f *pathFakeGH) RunStdin(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if len(args) < 2 {
		return []byte("{}"), nil
	}
	if r, ok := f.responses[args[1]]; ok {
		return r, nil
	}
	return []byte("{}"), nil
}
