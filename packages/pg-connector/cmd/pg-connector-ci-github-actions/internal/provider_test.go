// provider_test.go is carried over from
// packages/pg-pr/pkg/provider/cicd/ghactions/ghactions_test.go
// [contract: "Tests: carry over the existing test files alongside the
// ported implementation (adapted types)"], adapted for ci.Provider's
// id-only signatures (ListRuns/RerunFailed take a single prID rather than
// repo+prNumber) and the PRResolver seam now resolving both repo and
// branch (resolver.go) rather than just a branch. It also adds the
// packet's two explicitly required tests: a scriptout-level end-to-end
// test lives in main_test.go; the "ListRuns' returned CIRuns carry a
// non-empty PRID" test is TestListRuns_PRIDPopulated below.
package internal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeGH replays canned responses keyed by the first two arguments
// ("run list", "run view", "run rerun"), carried over unchanged from
// ghactions_test.go's own fakeGH.
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

// fakePR satisfies this backend's own PRResolver interface (resolver.go)
// with a controllable repo/branch, adapted from ghactions_test.go's own
// fakePR (which resolved only a branch, taking repo+number as separate
// arguments — ci.Provider's id-only signature means this backend's own
// PRResolver must resolve the repo too).
type fakePR struct {
	repo   string
	branch string
	err    error
}

func (f *fakePR) Resolve(_ context.Context, _ string) (string, string, error) {
	return f.repo, f.branch, f.err
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

func TestListRuns_ResolvesRepoAndBranchAndFilters(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
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
	if !strings.Contains(joined, "--repo foo/bar") {
		t.Fatalf("expected --repo foo/bar: %v", last)
	}
	if !strings.Contains(joined, "--branch feat/x") {
		t.Fatalf("expected --branch feat/x: %v", last)
	}
}

// TestListRuns_PRIDPopulated is this packet's required test: every
// returned CIRun carries a non-empty PRID [contract; design: §2 AC].
func TestListRuns_PRIDPopulated(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected at least one run")
	}
	for _, r := range runs {
		if r.PRID != "foo/bar#42" {
			t.Errorf("run %s PRID = %q, want %q", r.ID, r.PRID, "foo/bar#42")
		}
	}
}

func TestListRuns_ValidatesEmptyID(t *testing.T) {
	p := NewWithDeps(newFakeGH(), &fakePR{repo: "a/b", branch: "x"})
	if _, err := p.ListRuns(context.Background(), ""); err == nil {
		t.Fatalf("expected error for empty pr id")
	}
	if _, err := p.ListRuns(context.Background(), "   "); err == nil {
		t.Fatalf("expected error for whitespace-only pr id")
	}
}

func TestListRuns_ResolverInvalidRepo(t *testing.T) {
	p := NewWithDeps(newFakeGH(), &fakePR{repo: "", branch: "feat/x"})
	if _, err := p.ListRuns(context.Background(), "foo/bar#42"); err == nil {
		t.Fatalf("expected error for empty repo from resolver")
	}
}

func TestListRuns_ResolverInvalidBranch(t *testing.T) {
	p := NewWithDeps(newFakeGH(), &fakePR{repo: "foo/bar", branch: ""})
	if _, err := p.ListRuns(context.Background(), "foo/bar#42"); err == nil {
		t.Fatalf("expected error for empty branch from resolver")
	}
}

func TestListRuns_PropagatesResolverError(t *testing.T) {
	resolverErr := errors.New("boom: resolver failed")
	p := NewWithDeps(newFakeGH(), &fakePR{err: resolverErr})
	_, err := p.ListRuns(context.Background(), "foo/bar#42")
	if !errors.Is(err, resolverErr) {
		t.Fatalf("expected propagated resolver error, got %v", err)
	}
}

func TestListRuns_PropagatesGHError(t *testing.T) {
	gh := newFakeGH()
	gh.errs["run list"] = errors.New("boom")
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if _, err := p.ListRuns(context.Background(), "foo/bar#42"); err == nil {
		t.Fatalf("expected propagated error")
	}
}

func TestListRuns_EmptyArray(t *testing.T) {
	p := NewWithDeps(newFakeGH(), &fakePR{repo: "foo/bar", branch: "feat/x"})
	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
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
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar#42"); err != nil {
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
		`[{"databaseId":1,"status":"completed","conclusion":"success","headBranch":"f"}]`,
	)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	err := p.RerunFailed(context.Background(), "foo/bar#42")
	if err == nil {
		t.Fatalf("expected error when no failed runs")
	}
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListRuns_HeadSHAPropagated(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(sampleRunList)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})

	runs, err := p.ListRuns(context.Background(), "foo/bar#42")
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

func TestCheckAuth_PropagatesGHError(t *testing.T) {
	gh := newFakeGH()
	gh.errs["api graphql"] = errors.New("boom")
	p := NewWithDeps(gh, nil)
	if err := p.CheckAuth(context.Background()); err == nil {
		t.Fatalf("expected propagated error")
	}
}

func TestCheckAuth_Success(t *testing.T) {
	p := NewWithDeps(newFakeGH(), nil)
	if err := p.CheckAuth(context.Background()); err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
}
