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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
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

	// SetDraft recording. The engine type-asserts to DraftToggler; with
	// setDraftCalls non-nil, the assertion succeeds via the method below.
	setDraftCalls []setDraftCall
	setDraftErr   error
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
	// Event-ownership refactor: the PR (merge-request) bead is now projected
	// by the beadsbridge handler at outbox flush, not created inline. Wire a
	// real store + a per-repo routing bridge (mirrors production cmd wiring) so
	// the bead still lands in the engine's bd workspace and the workspace-based
	// assertions (BeadsClosed, idempotent re-run, isolation) keep holding.
	wireOutboxBridge(t, e)
	return e
}

// wireOutboxBridge attaches a fresh store.DB + a per-repo routing bridge
// dispatcher onto the engine, mirroring the production wiring in
// cmd/pg-pr/sync.go (newBeadsBridgeHandler). Each event is routed to the bd
// client for its repo: the shared Deps.Beads when set, else a per-repo
// NewClientForRepo(path). Errors are swallowed by the dispatcher (as in prod).
func wireOutboxBridge(t *testing.T, e *Engine) {
	t.Helper()
	db := store.OpenForTest(t)
	disp := event.New()
	disp.Register(func(ctx context.Context, ev store.Event) error {
		var head struct {
			Repo string `json:"repo"`
		}
		if err := json.Unmarshal(ev.Payload, &head); err != nil || head.Repo == "" {
			return nil
		}
		var client beadsbridge.BeadClient
		if e.deps.Beads != nil {
			if bc, ok := e.deps.Beads.(beadsbridge.BeadClient); ok {
				client = bc
			}
		}
		if client == nil {
			for _, r := range e.cfg().Repos {
				if r.Remote == head.Repo && r.Path != "" {
					client = beads.NewClientForRepo(r.Path)
					break
				}
			}
		}
		if client == nil {
			return nil
		}
		return beadsbridge.New(client).Handle(ctx, ev)
	})
	e.SetStoreAndDispatch(db, disp.Dispatch)
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
	// Event-ownership refactor: the per-repo bead is projected by the bridge at
	// outbox flush, routed to each repo's own .beads/ workspace by Path
	// (mirrors production newBeadsBridgeHandler). wireOutboxBridge handles the
	// Deps.Beads==nil + per-repo Path case.
	wireOutboxBridge(t, e)

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
func (noopBeads) CloseFeedback(context.Context, string, string) error { return nil }

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

// inlineGuardBeads is the engine's per-repo bd client for the
// outbox-projection test. It embeds noopBeads (which returns empty/nil for
// every method) and overrides EnsureMergeRequest to RECORD + FAIL — proving
// the inline create path is gone. ListMergeRequests is left as noopBeads's
// (nil, nil) so listExistingByKey and the close-detection loop still work.
type inlineGuardBeads struct {
	noopBeads
	ensureCalled int
}

func (g *inlineGuardBeads) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	g.ensureCalled++
	return "", false, errors.New("inlineGuardBeads: EnsureMergeRequest must NOT be called inline; the PR bead is projected via the outbox bridge")
}

// TestSyncCreatesBeadViaOutbox proves Task 6's core invariant: the one-shot
// Engine.Sync per-PR path no longer creates the PR (merge-request) bead inline.
// Instead it emits a pr.opened event whose outbox flush drives the beadsbridge
// handler's EnsureMergeRequest. We assert:
//   - the engine's own per-repo bd client (inlineGuardBeads) is NEVER asked to
//     EnsureMergeRequest (the inline path is removed),
//   - the bridge's bd client DID receive EnsureMergeRequest with full PR fields
//     during flushOutbox,
//   - summary.BeadsCreated == 1.
func TestSyncCreatesBeadViaOutbox(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	vcs := newFakeVCS()
	pr := samplePR(7, "foo/bar", "feat/outbox")
	pr.Title = "outbox-projected PR"
	pr.HeadSHA = "sha-outbox"
	vcs.my["foo/bar"] = []api.PR{pr}

	guard := &inlineGuardBeads{}

	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    guard,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Wire the REAL bridge over a fake bd client behind the REAL dispatcher.
	// FindByRepoAndNumber returns nil so ensureProcessFeedbackBead (if a
	// feedback.created event ever fires) is a no-op; here there is no feedback.
	bridgeClient := &fullChainBeadClient{}
	dispatcher := event.New()
	dispatcher.Register(beadsbridge.New(bridgeClient).Handle)
	e.SetStoreAndDispatch(db, dispatcher.Dispatch)

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	if guard.ensureCalled != 0 {
		t.Fatalf("inline EnsureMergeRequest must not be called; got %d call(s)", guard.ensureCalled)
	}
	if len(bridgeClient.ensureCalls) != 1 {
		t.Fatalf("expected bridge EnsureMergeRequest called once via outbox; got %d", len(bridgeClient.ensureCalls))
	}
	got := bridgeClient.ensureCalls[0]
	if got.Repo != "foo/bar" || got.PRNumber != 7 {
		t.Fatalf("bridge EnsureMergeRequest fields: got %+v want foo/bar#7", got)
	}
	if got.Branch != "feat/outbox" || got.URL == "" || got.Author != "phillipg" {
		t.Fatalf("bridge EnsureMergeRequest fields incomplete: %+v", got)
	}
	if sum.BeadsCreated != 1 {
		t.Fatalf("BeadsCreated: got %d want 1", sum.BeadsCreated)
	}

	// And the authoritative store row was written for the observed PR
	// (drives Task 8 close-detection via ListOpenPRs).
	open, err := db.ListOpenPRs(ctx, "foo/bar")
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if len(open) != 1 || open[0].Number != 7 {
		t.Fatalf("expected one open store PR row for foo/bar#7; got %+v", open)
	}
}

// TestMaybePromoteDraftEmitsUpdate verifies that when a self-authored draft PR
// has all CI runs green, maybePromoteDraft (called via Sync) emits a
// store.EventPRUpdated event whose payload has State=="open" and Draft==false.
// This is the Task 7 contract: bead-state projection for draft-promote arrives
// via the bridge (event-driven), not via an inline bead write.
func TestMaybePromoteDraftEmitsUpdate(t *testing.T) {
	ctx := context.Background()

	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Self-authored draft PR with all CI green — conditions for draft promotion.
	pr := selfDraftPR(55, "foo/bar", "feat/draft-promote")
	pr.State = "open"
	vcs.my["foo/bar"] = []api.PR{pr}
	ci.runs[keyOf("foo/bar", 55)] = []api.CIRun{successRun()}

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)

	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Do NOT wire the bridge dispatcher — we want to inspect the raw outbox
	// rows, not have them consumed by the bridge handler.

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	// SetDraft must have fired (pre-existing behaviour).
	if len(vcs.setDraftCalls) != 1 {
		t.Fatalf("expected 1 SetDraft call; got %d: %+v", len(vcs.setDraftCalls), vcs.setDraftCalls)
	}
	if sum.DraftPromoted != 1 {
		t.Fatalf("DraftPromoted: got %d want 1", sum.DraftPromoted)
	}

	// Drain the outbox and find the pr.updated event emitted by draft-promote.
	var events []store.Event
	if err := db.RunOutbox(ctx, func(_ context.Context, ev store.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}

	// There may be multiple events (e.g. pr.opened from the initial upsert path).
	// We need at least one pr.updated with State=="open" and Draft==false.
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventPRUpdated {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.State == "open" && !p.Draft {
			found = true
			// Also verify repo/number match.
			if p.Repo != "foo/bar" || p.Number != 55 {
				t.Errorf("pr.updated payload repo/number mismatch: got %s#%d want foo/bar#55", p.Repo, p.Number)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected a pr.updated event with State=open Draft=false in outbox; events: %+v", events)
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
