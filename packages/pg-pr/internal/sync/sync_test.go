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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
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

// ----------------------------------------------------------------------
// Test bd workspace
// ----------------------------------------------------------------------

var bdCounter int64

func newRealBDClient(t *testing.T) *beads.Client {
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
	runner := &beads.CLIRunner{Dir: dir, Env: env}
	return beads.NewClientWithRunner(runner)
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
	_, err = New(Deps{Cfg: minimalCfg()})
	if err == nil {
		t.Fatalf("expected error for missing beads client")
	}
	_, err = New(Deps{Cfg: minimalCfg(), Beads: &noopBeads{}})
	if err == nil {
		t.Fatalf("expected error for missing VCS")
	}
}

// noopBeads is a do-nothing BeadClient used only for New validation tests.
type noopBeads struct{}

func (noopBeads) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "", false, nil
}
func (noopBeads) CloseMergeRequest(context.Context, string, string) error { return nil }
func (noopBeads) ListMergeRequests(context.Context, bool) ([]beads.MergeRequest, error) {
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
