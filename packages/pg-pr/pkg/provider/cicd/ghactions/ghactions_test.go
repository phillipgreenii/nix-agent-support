package ghactions

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGH replays canned responses keyed by the first two arguments
// ("run list", "run view", "run rerun").
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
	key := strings.Join(args[:minInt(2, len(args))], " ")
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if resp, ok := f.responses[key]; ok {
		return resp, nil
	}
	return []byte("[]"), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fakePR satisfies the PRResolver interface with controllable head branch.
type fakePR struct {
	branch string
	err    error
}

func (f *fakePR) PRHeadBranch(_ context.Context, _ string, _ int) (string, error) {
	return f.branch, f.err
}

const sampleRunList = `[
  {
    "databaseId": 1001,
    "name": "ci",
    "status": "completed",
    "conclusion": "failure",
    "url": "https://github.com/foo/bar/actions/runs/1001",
    "headBranch": "feat/x",
    "headSha": "deadbeef"
  },
  {
    "databaseId": 1002,
    "name": "lint",
    "status": "completed",
    "conclusion": "success",
    "url": "https://github.com/foo/bar/actions/runs/1002",
    "headBranch": "feat/x",
    "headSha": "deadbeef"
  }
]`

func TestListRuns_ResolvesBranchAndFilters(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	p := NewWithDeps(gh, &fakePR{branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].ID != "1001" || runs[0].Conclusion != "failure" {
		t.Fatalf("run[0]: %+v", runs[0])
	}
	if runs[0].Provider != ProviderName {
		t.Fatalf("provider tag: %q", runs[0].Provider)
	}

	// Verify gh args.
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "--branch feat/x") {
		t.Fatalf("expected --branch feat/x: %v", last)
	}
}

func TestListRunsByBranch_SkipsPRResolver(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	// PRResolver intentionally nil: ListRunsByBranch must not consult it.
	p := NewWithDeps(gh, nil)

	runs, err := p.ListRunsByBranch(context.Background(), "foo/bar", "feat/x")
	if err != nil {
		t.Fatalf("ListRunsByBranch: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "--branch feat/x") {
		t.Fatalf("expected --branch feat/x: %v", last)
	}
}

func TestListRunsByBranch_RequiresBranch(t *testing.T) {
	p := NewWithDeps(newFakeGH(), nil)
	if _, err := p.ListRunsByBranch(context.Background(), "foo/bar", ""); err == nil {
		t.Fatalf("expected error for empty branch")
	}
	if _, err := p.ListRunsByBranch(context.Background(), "foo/bar", "   "); err == nil {
		t.Fatalf("expected error for whitespace-only branch")
	}
}

func TestListRuns_ValidatesInputs(t *testing.T) {
	p := NewWithDeps(newFakeGH(), &fakePR{branch: "x"})
	if _, err := p.ListRuns(context.Background(), "", 1); err == nil {
		t.Fatalf("expected error for empty repo")
	}
	if _, err := p.ListRuns(context.Background(), "a/b", 0); err == nil {
		t.Fatalf("expected error for PR=0")
	}
}

func TestListRuns_MissingResolver(t *testing.T) {
	p := NewWithDeps(newFakeGH(), nil)
	if _, err := p.ListRuns(context.Background(), "foo/bar", 1); err == nil {
		t.Fatalf("expected error when PRResolver is nil")
	}
}

func TestListRuns_PropagatesGHError(t *testing.T) {
	gh := newFakeGH()
	gh.errs["run list"] = errors.New("boom")
	p := NewWithDeps(gh, &fakePR{branch: "feat/x"})
	if _, err := p.ListRuns(context.Background(), "foo/bar", 42); err == nil {
		t.Fatalf("expected propagated error")
	}
}

func TestListRuns_EmptyArray(t *testing.T) {
	p := NewWithDeps(newFakeGH(), &fakePR{branch: "feat/x"})
	runs, err := p.ListRuns(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs, got %d", len(runs))
	}
}

func TestGetLogs_ReturnsRawBytes(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run view"] = []byte("log output here")
	p := NewWithDeps(gh, nil)
	raw, err := p.GetLogs(context.Background(), "1001")
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if string(raw) != "log output here" {
		t.Fatalf("logs: %q", raw)
	}
	last := gh.calls[len(gh.calls)-1]
	if last[0] != "run" || last[1] != "view" || last[2] != "1001" || last[3] != "--log" {
		t.Fatalf("expected run view 1001 --log: %v", last)
	}
}

func TestGetLogs_ValidatesEmpty(t *testing.T) {
	p := NewWithDeps(newFakeGH(), nil)
	if _, err := p.GetLogs(context.Background(), ""); err == nil {
		t.Fatalf("expected error for empty run id")
	}
}

func TestRerunFailed_PicksLatestFailedRun(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	p := NewWithDeps(gh, &fakePR{branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar", 42); err != nil {
		t.Fatalf("RerunFailed: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "run rerun 1001 --failed") {
		t.Fatalf("expected rerun 1001: %v", last)
	}
}

func TestRerunFailed_NoFailedRuns(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(
		`[{"databaseId":1,"status":"completed","conclusion":"success","headBranch":"f"}]`)
	p := NewWithDeps(gh, &fakePR{branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar", 42); err == nil {
		t.Fatalf("expected error when no failed runs")
	}
}

func TestListRuns_HeadSHAPropagated(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	p := NewWithDeps(gh, &fakePR{branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	// sampleRunList has headSha:"deadbeef" for both runs.
	for _, r := range runs {
		if r.HeadSHA != "deadbeef" {
			t.Errorf("run %s HeadSHA: got %q want \"deadbeef\"", r.ID, r.HeadSHA)
		}
	}
}
