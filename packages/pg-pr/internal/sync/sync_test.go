package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	// CIRuns), an UNCLAIMED GraphQL run must NOT leak into the result -- CI still
	// comes from the dedicated CICD provider so large PRs keep complete CI
	// (GraphQL statusCheckRollup caps at 30 contexts). A run a configured
	// check-interpreter DOES claim is a different case, covered by
	// TestEnrichOnePR_MergesClaimedGraphQLRunIntoCICDRuns below (pg2-g9fu0).
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
		t.Errorf("CI must come from the CICD provider (unclaimed GraphQL run must not leak in), got %+v", got.CIRuns)
	}
	if len(got.Comments) != 1 || got.Comments[0].ThreadID != "PRRT_abc" {
		t.Errorf("comments should still come from GraphQL, got %+v", got.Comments)
	}
}

// TestEnrichOnePR_MergesClaimedGraphQLRunIntoCICDRuns is the per-PR-path
// counterpart of TestReconcileTruncatedCI_PreservesClaimedGateRun (bulk
// path, ci_truncation_test.go): a run a configured check-interpreter CLAIMS
// (e.g. an approval-gate check like policy-bot) is a classic
// commit-Status-API context, which the dedicated CICD provider structurally
// can never reproduce (Actions/CheckRun only) -- so it must be merged back
// in from GraphQL's statusCheckRollup (ep.CIRuns), additive to (never
// replacing) the CICD-sourced runs. This is pg2-g9fu0's fix: before it,
// enrichOnePR discarded ep.CIRuns entirely, so gateStateFromSync could never
// observe a classic-status gate check via the per-PR daemon path.
func TestEnrichOnePR_MergesClaimedGraphQLRunIntoCICDRuns(t *testing.T) {
	gateRun := api.CIRun{
		Name:        "policy-bot: approval required (click for details): main",
		Status:      "completed",
		Conclusion:  "failure",
		Description: "0/1 rules approved",
		Provider:    "github-status",
	}
	vp := &enricherVCS{ep: &vcs.EnrichedPR{
		CIRuns: []api.CIRun{{Name: "from-graphql-unclaimed"}, gateRun},
	}}
	cicd := newFakeCICD()
	cicd.runs[keyOf("o/r", 42)] = []api.CIRun{{Name: "from-cicd"}}
	e := &Engine{deps: Deps{
		VCS:  map[string]VCSProvider{"github": vp},
		CICD: map[string]CICDProvider{"gh-actions": cicd},
	}}
	rcfg := config.RepoConfig{
		Remote: "o/r", VCS: "github", CICD: []string{"gh-actions"},
		CheckInterpreters: []config.CheckInterpreterConfig{
			{Patterns: []string{"^policy-bot"}, Type: "approval-gate"},
		},
	}
	got := e.enrichOnePR(context.Background(), rcfg, api.PR{Repo: "o/r", Number: 42})

	byName := map[string]api.CIRun{}
	for _, r := range got.CIRuns {
		byName[r.Name] = r
	}
	if _, ok := byName["from-cicd"]; !ok {
		t.Errorf("CICD-provided run must survive the merge, got %+v", got.CIRuns)
	}
	if _, ok := byName["from-graphql-unclaimed"]; ok {
		t.Errorf("an UNCLAIMED GraphQL run must NOT be merged in, got %+v", got.CIRuns)
	}
	merged, ok := byName[gateRun.Name]
	if !ok {
		t.Fatalf("the check-interpreter-CLAIMED GraphQL run (policy-bot) must be merged in, got %+v", got.CIRuns)
	}
	if merged.Description != "0/1 rules approved" {
		t.Errorf("merged gate run's Description must be preserved intact, got %q", merged.Description)
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
