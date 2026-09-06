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
	"fmt"
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
	_, err := p.ListRuns(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for empty pr id")
	}
	// An empty required field is the CALLER's mistake, not this backend
	// being unhealthy [design: §4.2, bug pg2-r9iok].
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if _, err := p.ListRuns(context.Background(), "   "); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument) for whitespace-only pr id", err)
	}
}

// TestListRuns_ResolverInvalidID_IsInvalidArgument proves a prID that
// doesn't parse into this backend's own "<owner>/<repo>#<number>" id shape
// — the real (non-fakePR) resolver path — is now classified as
// ErrInvalidArgument, not left unwrapped to fall through to
// codeForError's "unavailable" fallback [bug pg2-r9iok].
func TestListRuns_ResolverInvalidID_IsInvalidArgument(t *testing.T) {
	p := New() // production wiring: real ghPRResolver, no fakePR
	_, err := p.ListRuns(context.Background(), "not-a-valid-id")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestListRuns_NonexistentPR_NotFound proves the GraphQL "could not
// resolve" phrasing gh returns for a nonexistent PR number (verified
// empirically against real `gh` 2.99.0 — see provider.go's isGHNotFound
// doc comment) is reachable as not_found through the real resolver path,
// not misreported as this backend being unhealthy [design: §4.5, bug
// pg2-r9iok].
func TestListRuns_NonexistentPR_NotFound(t *testing.T) {
	gh := newFakeGH()
	gh.errs["pr view"] = errors.New("gh pr view 999999999: exit status 1: GraphQL: Could not resolve to a PullRequest with the number of 999999999. (repository.pullRequest)")
	p := NewWithDeps(gh, newGHPRResolver(gh))

	_, err := p.ListRuns(context.Background(), "foo/bar#999999999")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must NOT also be ErrUnavailable — a not_found answer must not share a code with a failure", err)
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
	_, err := p.GetLogs(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for empty run id")
	}
	// An empty required field is the CALLER's mistake, not this backend
	// being unhealthy [design: §4.2, bug pg2-r9iok].
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestGetLogs_NonexistentRun_NotFound proves the REST-404 phrasing gh
// returns for a nonexistent run id (verified empirically against real `gh`
// 2.99.0: `gh run view <id> --log` prints "failed to get run: HTTP 404: Not
// Found (...)" on stderr, exit 1 — see provider.go's isGHNotFound doc
// comment) is now reachable as not_found, not misreported as this backend
// being unhealthy [design: §4.5, bug pg2-r9iok].
func TestGetLogs_NonexistentRun_NotFound(t *testing.T) {
	gh := newFakeGH()
	gh.errs["run view"] = errors.New("failed to get run: HTTP 404: Not Found (https://api.github.com/repos/foo/bar/actions/runs/999999999999?exclude_pull_requests=true)")
	p := NewWithDeps(gh, nil)

	_, err := p.GetLogs(context.Background(), "999999999999")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must NOT also be ErrUnavailable — a not_found answer must not share a code with a failure", err)
	}
}

// TestGetLogs_GenuineGHFailure_PassesThroughUnclassified proves a real gh
// failure unrelated to auth or not-found still propagates unwrapped
// (classifyGHError's "everything else" case, matching the sibling
// pg-connector-pr-github backend's own contract).
func TestGetLogs_GenuineGHFailure_PassesThroughUnclassified(t *testing.T) {
	gh := newFakeGH()
	wantErr := errors.New(`run view: exec: "gh": executable file not found in $PATH`)
	gh.errs["run view"] = wantErr
	p := NewWithDeps(gh, nil)

	_, err := p.GetLogs(context.Background(), "1001")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want errors.Is(err, wantErr)", err)
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must NOT be classified as ErrNotFound", err)
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

// TestRerunFailed_MatchesTimedOut, TestRerunFailed_MatchesStartupFailure and
// TestRerunFailed_MatchesCancelled each pin one conclusion pg-mzymd added to
// rerunnableConclusions: before the fix, a run ending in any of these three
// states returned ErrNotFound instead of being rerun.
func TestRerunFailed_MatchesTimedOut(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(
		`[{"databaseId":2001,"status":"completed","conclusion":"timed_out","headBranch":"feat/x"}]`,
	)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar#42"); err != nil {
		t.Fatalf("RerunFailed: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "run rerun 2001 --failed") {
		t.Fatalf("expected rerun 2001: %v", last)
	}
}

func TestRerunFailed_MatchesStartupFailure(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(
		`[{"databaseId":2002,"status":"completed","conclusion":"startup_failure","headBranch":"feat/x"}]`,
	)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar#42"); err != nil {
		t.Fatalf("RerunFailed: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "run rerun 2002 --failed") {
		t.Fatalf("expected rerun 2002: %v", last)
	}
}

func TestRerunFailed_MatchesCancelled(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(
		`[{"databaseId":2003,"status":"completed","conclusion":"cancelled","headBranch":"feat/x"}]`,
	)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar#42"); err != nil {
		t.Fatalf("RerunFailed: %v", err)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "run rerun 2003 --failed") {
		t.Fatalf("expected rerun 2003: %v", last)
	}
}

// TestRerunFailed_ExcludedConclusionsStayNotFound proves rerunnableConclusions'
// deliberate exclusions ("action_required", "skipped", "neutral", "stale")
// keep answering ErrNotFound rather than being swept in by the pg2-mzymd
// widening — the widening is targeted at the three genuinely-failed
// conclusions, not "anything that isn't success".
func TestRerunFailed_ExcludedConclusionsStayNotFound(t *testing.T) {
	for _, conclusion := range []string{"action_required", "skipped", "neutral", "stale"} {
		gh := newFakeGH()
		gh.responses["run list"] = []byte(
			fmt.Sprintf(`[{"databaseId":3001,"status":"completed","conclusion":%q,"headBranch":"f"}]`, conclusion),
		)
		p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
		err := p.RerunFailed(context.Background(), "foo/bar#42")
		if err == nil {
			t.Fatalf("conclusion %q: expected error, got nil", conclusion)
		}
		if !errors.Is(err, scriptout.ErrNotFound) {
			t.Errorf("conclusion %q: expected ErrNotFound, got %v", conclusion, err)
		}
	}
}

// TestRerunFailed_OnlyRerunsNewestMatch pins the other half of pg2-mzymd's
// decision: RerunFailed reruns only the single newest matching run, never
// every matching run, even though two different rerunnable conclusions are
// both present. This is unchanged, deliberate behavior carried over from
// ghactions.go (see RerunFailed's doc comment) — this test exists so a
// future change to "rerun every match" is a conscious decision, not an
// accidental regression.
func TestRerunFailed_OnlyRerunsNewestMatch(t *testing.T) {
	gh := newFakeGH()
	gh.responses["run list"] = []byte(`[
		{"databaseId":4002,"status":"completed","conclusion":"timed_out","headBranch":"feat/x"},
		{"databaseId":4001,"status":"completed","conclusion":"failure","headBranch":"feat/x"}
	]`)
	p := NewWithDeps(gh, &fakePR{repo: "foo/bar", branch: "feat/x"})
	if err := p.RerunFailed(context.Background(), "foo/bar#42"); err != nil {
		t.Fatalf("RerunFailed: %v", err)
	}
	rerunCalls := 0
	for _, call := range gh.calls {
		if len(call) >= 2 && call[0] == "run" && call[1] == "rerun" {
			rerunCalls++
		}
	}
	if rerunCalls != 1 {
		t.Fatalf("expected exactly 1 rerun call (newest match only), got %d: %v", rerunCalls, gh.calls)
	}
	last := gh.calls[len(gh.calls)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "run rerun 4002 --failed") {
		t.Fatalf("expected rerun of newest match 4002, not the older match 4001: %v", last)
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
