package sync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// ----------------------------------------------------------------------
// Fakes
// ----------------------------------------------------------------------

type fakeVCS struct {
	my       map[string][]api.PR // keyed by repo
	team     map[string][]api.PR
	views    map[string]api.PR // keyed by repo#pr
	myErr    map[string]error
	teamErr  map[string]error
	viewsErr map[string]error

	// Reply pipeline (B3) recording. nil => not enabled (the engine will
	// type-assert to ThreadReplier; with replyCalls non-nil the assertion
	// uses the wrapper below in newReplyFakeVCS).
	replyCalls   []replyCall
	replyResp    *api.Comment // canned response; nil = error
	replyRespErr error

	// SetDraft recording. The engine type-asserts to DraftToggler; with
	// setDraftCalls non-nil, the assertion succeeds via the method below.
	setDraftCalls []setDraftCall
	setDraftErr   error
}

type replyCall struct {
	Repo     string
	ThreadID string
	Body     string
}

type setDraftCall struct {
	Repo   string
	Number int
	Draft  bool
}

func (f *fakeVCS) SetDraft(_ context.Context, repo string, n int, draft bool) error {
	f.setDraftCalls = append(f.setDraftCalls, setDraftCall{Repo: repo, Number: n, Draft: draft})
	return f.setDraftErr
}

func newFakeVCS() *fakeVCS {
	return &fakeVCS{
		my:       map[string][]api.PR{},
		team:     map[string][]api.PR{},
		views:    map[string]api.PR{},
		myErr:    map[string]error{},
		teamErr:  map[string]error{},
		viewsErr: map[string]error{},
	}
}

func (f *fakeVCS) GetPR(_ context.Context, repo string, n int) (*api.PR, error) {
	key := keyOf(repo, n)
	if err := f.viewsErr[key]; err != nil {
		return nil, err
	}
	pr, ok := f.views[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return &pr, nil
}

func (f *fakeVCS) ListMyPRs(_ context.Context, repo string) ([]api.PR, error) {
	if err := f.myErr[repo]; err != nil {
		return nil, err
	}
	return f.my[repo], nil
}

func (f *fakeVCS) ListTeamPRs(_ context.Context, repo string, _ []string) ([]api.PR, error) {
	if err := f.teamErr[repo]; err != nil {
		return nil, err
	}
	return f.team[repo], nil
}

// ReplyToThread satisfies the ThreadReplier interface. Calls are recorded
// in f.replyCalls; the return value comes from f.replyResp / f.replyRespErr.
func (f *fakeVCS) ReplyToThread(_ context.Context, repo, threadID, body string) (*api.Comment, error) {
	f.replyCalls = append(f.replyCalls, replyCall{Repo: repo, ThreadID: threadID, Body: body})
	if f.replyRespErr != nil {
		return nil, f.replyRespErr
	}
	return f.replyResp, nil
}

func keyOf(repo string, n int) string { return repo + "#" + itoa(n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	out := ""
	for n > 0 {
		out = string('0'+rune(n%10)) + out
		n /= 10
	}
	if neg {
		out = "-" + out
	}
	return out
}

// fakeCICD is a minimal CICDProvider for tests. runs is keyed by
// "repo#prNumber"; missing keys return an empty slice (treated by
// allRunsSuccessful as "no runs" → not promotable).
type fakeCICD struct {
	runs map[string][]api.CIRun
}

func newFakeCICD() *fakeCICD {
	return &fakeCICD{runs: map[string][]api.CIRun{}}
}

func (c *fakeCICD) ListRuns(_ context.Context, repo string, n int) ([]api.CIRun, error) {
	return c.runs[keyOf(repo, n)], nil
}

// successRun returns a single completed+successful CI run, the shape
// allRunsSuccessful requires for draft promotion to fire.
func successRun() api.CIRun {
	return api.CIRun{Status: "completed", Conclusion: "success"}
}

// ----------------------------------------------------------------------
// Test bd workspace
// ----------------------------------------------------------------------

var bdCounter int64

func newRealBDClient(t *testing.T) *beads.Client {
	t.Helper()
	dir, env := newRealBDWorkspaceDir(t)
	runner := &beads.CLIRunner{Dir: dir, Env: env}
	return beads.NewClientWithRunner(runner)
}

// newRealBDWorkspaceDir boots a fresh bd workspace under t.TempDir() and
// returns (dir, cleanEnv). Used by tests that need to construct per-repo
// bd clients via beads.NewClientForRepo against a real workspace.
func newRealBDWorkspaceDir(t *testing.T) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	dir := t.TempDir()
	n := atomic.AddInt64(&bdCounter, 1)
	prefix := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	prefix = alnum(prefix) + intToBase36(n)
	env := cleanEnv()
	init := exec.Command("bd", "init", "--prefix", prefix)
	init.Dir = dir
	init.Env = env
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}
	cfgSet := exec.Command("bd", "config", "set", "types.custom", "merge-request,feedback")
	cfgSet.Dir = dir
	cfgSet.Env = env
	if out, err := cfgSet.CombinedOutput(); err != nil {
		t.Fatalf("bd config set: %v\n%s", err, out)
	}
	return dir, env
}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.Index(kv, "="); i > 0 {
			k = kv[:i]
		}
		if k == "BEADS_DIR" || k == "WORKSPACE_ROOT" || k == "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func intToBase36(n int64) string {
	if n == 0 {
		return "0"
	}
	const al = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := ""
	for n > 0 {
		out = string(al[n%36]) + out
		n /= 36
	}
	return out
}

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

func minimalCfg() *config.Config {
	return &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: "/tmp/wr",
		Repos: []config.RepoConfig{
			{Remote: "foo/bar", VCS: "github", TeamMembers: []string{"alice"}},
		},
	}
}

func makeEngine(t *testing.T, vcs *fakeVCS) *Engine {
	t.Helper()
	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func samplePR(n int, repo, branch string) api.PR {
	return api.PR{
		Repo:   repo,
		Number: n,
		State:  "open",
		Branch: branch,
		Base:   "main",
		Author: "phillipg",
		URL:    "https://github.com/" + repo + "/pull/" + itoa(n),
	}
}

// teammatePR returns a draft PR authored by someone other than the
// configured SelfLogin ("phillipg"). Used by tests asserting the
// ownership guards.
func teammatePR(n int, repo, branch string) api.PR {
	pr := samplePR(n, repo, branch)
	pr.Author = "coworker"
	pr.Draft = true
	return pr
}

// selfDraftPR returns a self-authored draft PR.
func selfDraftPR(n int, repo, branch string) api.PR {
	pr := samplePR(n, repo, branch)
	pr.Draft = true
	return pr
}

// cfgWithCICD returns a config that wires a single CICD provider name
// ("ci") on the foo/bar repo so maybePromoteDraft can fire in tests.
func cfgWithCICD() *config.Config {
	return &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: "/tmp/wr",
		Repos: []config.RepoConfig{
			{Remote: "foo/bar", VCS: "github", CICD: []string{"ci"}, TeamMembers: []string{"coworker"}},
		},
	}
}

func TestSync_CreatesBeadsForObservedPRs(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/x")}
	vcs.team["foo/bar"] = []api.PR{samplePR(2, "foo/bar", "feat/y")}

	e := makeEngine(t, vcs)
	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v\n%+v", err, sum)
	}
	if sum.TotalPRs != 2 {
		t.Fatalf("TotalPRs: got %d want 2", sum.TotalPRs)
	}
	if sum.BeadsCreated != 2 {
		t.Fatalf("BeadsCreated: got %d want 2", sum.BeadsCreated)
	}
	if sum.BeadsUpdated != 0 {
		t.Fatalf("BeadsUpdated: got %d want 0", sum.BeadsUpdated)
	}
	if len(sum.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", sum.Errors)
	}
}

func TestSync_IdempotentOnReRun(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/x")}
	e := makeEngine(t, vcs)

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if sum.BeadsCreated != 0 {
		t.Fatalf("re-run BeadsCreated: got %d want 0", sum.BeadsCreated)
	}
	if sum.BeadsUpdated != 1 {
		t.Fatalf("re-run BeadsUpdated: got %d want 1", sum.BeadsUpdated)
	}
}

func TestSync_ClosesBeadsWhenPRDisappears(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{
		samplePR(1, "foo/bar", "feat/x"),
		samplePR(2, "foo/bar", "feat/y"),
	}
	e := makeEngine(t, vcs)
	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// PR #2 disappears from the watched set on a subsequent sync.
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/x")}
	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if sum.BeadsClosed != 1 {
		t.Fatalf("BeadsClosed: got %d want 1", sum.BeadsClosed)
	}
}

func TestSync_DoesNotCloseBeadsForFailedRepo(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/x")}
	e := makeEngine(t, vcs)
	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Simulate failure on the next enum.
	vcs.myErr["foo/bar"] = errors.New("gh auth required")
	sum, err := e.Sync(ctx)
	if err == nil {
		t.Fatalf("expected aggregate error when repo enum fails")
	}
	if sum.BeadsClosed != 0 {
		t.Fatalf("must not close beads for failed repo (got %d closed)", sum.BeadsClosed)
	}
	if len(sum.Errors) == 0 {
		t.Fatalf("expected errors in summary")
	}
}

func TestSync_WritesStateFile(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/x")}
	e := makeEngine(t, vcs)

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	statePath := filepath.Join(e.deps.StateDir, "repo-state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var sf stateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if _, ok := sf.Repos["foo/bar"]; !ok {
		t.Fatalf("expected state for foo/bar, got %+v", sf)
	}
}

func TestSyncPR_SingleRefresh(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.views[keyOf("foo/bar", 42)] = samplePR(42, "foo/bar", "feat/z")
	e := makeEngine(t, vcs)

	sum, err := e.SyncPR(ctx, "foo/bar", 42)
	if err != nil {
		t.Fatalf("SyncPR: %v", err)
	}
	if sum.TotalPRs != 1 {
		t.Fatalf("TotalPRs: %d", sum.TotalPRs)
	}
	if sum.BeadsUpdated != 1 {
		t.Fatalf("BeadsUpdated: %d", sum.BeadsUpdated)
	}
}

func TestSyncPR_ClosesWhenUpstreamMerged(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	open := samplePR(42, "foo/bar", "feat/z")
	vcs.views[keyOf("foo/bar", 42)] = open
	e := makeEngine(t, vcs)

	if _, err := e.SyncPR(ctx, "foo/bar", 42); err != nil {
		t.Fatalf("first SyncPR: %v", err)
	}

	merged := open
	merged.State = "merged"
	merged.Merged = true
	vcs.views[keyOf("foo/bar", 42)] = merged

	sum, err := e.SyncPR(ctx, "foo/bar", 42)
	if err != nil {
		t.Fatalf("merged SyncPR: %v", err)
	}
	if sum.BeadsClosed != 1 {
		t.Fatalf("expected BeadsClosed=1, got %d", sum.BeadsClosed)
	}
}

// TestSync_PerRepoWorkspaceIsolation verifies that when the engine's
// Deps.Beads is unset (production wiring), each repo's bd operations land
// in its own .beads/ workspace. Two repos point at two distinct temp
// workspaces; after Sync, each workspace must hold only the beads for its
// own PRs.
func TestSync_PerRepoWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()

	// Strip BEADS_DIR/WORKSPACE_ROOT for the test process so the bd
	// invocations the engine spawns inherit a clean env. beads.NewClientForRepo
	// only sets Dir, so it relies on the process env being clean of these
	// overrides.
	t.Setenv("BEADS_DIR", "")
	t.Setenv("WORKSPACE_ROOT", "")
	t.Setenv("ZR_MACHINE_SUPPORT_WORKSPACE_ROOT", "")
	if err := os.Unsetenv("BEADS_DIR"); err != nil {
		t.Fatalf("unset BEADS_DIR: %v", err)
	}
	if err := os.Unsetenv("WORKSPACE_ROOT"); err != nil {
		t.Fatalf("unset WORKSPACE_ROOT: %v", err)
	}
	if err := os.Unsetenv("ZR_MACHINE_SUPPORT_WORKSPACE_ROOT"); err != nil {
		t.Fatalf("unset ZR_MACHINE_SUPPORT_WORKSPACE_ROOT: %v", err)
	}

	dirA, _ := newRealBDWorkspaceDir(t)
	dirB, _ := newRealBDWorkspaceDir(t)

	vcs := newFakeVCS()
	vcs.my["mono/a"] = []api.PR{samplePR(1, "mono/a", "feat/a1")}
	vcs.my["mono/b"] = []api.PR{samplePR(2, "mono/b", "feat/b2")}

	cfg := &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: "/tmp/wr",
		Repos: []config.RepoConfig{
			{Remote: "mono/a", VCS: "github", Path: dirA},
			{Remote: "mono/b", VCS: "github", Path: dirB},
		},
	}

	// Deps.Beads intentionally nil so the engine constructs a fresh
	// per-repo Client via beads.NewClientForRepo(rcfg.Path) for each repo.
	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vcs},
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Inspect each workspace directly via a fresh per-repo Client.
	clientA := beads.NewClientForRepo(dirA)
	clientB := beads.NewClientForRepo(dirB)

	listA, err := clientA.ListMergeRequests(ctx, true)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	listB, err := clientB.ListMergeRequests(ctx, true)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}

	if len(listA) != 1 {
		t.Fatalf("workspace A: got %d beads, want 1 (%+v)", len(listA), listA)
	}
	if listA[0].Fields.Repo != "mono/a" || listA[0].Fields.PRNumber != 1 {
		t.Fatalf("workspace A bead: got %+v want mono/a#1", listA[0].Fields)
	}
	if len(listB) != 1 {
		t.Fatalf("workspace B: got %d beads, want 1 (%+v)", len(listB), listB)
	}
	if listB[0].Fields.Repo != "mono/b" || listB[0].Fields.PRNumber != 2 {
		t.Fatalf("workspace B bead: got %+v want mono/b#2", listB[0].Fields)
	}
}

func TestSyncPR_RejectsUnknownRepo(t *testing.T) {
	ctx := context.Background()
	e := makeEngine(t, newFakeVCS())
	_, err := e.SyncPR(ctx, "no/such-repo", 1)
	if err == nil {
		t.Fatalf("expected error for repo not in config")
	}
	if !strings.Contains(err.Error(), "not in config") {
		t.Fatalf("error message: %v", err)
	}
}

func TestNew_ValidatesRequiredDeps(t *testing.T) {
	_, err := New(Deps{})
	if err == nil {
		t.Fatalf("expected error for missing cfg")
	}
	// Deps.Beads is now optional: when unset, the engine constructs a
	// per-repo Client via beads.NewClientForRepo. Only cfg + at least one
	// VCS provider remain required.
	_, err = New(Deps{Cfg: minimalCfg()})
	if err == nil {
		t.Fatalf("expected error for missing VCS")
	}
	if _, err := New(Deps{Cfg: minimalCfg(), VCS: map[string]VCSProvider{"github": newFakeVCS()}}); err != nil {
		t.Fatalf("expected New to succeed without Beads (per-repo client mode): %v", err)
	}
	if _, err := New(Deps{Cfg: minimalCfg(), VCS: map[string]VCSProvider{"github": newFakeVCS()}, Beads: &noopBeads{}}); err != nil {
		t.Fatalf("expected New to succeed with injected Beads: %v", err)
	}
}

// noopBeads is a do-nothing BeadClient used only for New validation tests.
type noopBeads struct{}

func (noopBeads) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "", false, nil
}
func (noopBeads) UpdateMergeRequest(context.Context, string, beads.MergeRequestFields) error {
	return nil
}
func (noopBeads) CloseMergeRequest(context.Context, string, string) error { return nil }
func (noopBeads) ListMergeRequests(context.Context, bool) ([]beads.MergeRequest, error) {
	return nil, nil
}
func (noopBeads) GetMergeRequest(context.Context, string) (*beads.MergeRequest, error) {
	return nil, nil
}
func (noopBeads) CreateProcessingCycle(context.Context, string, string, bool) (string, error) {
	return "", nil
}
func (noopBeads) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (noopBeads) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (noopBeads) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return nil, nil
}
func (noopBeads) CreateFeedback(context.Context, beads.CreateFeedbackInput) (string, error) {
	return "", nil
}
func (noopBeads) MarkFeedbackResolvedUpstream(context.Context, string) error { return nil }
func (noopBeads) ListFeedback(context.Context, string, bool) ([]beads.Feedback, error) {
	return nil, nil
}
func (noopBeads) FindFeedbackByFingerprint(context.Context, string, string) (*beads.Feedback, error) {
	return nil, nil
}
func (noopBeads) CloseFeedback(context.Context, string, string) error { return nil }
func (noopBeads) ListFeedbackPendingReply(context.Context) ([]beads.Feedback, error) {
	return nil, nil
}
func (noopBeads) SetResponseID(context.Context, string, string) error { return nil }
func (noopBeads) FindMergeRequestForFeedback(context.Context, string) (*beads.MergeRequest, error) {
	return nil, nil
}

func TestSync_ProgressesEvenIfStateSaveFails(t *testing.T) {
	// Exercise the state-save error path by pointing StateDir at a file
	// where MkdirAll will fail (a regular file with .ext acting as parent).
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/x")}

	bd := newRealBDClient(t)
	parent := t.TempDir()
	blockerFile := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blockerFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// StateDir is the blocker file (not a directory). MkdirAll on
	// blocker/repo-state.json's parent will fail because blocker is a file.
	stateDir := filepath.Join(blockerFile, "subdir")
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
		Now:      func() time.Time { return time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	sum, err := e.Sync(ctx)
	if err == nil {
		t.Fatalf("expected aggregate error due to state-save failure")
	}
	// BeadsCreated should still reflect work done.
	if sum.BeadsCreated != 1 {
		t.Fatalf("BeadsCreated: %d", sum.BeadsCreated)
	}
}

// ----------------------------------------------------------------------
// Reply pipeline (Phase 6 B3)
// ----------------------------------------------------------------------

// seedFeedback creates a (merge-request, processing-cycle, feedback) chain
// directly in the bd workspace. Returns the feedback bead id. Used to
// pre-populate state for reply-pipeline tests.
func seedFeedback(t *testing.T, bd *beads.Client, repo string, pr int, kind beads.FeedbackKind, externalID string) string {
	t.Helper()
	ctx := context.Background()
	prID, _, err := bd.EnsureMergeRequest(ctx, "", beads.MergeRequestFields{Repo: repo, PRNumber: pr})
	if err != nil {
		t.Fatalf("seed MR: %v", err)
	}
	cycleID, err := bd.CreateProcessingCycle(ctx, prID, repo+"#seed", false)
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	fbID, err := bd.CreateFeedback(ctx, beads.CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              kind,
		ExternalID:        externalID,
		Fingerprint:       "fp-" + externalID,
		Title:             "seeded",
	})
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	return fbID
}

func TestSync_PostsQueuedReplyAndStoresResponseID(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	// PR must be in the enumerate set so the repo is "healthy".
	vcs.my["foo/bar"] = []api.PR{samplePR(42, "foo/bar", "feat/r")}
	vcs.replyResp = &api.Comment{ID: "C_RESP_123", Author: "phillipg"}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Seed a feedback bead under foo/bar#42 with a queued reply.
	fbID := seedFeedback(t, bd, "foo/bar", 42, beads.FeedbackKindCommentThread, "THREAD_ABC")
	if err := bd.SetReplyDraft(ctx, fbID, "thanks, fixed in deadbee"); err != nil {
		t.Fatalf("SetReplyDraft: %v", err)
	}

	// Sync: should post the reply and record response_id.
	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v\nErrors: %+v", err, sum.Errors)
	}
	if sum.RepliesPosted != 1 {
		t.Fatalf("RepliesPosted: got %d want 1 (errors: %+v)", sum.RepliesPosted, sum.Errors)
	}
	if len(vcs.replyCalls) != 1 {
		t.Fatalf("expected 1 ReplyToThread call, got %d: %+v", len(vcs.replyCalls), vcs.replyCalls)
	}
	got := vcs.replyCalls[0]
	if got.Repo != "foo/bar" || got.ThreadID != "THREAD_ABC" || got.Body != "thanks, fixed in deadbee" {
		t.Fatalf("ReplyToThread args: got %+v", got)
	}
	respID, err := bd.GetResponseID(ctx, fbID)
	if err != nil {
		t.Fatalf("GetResponseID: %v", err)
	}
	if respID != "C_RESP_123" {
		t.Fatalf("response_id: got %q want C_RESP_123", respID)
	}

	// Second sync: with response_id set, must NOT call ReplyToThread again.
	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(vcs.replyCalls) != 1 {
		t.Fatalf("expected no further ReplyToThread calls; got %d total", len(vcs.replyCalls))
	}
}

func TestSync_ReplyToThreadErrorLeavesResponseIDEmpty(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(50, "foo/bar", "feat/r2")}
	vcs.replyRespErr = errors.New("upstream is down")

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, _ := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})

	fbID := seedFeedback(t, bd, "foo/bar", 50, beads.FeedbackKindReviewThread, "PRRT_err")
	if err := bd.SetReplyDraft(ctx, fbID, "queued"); err != nil {
		t.Fatalf("SetReplyDraft: %v", err)
	}

	sum, err := e.Sync(ctx)
	// Sync returns an aggregate error since the reply pass recorded one,
	// but the test cares about the per-bead behavior.
	if err == nil {
		t.Fatalf("expected aggregate error from VCS failure")
	}
	if sum.RepliesPosted != 0 {
		t.Fatalf("RepliesPosted: got %d want 0", sum.RepliesPosted)
	}
	respID, err := bd.GetResponseID(ctx, fbID)
	if err != nil {
		t.Fatalf("GetResponseID: %v", err)
	}
	if respID != "" {
		t.Fatalf("response_id should remain empty on VCS error, got %q", respID)
	}
	// Next sync (after fixing the VCS) would retry — confirm at least one
	// reply call was attempted this round.
	if len(vcs.replyCalls) != 1 {
		t.Fatalf("expected 1 ReplyToThread attempt, got %d", len(vcs.replyCalls))
	}
}

func TestSync_SkipsNonReplyableFeedbackKind(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(60, "foo/bar", "feat/r3")}
	vcs.replyResp = &api.Comment{ID: "should_not_be_used"}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, _ := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})

	// ci-failure cannot be replied to.
	fbID := seedFeedback(t, bd, "foo/bar", 60, beads.FeedbackKindCIFailure, "CI_RUN_99")
	if err := bd.SetReplyDraft(ctx, fbID, "queued reply"); err != nil {
		t.Fatalf("SetReplyDraft: %v", err)
	}

	sum, err := e.Sync(ctx)
	// One per-bead skip is logged as a SummaryError — Sync returns aggregate error.
	if err == nil {
		t.Fatalf("expected aggregate error from skip")
	}
	if sum.RepliesPosted != 0 {
		t.Fatalf("RepliesPosted: got %d want 0", sum.RepliesPosted)
	}
	if len(vcs.replyCalls) != 0 {
		t.Fatalf("expected no ReplyToThread calls on non-reply-able kind, got %d", len(vcs.replyCalls))
	}
	respID, err := bd.GetResponseID(ctx, fbID)
	if err != nil {
		t.Fatalf("GetResponseID: %v", err)
	}
	if respID != "" {
		t.Fatalf("response_id should never be set for ci-failure, got %q", respID)
	}
	// Verify the skip reason landed in Summary.Errors.
	foundSkip := false
	for _, e := range sum.Errors {
		if strings.Contains(e.Message, "cannot reply to ci-failure") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("expected 'cannot reply to ci-failure' in errors, got %+v", sum.Errors)
	}
}

func TestSync_SkipsReplyForFeedbackOfDifferentRepo(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	// The configured repo is foo/bar (per minimalCfg).
	vcs.my["foo/bar"] = []api.PR{samplePR(70, "foo/bar", "feat/r4")}
	vcs.replyResp = &api.Comment{ID: "should_not_be_used"}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, _ := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})

	// Seed a feedback bead under a DIFFERENT repo (other/repo).
	fbID := seedFeedback(t, bd, "other/repo", 7, beads.FeedbackKindCommentThread, "TH_OTHER")
	if err := bd.SetReplyDraft(ctx, fbID, "should be skipped for foo/bar's sync"); err != nil {
		t.Fatalf("SetReplyDraft: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v\nErrors: %+v", err, sum.Errors)
	}
	if sum.RepliesPosted != 0 {
		t.Fatalf("RepliesPosted: got %d want 0", sum.RepliesPosted)
	}
	if len(vcs.replyCalls) != 0 {
		t.Fatalf("expected no ReplyToThread calls for cross-repo feedback, got %d", len(vcs.replyCalls))
	}
	respID, err := bd.GetResponseID(ctx, fbID)
	if err != nil {
		t.Fatalf("GetResponseID: %v", err)
	}
	if respID != "" {
		t.Fatalf("response_id should be untouched, got %q", respID)
	}
}

// TestSync_PopulatesSnapshot verifies that when Deps.Snapshot is set, the
// sync loop writes a non-nil snapshot containing the observed PRs and
// classifies them by author. The test uses an in-process noopBeads so the
// bd-workspace integration hang is avoided; the BeadsDeps walk requires
// *beads.Client and is therefore skipped (empty BeadsDeps → WaitingOnMe is
// false, per builder spec).
func TestSync_PopulatesSnapshot(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	// One PR authored by the configured self (phillipg) → routed to Mine.
	vcs.my["foo/bar"] = []api.PR{samplePR(42, "foo/bar", "feat/dash")}

	store := snapshot.NewStore()
	reg, err := agentregistry.New(nil)
	if err != nil {
		t.Fatalf("agentregistry.New: %v", err)
	}
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:           minimalCfg(),
		VCS:           map[string]VCSProvider{"github": vcs},
		Beads:         &noopBeads{},
		StateDir:      stateDir,
		Snapshot:      store,
		AgentRegistry: reg,
		SyncInterval:  10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := e.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	snap, ok := store.Get()
	if !ok || snap == nil {
		t.Fatal("expected snapshot to be present after Sync")
	}
	if snap.SyncIntervalSeconds != int((10 * time.Minute).Seconds()) {
		t.Errorf("SyncIntervalSeconds: got %d want %d", snap.SyncIntervalSeconds, int((10 * time.Minute).Seconds()))
	}
	if len(snap.Mine) != 1 {
		t.Fatalf("Mine: got %d row(s) want 1; snap=%+v", len(snap.Mine), snap)
	}
	row := snap.Mine[0]
	if row.Number != 42 {
		t.Errorf("Mine[0].Number: got %d want 42", row.Number)
	}
	if row.Repo != "foo/bar" {
		t.Errorf("Mine[0].Repo: got %q want foo/bar", row.Repo)
	}
	if row.WaitingOnMe {
		t.Errorf("Mine[0].WaitingOnMe: empty BeadsDeps must be false")
	}
	if len(snap.Team) != 0 {
		t.Errorf("Team: got %d row(s) want 0", len(snap.Team))
	}
}

func TestSyncPR_SkipsDraftPromoteForTeammate(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Team-mate's PR — draft, CI green. Without the guard, sync would
	// SetDraft(false).
	pr := teammatePR(99, "foo/bar", "feat/coworker")
	vcs.views[keyOf("foo/bar", 99)] = pr
	ci.runs[keyOf("foo/bar", 99)] = []api.CIRun{successRun()}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.SyncPR(ctx, "foo/bar", 99)
	if err != nil {
		t.Fatalf("SyncPR: %v (errors=%+v)", err, sum.Errors)
	}
	if len(vcs.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls for team-mate PR; got %+v", vcs.setDraftCalls)
	}
	// Bead is still upserted with Author=coworker.
	if sum.BeadsCreated+sum.BeadsUpdated != 1 {
		t.Fatalf("expected 1 bead upserted; got created=%d updated=%d",
			sum.BeadsCreated, sum.BeadsUpdated)
	}
	if sum.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", sum.DraftPromoted)
	}
}

func TestIsSelfAuthored(t *testing.T) {
	cases := []struct {
		name   string
		self   string
		author string
		want   bool
	}{
		{"matches", "phillipg", "phillipg", true},
		{"different login", "phillipg", "coworker", false},
		{"empty author", "phillipg", "", false},
		{"empty self", "", "phillipg", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{deps: Deps{Cfg: &config.Config{SelfLogin: tc.self}}}
			got := e.isSelfAuthored(tc.author)
			if got != tc.want {
				t.Fatalf("isSelfAuthored(%q) with self=%q: got %v want %v",
					tc.author, tc.self, got, tc.want)
			}
		})
	}
}

func TestSync_OnlyPromotesDraftForSelfAuthoredPRs(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Mixed pool: one self draft+green, one team draft+green.
	selfPR := selfDraftPR(10, "foo/bar", "feat/mine")
	teamPR := teammatePR(20, "foo/bar", "feat/theirs")
	vcs.my["foo/bar"] = []api.PR{selfPR}
	vcs.team["foo/bar"] = []api.PR{teamPR}
	ci.runs[keyOf("foo/bar", 10)] = []api.CIRun{successRun()}
	ci.runs[keyOf("foo/bar", 20)] = []api.CIRun{successRun()}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	// Exactly one SetDraft, and it must be for the self PR (#10).
	if len(vcs.setDraftCalls) != 1 {
		t.Fatalf("expected 1 SetDraft call; got %d: %+v",
			len(vcs.setDraftCalls), vcs.setDraftCalls)
	}
	got := vcs.setDraftCalls[0]
	if got.Number != 10 || got.Draft != false {
		t.Fatalf("expected SetDraft(repo, 10, false); got %+v", got)
	}

	// Both beads must be upserted.
	if sum.BeadsCreated != 2 {
		t.Fatalf("BeadsCreated: got %d want 2", sum.BeadsCreated)
	}
}

func TestSummary_WarningsJSONRoundTrip(t *testing.T) {
	s := Summary{
		Warnings: []SummaryError{
			{Repo: "foo/bar", Message: "example warning"},
		},
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"warnings"`) {
		t.Fatalf("expected warnings key in JSON; got %s", raw)
	}

	// Round-trip back.
	var got Summary
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Message != "example warning" {
		t.Fatalf("round-trip lost warnings: %+v", got.Warnings)
	}

	// Empty warnings should omit the key (omitempty semantics).
	empty, err := json.Marshal(Summary{})
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	if strings.Contains(string(empty), `"warnings"`) {
		t.Fatalf("empty Warnings should be omitted; got %s", empty)
	}
}

func TestSync_SkipsAndWarnsOnTeammateReplyDraft(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	// Both PRs need to be in the enumerate set so the repo is healthy.
	vcs.my["foo/bar"] = []api.PR{samplePR(42, "foo/bar", "feat/mine")}
	vcs.team["foo/bar"] = []api.PR{teammatePR(99, "foo/bar", "feat/theirs")}
	vcs.replyResp = &api.Comment{ID: "C_SELF_RESP", Author: "phillipg"}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Seed two MR beads with explicit Author fields and a queued reply
	// on each. EnsureMergeRequest is idempotent on URL — by the time
	// Sync runs, it'll find these existing beads and update the upstream
	// fields onto them.
	selfMRID, _, err := bd.EnsureMergeRequest(ctx,
		"https://github.com/foo/bar/pull/42",
		beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 42, Author: "phillipg"})
	if err != nil {
		t.Fatalf("seed self MR: %v", err)
	}
	teamMRID, _, err := bd.EnsureMergeRequest(ctx,
		"https://github.com/foo/bar/pull/99",
		beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 99, Author: "coworker"})
	if err != nil {
		t.Fatalf("seed team MR: %v", err)
	}

	selfCycle, err := bd.CreateProcessingCycle(ctx, selfMRID, "foo/bar#self-seed", false)
	if err != nil {
		t.Fatalf("self cycle: %v", err)
	}
	teamCycle, err := bd.CreateProcessingCycle(ctx, teamMRID, "foo/bar#team-seed", false)
	if err != nil {
		t.Fatalf("team cycle: %v", err)
	}

	selfFB, err := bd.CreateFeedback(ctx, beads.CreateFeedbackInput{
		ProcessingCycleID: selfCycle, Kind: beads.FeedbackKindCommentThread,
		ExternalID: "TH_SELF", Fingerprint: "fp-self", Title: "self",
	})
	if err != nil {
		t.Fatalf("self feedback: %v", err)
	}
	teamFB, err := bd.CreateFeedback(ctx, beads.CreateFeedbackInput{
		ProcessingCycleID: teamCycle, Kind: beads.FeedbackKindCommentThread,
		ExternalID: "TH_TEAM", Fingerprint: "fp-team", Title: "team",
	})
	if err != nil {
		t.Fatalf("team feedback: %v", err)
	}

	if err := bd.SetReplyDraft(ctx, selfFB, "self reply"); err != nil {
		t.Fatalf("SetReplyDraft self: %v", err)
	}
	if err := bd.SetReplyDraft(ctx, teamFB, "team reply — should NOT post"); err != nil {
		t.Fatalf("SetReplyDraft team: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	// Exactly one ReplyToThread call — the self one.
	if len(vcs.replyCalls) != 1 {
		t.Fatalf("expected 1 ReplyToThread call; got %d: %+v",
			len(vcs.replyCalls), vcs.replyCalls)
	}
	if vcs.replyCalls[0].ThreadID != "TH_SELF" {
		t.Fatalf("expected reply to TH_SELF; got %+v", vcs.replyCalls[0])
	}
	if sum.RepliesPosted != 1 {
		t.Fatalf("RepliesPosted: got %d want 1 (errors=%+v)", sum.RepliesPosted, sum.Errors)
	}

	// Self bead got its response_id.
	selfRespID, err := bd.GetResponseID(ctx, selfFB)
	if err != nil {
		t.Fatalf("GetResponseID self: %v", err)
	}
	if selfRespID != "C_SELF_RESP" {
		t.Fatalf("self response_id: got %q want C_SELF_RESP", selfRespID)
	}

	// Team bead untouched: ReplyDraft unchanged, response_id empty.
	teamRespID, err := bd.GetResponseID(ctx, teamFB)
	if err != nil {
		t.Fatalf("GetResponseID team: %v", err)
	}
	if teamRespID != "" {
		t.Fatalf("team response_id should be empty; got %q", teamRespID)
	}
	teamDraft, err := bd.GetReplyDraft(ctx, teamFB)
	if err != nil {
		t.Fatalf("GetReplyDraft team: %v", err)
	}
	if teamDraft != "team reply — should NOT post" {
		t.Fatalf("team ReplyDraft should be preserved; got %q", teamDraft)
	}

	// Exactly one warning, referencing the team feedback bead.
	if len(sum.Warnings) != 1 {
		t.Fatalf("expected 1 Warning; got %d: %+v", len(sum.Warnings), sum.Warnings)
	}
	w := sum.Warnings[0]
	if w.Repo != "foo/bar" {
		t.Fatalf("warning Repo: %q", w.Repo)
	}
	if !strings.Contains(w.Message, teamFB) {
		t.Fatalf("warning Message should reference team feedback id %q; got %q", teamFB, w.Message)
	}
	if !strings.Contains(w.Message, "coworker") {
		t.Fatalf("warning Message should mention author %q; got %q", "coworker", w.Message)
	}
}

func TestSync_TreatsEmptySelfLoginAsTeammate(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Both PRs draft+green. With empty SelfLogin, neither should be promoted.
	pr1 := samplePR(1, "foo/bar", "feat/a")
	pr1.Draft = true
	pr2 := samplePR(2, "foo/bar", "feat/b")
	pr2.Draft = true
	vcs.my["foo/bar"] = []api.PR{pr1, pr2}
	ci.runs[keyOf("foo/bar", 1)] = []api.CIRun{successRun()}
	ci.runs[keyOf("foo/bar", 2)] = []api.CIRun{successRun()}

	cfg := cfgWithCICD()
	cfg.SelfLogin = "" // simulate misconfiguration

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}
	if len(vcs.setDraftCalls) != 0 {
		t.Fatalf("expected NO SetDraft calls when SelfLogin is empty; got %+v",
			vcs.setDraftCalls)
	}
	// Beads still upserted.
	if sum.BeadsCreated != 2 {
		t.Fatalf("BeadsCreated: got %d want 2", sum.BeadsCreated)
	}
}

func TestBuildEnrichedSearchQuery(t *testing.T) {
	cases := []struct {
		name string
		repo string
		self string
		team []string
		want string
	}{
		{
			name: "self only",
			repo: "owner/repo",
			self: "alice",
			team: nil,
			want: "is:pr is:open repo:owner/repo author:alice",
		},
		{
			name: "self plus team",
			repo: "x/y",
			self: "alice",
			team: []string{"bob", "carol"},
			want: "is:pr is:open repo:x/y author:alice author:bob author:carol",
		},
		{
			name: "team only (empty self)",
			repo: "x/y",
			self: "",
			team: []string{"bob"},
			want: "is:pr is:open repo:x/y author:bob",
		},
		{
			name: "dedup self in team list",
			repo: "x/y",
			self: "alice",
			team: []string{"alice", "bob"},
			want: "is:pr is:open repo:x/y author:alice author:bob",
		},
		{
			name: "blank entries ignored",
			repo: "x/y",
			self: "  ",
			team: []string{"", "bob", "  "},
			want: "is:pr is:open repo:x/y author:bob",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildEnrichedSearchQuery(tc.repo, tc.self, tc.team)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripCodeRabbitInternalState(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no markers leaves body unchanged",
			in:   "regular review comment",
			want: "regular review comment",
		},
		{
			name: "wraps the internal-state block with elision marker",
			in:   "## Walkthrough\nstuff\n<!-- internal state start -->\nLOTS\nOF\nBASE64\n<!-- internal state end -->\nafter",
			want: "## Walkthrough\nstuff\n[CodeRabbit internal state elided]\nafter",
		},
		{
			name: "unmatched start marker leaves body untouched",
			in:   "before <!-- internal state start --> still has start no end",
			want: "before <!-- internal state start --> still has start no end",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripCodeRabbitInternalState(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommentEvent_StripsInternalStateBeforeStorage(t *testing.T) {
	// CodeRabbit walkthrough comments embed a ~120KB base64 internal-
	// state block that overflows bd's description column. commentEvent
	// must strip it so the bead's body fits and so the title (first
	// line of body) is a human-readable summary line.
	c := api.Comment{
		Author: "coderabbitai[bot]",
		Body:   "## Walkthrough\nThe human summary.\n<!-- internal state start -->\n" + strings.Repeat("x", 200_000) + "\n<!-- internal state end -->\nafter",
	}
	ev := commentEvent(c)
	if strings.Contains(ev.body, "internal state start") {
		t.Errorf("event body still contains the internal-state marker — strip missed")
	}
	if !strings.Contains(ev.body, "## Walkthrough") {
		t.Errorf("event body lost the human-readable preamble: %q", ev.body[0:200])
	}
	if !strings.Contains(ev.body, "[CodeRabbit internal state elided]") {
		t.Errorf("event body missing the elision marker")
	}
	if len(ev.body) > 65_535 {
		t.Errorf("event body still exceeds bd's TEXT column cap (%d bytes)", len(ev.body))
	}
	if !strings.HasPrefix(ev.title, "## Walkthrough") {
		t.Errorf("title should be derived from human-readable first line; got %q", ev.title)
	}
}
