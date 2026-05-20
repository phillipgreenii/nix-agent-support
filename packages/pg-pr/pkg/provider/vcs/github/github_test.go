package github

import (
	"context"
	"errors"
	"strings"
	"testing"
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
