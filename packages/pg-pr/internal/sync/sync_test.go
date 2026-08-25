package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// enricherVCS embeds fakeVCS and adds the SinglePREnricher capability so
// enrichOnePR routing can be exercised.
type enricherVCS struct {
	fakeVCS
	ep        *vcs.EnrichedPR
	enrichErr error
	called    bool
}

func (e *enricherVCS) EnrichPR(_ context.Context, _ string, _ int) (*vcs.EnrichedPR, error) {
	e.called = true
	return e.ep, e.enrichErr
}

func TestEnrichOnePR_PrefersGraphQL(t *testing.T) {
	vp := &enricherVCS{ep: &vcs.EnrichedPR{
		Comments:  []api.Comment{{ID: "PRRC_1", ThreadID: "PRRT_abc", Path: "x.go", CreatedAt: "2026-06-03T09:00:00Z"}},
		Truncated: []string{"reviewThreads"}, // non-empty must NOT trigger REST fallback
	}}
	e := &Engine{deps: Deps{VCS: map[string]VCSProvider{"github": vp}}}
	got := e.enrichOnePR(context.Background(), config.RepoConfig{Remote: "o/r", VCS: "github"}, api.PR{Repo: "o/r", Number: 42})
	if !vp.called {
		t.Fatal("expected EnrichPR (GraphQL) to be used")
	}
	if len(got.Comments) != 1 || got.Comments[0].ThreadID != "PRRT_abc" {
		t.Errorf("expected GraphQL comments with PRRT thread id, got %+v", got.Comments)
	}
	if got.PR.Number != 42 {
		t.Errorf("observed PR state should be preserved, got %+v", got.PR)
	}
}

// TestEnrichOnePR_CarriesMergeability guards against the pg2-dwfld daemon gap:
// GraphQL is the only source of Mergeable/MergeStateStatus/AutoMergeEnabled
// (the REST GetPR path used by refreshPR leaves them empty), so enrichOnePR
// must carry those three fields from ep.PR onto the returned out.PR even
// though out.PR otherwise starts from the REST-observed pr.
func TestEnrichOnePR_CarriesMergeability(t *testing.T) {
	vp := &enricherVCS{ep: &vcs.EnrichedPR{
		PR: api.PR{
			Mergeable:        "MERGEABLE",
			MergeStateStatus: "CLEAN",
			AutoMergeEnabled: true,
		},
	}}
	e := &Engine{deps: Deps{VCS: map[string]VCSProvider{"github": vp}}}
	got := e.enrichOnePR(context.Background(), config.RepoConfig{Remote: "o/r", VCS: "github"}, api.PR{Repo: "o/r", Number: 42})
	if got.PR.MergeStateStatus != "CLEAN" {
		t.Errorf("expected MergeStateStatus to be carried from GraphQL, got %q", got.PR.MergeStateStatus)
	}
	if !got.PR.AutoMergeEnabled {
		t.Error("expected AutoMergeEnabled to be carried from GraphQL, got false")
	}
	if got.PR.Mergeable != "MERGEABLE" {
		t.Errorf("expected Mergeable to be carried from GraphQL, got %q", got.PR.Mergeable)
	}
	if got.PR.Number != 42 {
		t.Errorf("observed REST PR fields should be preserved, got %+v", got.PR)
	}
}

func TestEnrichOnePR_FallsBackOnError(t *testing.T) {
	vp := &enricherVCS{enrichErr: errors.New("graphql boom")}
	e := &Engine{deps: Deps{VCS: map[string]VCSProvider{"github": vp}}}
	got := e.enrichOnePR(context.Background(), config.RepoConfig{Remote: "o/r", VCS: "github"}, api.PR{Repo: "o/r", Number: 42})
	if !vp.called {
		t.Fatal("expected EnrichPR to be attempted")
	}
	if got == nil || got.PR.Number != 42 {
		t.Fatalf("REST fallback should still return the PR, got %+v", got)
	}
}

func TestEnrichOnePR_CIAlwaysFromCICDProvider(t *testing.T) {
	// Even when single-PR GraphQL succeeds (and carries its own statusCheckRollup
	// CIRuns), CI must come from the dedicated CICD provider so large PRs keep
	// complete CI (GraphQL statusCheckRollup caps at 30 contexts).
	vp := &enricherVCS{ep: &vcs.EnrichedPR{
		Comments: []api.Comment{{ID: "PRRC_1", ThreadID: "PRRT_abc", Path: "x.go"}},
		CIRuns:   []api.CIRun{{Name: "from-graphql"}},
	}}
	cicd := newFakeCICD()
	cicd.runs[keyOf("o/r", 42)] = []api.CIRun{{Name: "from-cicd"}}
	e := &Engine{deps: Deps{
		VCS:  map[string]VCSProvider{"github": vp},
		CICD: map[string]CICDProvider{"gh-actions": cicd},
	}}
	got := e.enrichOnePR(context.Background(),
		config.RepoConfig{Remote: "o/r", VCS: "github", CICD: []string{"gh-actions"}},
		api.PR{Repo: "o/r", Number: 42})
	if len(got.CIRuns) != 1 || got.CIRuns[0].Name != "from-cicd" {
		t.Errorf("CI must come from the CICD provider, got %+v", got.CIRuns)
	}
	if len(got.Comments) != 1 || got.Comments[0].ThreadID != "PRRT_abc" {
		t.Errorf("comments should still come from GraphQL, got %+v", got.Comments)
	}
}

func TestThreadBearingTruncations(t *testing.T) {
	got := threadBearingTruncations([]string{"ciContexts", "files", "reviewThreads", "comments", "labels"})
	if len(got) != 2 || got[0] != "reviewThreads" || got[1] != "comments" {
		t.Errorf("got %v, want [reviewThreads comments]", got)
	}
	if n := len(threadBearingTruncations([]string{"ciContexts", "files"})); n != 0 {
		t.Errorf("ciContexts/files alone should yield no thread-bearing truncations, got %d", n)
	}
}

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
// cirollup.Compute as "no runs" (state "none") → not promotable).
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
// cirollup.Compute requires (state "success") for draft promotion to fire.
func successRun() api.CIRun {
	return api.CIRun{Status: "completed", Conclusion: "success"}
}

// ----------------------------------------------------------------------
// Test bd workspace
//
// tc-8myb: this package has 11 call sites that each used to boot a FRESH real
// bd workspace via `bd init` (an embedded-dolt bootstrap measured at ~19s
// standalone on this host, and 3+ minutes under load), which alone could blow
// the package's 10-minute `go test` budget. `bd init`'s output has no
// per-test-varying content (the .beads/embeddeddolt tree and config are
// identical regardless of which test asked for it), so instead of paying that
// cost 11 times, we pay it ONCE into a package-scoped template directory
// (bdWorkspaceTemplate, guarded by sync.Once) and give each test its own
// workspace via a cheap recursive filesystem copy (copyDir). Every test still
// gets a fully independent, isolated directory exactly as before — the fix
// eliminates the repeated `bd init` cost, not the isolation.
//
// A true SHARED live workspace (one `bd` database used concurrently/serially
// by all 11 call sites) was considered and rejected: most of these tests hard
// -code the same repo ("foo/bar") and small PR numbers (1, 2, 3, 42, 55, ...),
// and EnsureMergeRequest/ListMergeRequests key off workspace-wide "repo#PR"
// identities — e.g. TestSyncSummaryCounts asserts BeadsCreated/BeadsUpdated
// counts that depend on exactly which merge-request beads already exist in
// the workspace before Sync runs. Sharing would make one test's leftover
// beads silently change another test's "pre-existing vs. new" classification.
// TestSync_PerRepoWorkspaceIsolation additionally requires TWO genuinely
// separate workspaces by design (it proves per-repo isolation), so it keeps
// calling newRealBDWorkspaceDir twice.
// ----------------------------------------------------------------------

var (
	bdTemplateOnce sync.Once
	bdTemplateDir  string
	bdTemplateErr  error
)

// bdWorkspaceTemplate lazily builds ONE real bd workspace (bd init + bd
// config set) under a package-scoped temp directory and returns its path.
// The build is guarded by sync.Once so it runs at most once per `go test`
// process regardless of how many tests call it. Each `bd` subprocess is
// bounded by a generous timeout (see realBDSetupTimeout) so a stuck `bd init`
// fails fast with a clear error instead of silently eating the whole package
// budget.
func bdWorkspaceTemplate(t *testing.T) string {
	t.Helper()
	bdTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pg-pr-sync-bd-template-*")
		if err != nil {
			bdTemplateErr = fmt.Errorf("create bd template dir: %w", err)
			return
		}
		env := cleanEnv()

		ctx, cancel := context.WithTimeout(context.Background(), realBDSetupTimeout)
		defer cancel()
		init := exec.CommandContext(ctx, "bd", "init", "--prefix", "synctest")
		init.Dir = dir
		init.Env = env
		if out, err := init.CombinedOutput(); err != nil {
			bdTemplateErr = fmt.Errorf("bd init (template): %w\n%s", err, out)
			return
		}

		cfgCtx, cfgCancel := context.WithTimeout(context.Background(), realBDSetupTimeout)
		defer cfgCancel()
		cfgSet := exec.CommandContext(cfgCtx, "bd", "config", "set", "types.custom", "merge-request,feedback")
		cfgSet.Dir = dir
		cfgSet.Env = env
		if out, err := cfgSet.CombinedOutput(); err != nil {
			bdTemplateErr = fmt.Errorf("bd config set (template): %w\n%s", err, out)
			return
		}

		bdTemplateDir = dir
	})
	if bdTemplateErr != nil {
		t.Fatalf("bd workspace template: %v", bdTemplateErr)
	}
	return bdTemplateDir
}

func newRealBDClient(t *testing.T) *beads.Client {
	t.Helper()
	dir, env := newRealBDWorkspaceDir(t)
	runner := &beads.CLIRunner{Dir: dir, Env: env}
	return beads.NewClientWithRunner(runner)
}

// newRealBDWorkspaceDir returns a fresh, independent bd workspace under
// t.TempDir() by copying the package-level template (see bdWorkspaceTemplate)
// rather than re-running `bd init`, and returns (dir, cleanEnv). Used by
// tests that need to construct per-repo bd clients via beads.NewClientForRepo
// against a real workspace.
func newRealBDWorkspaceDir(t *testing.T) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	template := bdWorkspaceTemplate(t)
	dir := t.TempDir()
	if err := copyDir(template, dir); err != nil {
		t.Fatalf("copy bd workspace template: %v", err)
	}
	return dir, cleanEnv()
}

// copyDir recursively copies the contents of src into dst. dst must already
// exist. Used to clone the bd workspace template (see bdWorkspaceTemplate)
// cheaply instead of re-running `bd init` per test.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "." {
				return nil // dst already exists (t.TempDir())
			}
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
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

// realBDSetupTimeout bounds the one-time bd init/config set subprocesses that
// build the package's shared bd workspace template (see bdWorkspaceTemplate).
// bd init measured ~19s standalone on this host, and 3+ minutes under heavy
// load in the tc-8myb incident; 5 minutes is generous enough to tolerate that
// load case while still failing fast (and diagnosably) well short of the
// package's 10-minute go test default if `bd` genuinely wedges.
const realBDSetupTimeout = 5 * time.Minute

// realBDCtx returns a context bounded by realBDOpTimeout for a test that
// drives a real (non-fake) bd-backed Engine. Production code (CLIRunner.Run)
// has no built-in timeout of its own — it relies entirely on the caller's
// context (see pkg/beads/runner.go) — which is correct for production
// callers but means a genuinely stuck `bd` subprocess during a test would
// otherwise silently consume the whole package's 10-minute test budget (the
// tc-8myb incident: a `ListChildrenOfPR` call stuck 3+ minutes under load).
// Using this context instead of context.Background() only affects these
// tests; it does not change CLIRunner.Run's default (timeout-less) behavior
// for real production callers.
func realBDCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), realBDOpTimeout)
	t.Cleanup(cancel)
	return ctx
}

// realBDOpTimeout bounds a single test's real-bd-backed Sync/SyncPR call.
// Chosen generously (see realBDSetupTimeout) so a legitimately slow-but-
// working `bd` subprocess under load does not make CI flakier.
const realBDOpTimeout = 5 * time.Minute

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
// dispatcher onto the engine. The per-repo NewClientForRepo(path) routing
// follows the production wiring in cmd/pg-pr/sync.go (newBeadsBridgeHandler),
// but this helper ALSO adds a test-only branch that prefers a shared
// Deps.Beads client when set — production has no such branch (it always routes
// by repo Path). That branch lets tests share one in-memory/real bd workspace
// across repos. Errors are swallowed by the dispatcher (as in prod).
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
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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

// TestSyncSummaryCounts exercises all three per-PR summary counters in a
// single one-shot Engine.Sync, proving they are mutually exclusive and
// correctly attributed:
//
//   - PR #1 is NEW (no pre-existing bead in the workspace) → pr.opened →
//     BeadsCreated.
//   - PR #2 is PRE-EXISTING (an open merge-request bead already lives in the
//     bd workspace at sync start, so repoPreExisting finds it) → pr.updated →
//     BeadsUpdated.
//   - PR #3 has DISAPPEARED (an open store row that is not in the observed
//     set) → pr.closed → BeadsClosed.
//
// It uses the makeEngine harness (real bd client via Deps.Beads + real store
// + routing bridge) so the pre-existing-bead index reflects the workspace.
func TestSyncSummaryCounts(t *testing.T) {
	ctx := realBDCtx(t)
	vcs := newFakeVCS()
	// PR #1 (new) and PR #2 (pre-existing) are both observed this tick.
	// PR #3 is intentionally NOT observed (it disappeared upstream).
	vcs.my["foo/bar"] = []api.PR{
		samplePR(1, "foo/bar", "feat/new"),
		samplePR(2, "foo/bar", "feat/existing"),
	}
	e := makeEngine(t, vcs)

	// Pre-existing: seed an OPEN merge-request bead for PR #2 directly in the
	// engine's bd workspace BEFORE Sync, so listExistingByKey records it in
	// repoPreExisting and the per-PR block classifies #2 as pr.updated.
	// Type-assert to *beads.Client: makeEngine uses newRealBDClient (concrete),
	// and sync.BeadClient is now {ListMergeRequests} — the seed call goes through
	// the concrete type's EnsureMergeRequest method, not the slim interface.
	seeder := e.deps.Beads.(*beads.Client)
	if _, _, err := seeder.EnsureMergeRequest(ctx, "foo/bar#2", beads.MergeRequestFields{
		Repo: "foo/bar", PRNumber: 2, State: "open", Branch: "feat/existing",
		Base: "main", Author: "phillipg",
	}); err != nil {
		t.Fatalf("seed pre-existing bead: %v", err)
	}

	// Disappeared: seed an OPEN store row for PR #3 that is NOT observed, so
	// the close-detection loop emits pr.closed and counts BeadsClosed.
	if _, err := e.deps.Store.UpsertPR(ctx, store.PullRequest{
		Repo: "foo/bar", Number: 3, Ownership: "mine", Author: "phillipg",
		State: "open", Branch: "feat/gone", Base: "main",
		URL: "https://github.com/foo/bar/pull/3",
	}); err != nil {
		t.Fatalf("seed disappeared store row: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}
	if sum.BeadsCreated != 1 {
		t.Fatalf("BeadsCreated: got %d want 1 (errors=%+v)", sum.BeadsCreated, sum.Errors)
	}
	if sum.BeadsUpdated != 1 {
		t.Fatalf("BeadsUpdated: got %d want 1 (errors=%+v)", sum.BeadsUpdated, sum.Errors)
	}
	if sum.BeadsClosed != 1 {
		t.Fatalf("BeadsClosed: got %d want 1 (errors=%+v)", sum.BeadsClosed, sum.Errors)
	}
}

// TestSyncSummaryCountsDraftPromoteNoDoubleCount proves a draft-promote does
// NOT double-count BeadsUpdated for a PR already counted by the per-PR block.
// maybePromoteDraft emits an extra pr.updated event, but the summary counter is
// bumped exactly once per PR by the per-PR block (the draft-promote emit is not
// counted), so a single newly-observed self-authored draft PR yields exactly
// one create and zero updates.
func TestSyncSummaryCountsDraftPromoteNoDoubleCount(t *testing.T) {
	ctx := realBDCtx(t)
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Self-authored draft PR with all CI green — draft-promote fires and emits
	// an extra pr.updated, mirroring TestMaybePromoteDraftEmitsUpdate's setup.
	pr := selfDraftPR(55, "foo/bar", "feat/draft-promote")
	pr.State = "open"
	vcs.my["foo/bar"] = []api.PR{pr}
	ci.runs[keyOf("foo/bar", 55)] = []api.CIRun{successRun()}

	bd := newRealBDClient(t)
	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wireOutboxBridge(t, e)

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}
	if sum.DraftPromoted != 1 {
		t.Fatalf("DraftPromoted: got %d want 1", sum.DraftPromoted)
	}
	// The PR was newly observed → counted once as a create. The draft-promote's
	// extra pr.updated must NOT be counted, so BeadsUpdated stays 0.
	if sum.BeadsCreated != 1 {
		t.Fatalf("BeadsCreated: got %d want 1 (draft-promote should not change this)", sum.BeadsCreated)
	}
	if sum.BeadsUpdated != 0 {
		t.Fatalf("BeadsUpdated: got %d want 0 (draft-promote must not double-count)", sum.BeadsUpdated)
	}
}

// TestSyncEmitsCloseForDisappearedPR verifies the Task 8 store-driven
// close-detection path: a store PR row that is open but no longer in the
// observed set is (a) emitted as a pr.closed event, (b) counted in
// summary.BeadsClosed, and (c) marked closed in the store so it is not
// re-detected on the next tick.
//
// This test does NOT wire the bridge dispatcher: it inspects the raw outbox
// rows so it can prove the close event was enqueued (the bridge would consume
// them and we'd only see the bead side-effect).
func TestSyncEmitsCloseForDisappearedPR(t *testing.T) {
	ctx := realBDCtx(t)
	db := store.OpenForTest(t)

	// VCS returns NO PRs for foo/bar — enumeration succeeds (so foo/bar is
	// healthy and close-detection runs for it) but the observed set is empty.
	vcs := newFakeVCS()

	bd := newRealBDClient(t)
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No SetStoreAndDispatch / bridge: we want raw outbox inspection.

	// Pre-seed an OPEN store row for a PR that is no longer observed upstream.
	if _, err := db.UpsertPR(ctx, store.PullRequest{
		Repo: "foo/bar", Number: 1, Ownership: "mine", Author: "phillipg",
		State: "open", Branch: "feat/gone", Base: "main",
		URL: "https://github.com/foo/bar/pull/1",
	}); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	// (b) close was counted.
	if sum.BeadsClosed != 1 {
		t.Fatalf("BeadsClosed: got %d want 1 (errors=%+v)", sum.BeadsClosed, sum.Errors)
	}

	// (c) the store row was marked closed, so ListOpenPRs no longer returns it.
	open, err := db.ListOpenPRs(ctx, "foo/bar")
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("expected no open store rows after close; got %+v", open)
	}

	// (a) a pr.closed event was enqueued for foo/bar#1. Drain the outbox.
	var events []store.Event
	if err := db.RunOutbox(ctx, func(_ context.Context, ev store.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventPRClosed {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.Repo == "foo/bar" && p.Number == 1 {
			found = true
			if p.Merged {
				t.Errorf("pr.closed payload should have Merged=false; got %+v", p)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected a pr.closed event for foo/bar#1 in outbox; events: %+v", events)
	}

	// A SECOND Sync must NOT re-emit a close (the row is already closed, so
	// ListOpenPRs no longer returns it). This proves marking-closed prevents
	// re-detection every tick.
	sum2, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("second Sync: %v (errors=%+v)", err, sum2.Errors)
	}
	if sum2.BeadsClosed != 0 {
		t.Fatalf("second Sync BeadsClosed: got %d want 0 (re-emission not prevented)", sum2.BeadsClosed)
	}
}

// TestSyncHiddenPR_StillObserved_NoFalseClose is the pg2-4dz88.4.3 regression
// guard for the close-detection half of "hiding is display-layer only": a PR
// the operator hid, but which the VCS still reports open, must NOT be
// mistaken for "disappeared upstream". ListOpenPRs is Sync's ONLY source for
// that detection (see store.PullRequest.ListOpenPRs's doc) and deliberately
// does not filter on USER_HIDDEN — filtering there would make this exact PR
// look gone and wrongly emit pr.closed/pr.merged for a PR that never closed.
func TestSyncHiddenPR_StillObserved_NoFalseClose(t *testing.T) {
	ctx := realBDCtx(t)
	db := store.OpenForTest(t)

	vcs := newFakeVCS()
	vcs.my["foo/bar"] = []api.PR{samplePR(1, "foo/bar", "feat/still-here")}

	bd := newRealBDClient(t)
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No SetStoreAndDispatch / bridge: raw outbox inspection, matching
	// TestSyncEmitsCloseForDisappearedPR.

	// Pre-seed an OPEN store row for the SAME PR the fake VCS still reports,
	// already hidden.
	if _, err := db.UpsertPR(ctx, store.PullRequest{
		Repo: "foo/bar", Number: 1, Ownership: "mine", Author: "phillipg",
		State: "open", Branch: "feat/still-here", Base: "main",
		URL: "https://github.com/foo/bar/pull/1",
	}); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}
	if err := db.SetHidden(ctx, "foo/bar", 1, true, "still under review"); err != nil {
		t.Fatalf("seed SetHidden: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	if sum.BeadsClosed != 0 {
		t.Fatalf("BeadsClosed: got %d want 0 -- hiding must never cause a false close", sum.BeadsClosed)
	}

	open, err := db.ListOpenPRs(ctx, "foo/bar")
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if len(open) != 1 || !open[0].UserHidden {
		t.Fatalf("expected the hidden PR to remain open (and still hidden) in the store, got %+v", open)
	}

	var events []store.Event
	if err := db.RunOutbox(ctx, func(_ context.Context, ev store.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	for _, ev := range events {
		if ev.Type != store.EventPRClosed && ev.Type != store.EventPRMerged {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err == nil && p.Repo == "foo/bar" && p.Number == 1 {
			t.Fatalf("hiding must never emit %s for a PR that never closed: %+v", ev.Type, p)
		}
	}
}

func TestSync_DoesNotCloseBeadsForFailedRepo(t *testing.T) {
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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
	// First observation of PR #42 via SyncPR → pr.opened → BeadsCreated.
	// (Pre-fix, applyFetchedPR set BeadsUpdated=1 unconditionally, which
	// mislabeled a first-seen PR as "updated"; that expectation was wrong.)
	if sum.BeadsCreated != 1 {
		t.Fatalf("BeadsCreated: %d", sum.BeadsCreated)
	}
	if sum.BeadsUpdated != 0 {
		t.Fatalf("BeadsUpdated: %d", sum.BeadsUpdated)
	}
}

func TestSyncPR_ClosesWhenUpstreamMerged(t *testing.T) {
	ctx := realBDCtx(t)
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

// TestSyncPRClosedEmitsClose proves the CLI single-PR path (SyncPR) for a
// closed PR emits a store.EventPRClosed (so the bridge cascade-closes the bead)
// instead of closing the bead inline, while still reporting BeadsClosed==1.
//
// After the sync.BeadClient slim (pg2-4c5i.18), the structural guarantee is
// baked into the type — sync.BeadClient = {ListMergeRequests} means
// CloseMergeRequest cannot be called on the engine's bd client. The test still
// inspects the raw outbox to confirm the event was emitted.
func TestSyncPRClosedEmitsClose(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)

	bdc := &refreshFakeBeads{
		existing: &beads.MergeRequest{
			ID:     "mr-1",
			Fields: beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 42},
		},
	}
	closed := samplePR(42, "foo/bar", "feat/z")
	closed.State = "closed"

	vcs := newFakeVCS()
	vcs.views[keyOf("foo/bar", 42)] = closed
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No dispatcher: flushOutbox is a no-op so emitted rows stay pending for raw
	// inspection, and the engine's bd client stays untouched so an inline
	// CloseMergeRequest would be detectable.

	sum, err := e.SyncPR(ctx, "foo/bar", 42)
	if err != nil {
		t.Fatalf("SyncPR: %v (errors=%+v)", err, sum.Errors)
	}
	if sum.BeadsClosed != 1 {
		t.Fatalf("BeadsClosed: got %d want 1", sum.BeadsClosed)
	}
	// Structural guarantee: sync.BeadClient = {ListMergeRequests} prevents the
	// engine from calling CloseMergeRequest inline. Assert the close event fired.

	var events []store.Event
	if err := db.RunOutbox(ctx, func(_ context.Context, ev store.Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventPRClosed {
			continue
		}
		var p store.PRPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal PRPayload: %v", err)
		}
		if p.Repo == "foo/bar" && p.Number == 42 {
			found = true
			if p.Merged {
				t.Errorf("pr.closed payload should have Merged=false; got %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("expected a pr.closed event for foo/bar#42; events: %+v", events)
	}
}

// TestSync_PerRepoWorkspaceIsolation verifies that when the engine's
// Deps.Beads is unset (production wiring), each repo's bd operations land
// in its own .beads/ workspace. Two repos point at two distinct temp
// workspaces; after Sync, each workspace must hold only the beads for its
// own PRs.
func TestSync_PerRepoWorkspaceIsolation(t *testing.T) {
	ctx := realBDCtx(t)

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
	ctx := realBDCtx(t)
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

// noopBeads is a do-nothing BeadClient (sync.BeadClient = {ListMergeRequests}).
// Tests that need the bridge-side methods (beadsbridge.BeadClient) define their
// own fakes; noopBeads is only for engine construction and engine-only tests.
type noopBeads struct{}

func (noopBeads) ListMergeRequests(context.Context, bool) ([]beads.MergeRequest, error) {
	return nil, nil
}

func TestSync_ProgressesEvenIfStateSaveFails(t *testing.T) {
	// Exercise the state-save error path by pointing StateDir at a file
	// where MkdirAll will fail (a regular file with .ext acting as parent).
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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
	ctx := realBDCtx(t)
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
// outbox-projection test. After the #5 event-ownership refactor + this slim,
// sync.BeadClient = {ListMergeRequests}, so the engine literally cannot call
// EnsureMergeRequest through BeadClient — the structural guarantee is now
// baked into the type. noopBeads satisfies the slim interface.
type inlineGuardBeads struct {
	noopBeads
}

// TestSyncCreatesBeadViaOutbox proves Task 6's core invariant: the one-shot
// Engine.Sync per-PR path no longer creates the PR (merge-request) bead inline.
// Instead it emits a pr.opened event whose outbox flush drives the beadsbridge
// handler's EnsureMergeRequest. After the sync.BeadClient slim (pg2-4c5i.18),
// this is structurally guaranteed — sync.BeadClient = {ListMergeRequests}, so
// the engine literally cannot call EnsureMergeRequest through BeadClient. We
// assert:
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

	// The slim sync.BeadClient = {ListMergeRequests} structurally prevents the
	// engine from calling EnsureMergeRequest on guard — no assertion needed.
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
	ctx := realBDCtx(t)

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

// TestMaybePromoteDraftIgnoresExcludedCICheck verifies a draft PR whose ONLY
// failing check is an excluded advisory check (e.g. policy-bot) still
// promotes once the real checks are green — the shared cirollup classifier
// drops the excluded check from the rollup entirely, so it no longer blocks
// promotion. (pg2-qs46b)
// TestMaybePromoteDraftBlockedByUninterpretedGateCheck pins the
// transitional safe-default after excluded_ci_checks was removed outright
// (operator ruling on pg2-dw73b, 2026-08-24) and before
// pg2-4dz88.2.4/pg2-4dz88.2.6 wire the new check-interpreter registry
// (RepoConfig.CheckInterpreters, pg2-4dz88.2.3) into the rollup: with no
// interpreter mechanism consulted yet, a failing approval-gate-style check
// now blocks draft promotion like any other failing check — the
// "uninterpreted checks are never silently excluded" safe default the
// check-interpreter generalization itself requires. Successor to the
// now-removed TestMaybePromoteDraftIgnoresExcludedCICheck, which pinned
// the OLD mechanism's opposite behavior.
func TestMaybePromoteDraftBlockedByUninterpretedGateCheck(t *testing.T) {
	ctx := realBDCtx(t)

	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Self-authored draft PR: real check green, gate-bot failing.
	pr := selfDraftPR(56, "foo/bar", "feat/draft-promote-excl")
	pr.State = "open"
	vcs.my["foo/bar"] = []api.PR{pr}
	ci.runs[keyOf("foo/bar", 56)] = []api.CIRun{
		successRun(),
		{Name: "gate-bot: approval required", Status: "completed", Conclusion: "failure"},
	}

	bd := newRealBDClient(t)
	db := store.OpenForTest(t)

	cfg := cfgWithCICD()

	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	if len(vcs.setDraftCalls) != 0 {
		t.Fatalf("expected 0 SetDraft calls (no interpreter claims the failing check, so it must block promotion); got %d: %+v",
			len(vcs.setDraftCalls), vcs.setDraftCalls)
	}
	if sum.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", sum.DraftPromoted)
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
