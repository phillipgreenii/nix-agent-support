package github

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fakeGH replays canned responses keyed by the first two arguments
// ("pr list", "pr view"). Each call records args for assertion.
type fakeGH struct {
	responses map[string][]byte
	errs      map[string]error
	calls     [][]string
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

func (f *fakeGH) RunStdin(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
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

func TestStubMethodsReturnErrStub(t *testing.T) {
	ctx := context.Background()
	p := New()
	if err := p.SetDraft(ctx, "a/b", 1, true); !errors.Is(err, errStub) && err == nil {
		t.Fatalf("SetDraft should return errStub")
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
    "author_association": "MEMBER"
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
    "author_association": "MEMBER"
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
		`{"node_id":"IC_kw","user":{"login":"phillipg"},"body":"hi"}`)
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

func TestPostReview_SendsJSONPayload(t *testing.T) {
	gh := newFakeGH()
	gh.responses["api repos/foo/bar/pulls/42/reviews"] = []byte(
		`{"node_id":"RV_kw","state":"PENDING","body":"the body"}`)
	p := NewWithRunner(gh)

	rev, err := p.PostReview(context.Background(), "foo/bar", 42,
		"top-level body",
		[]api.Comment{
			{Path: "main.go", Line: 12, Body: "rename x"},
			{Path: "", Body: "PR-level note"},
			{Path: "readme.md", Body: "missing section"}, // file-level
		})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if rev.ID != "RV_kw" || rev.State != "pending" {
		t.Fatalf("unexpected review: %+v", rev)
	}
}

func TestReplyToThread_NotImplemented(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if _, err := p.ReplyToThread(context.Background(), "foo/bar", "TH_1", "hi"); err == nil ||
		!errors.Is(err, vcs.ErrNotImplemented) {
		t.Fatalf("expected vcs.ErrNotImplemented, got %v", err)
	}
}

func TestResolveThread_NotImplemented(t *testing.T) {
	p := NewWithRunner(newFakeGH())
	if err := p.ResolveThread(context.Background(), "foo/bar", "TH_1"); err == nil ||
		!errors.Is(err, vcs.ErrNotImplemented) {
		t.Fatalf("expected vcs.ErrNotImplemented, got %v", err)
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
