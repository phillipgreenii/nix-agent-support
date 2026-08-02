package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// ----------------------------------------------------------------------
// Stubs used to keep the CLI tests fast / hermetic (no real bd, no gh).
// ----------------------------------------------------------------------

type stubVCS struct {
	prs map[string][]api.PR
}

func (s *stubVCS) GetPR(_ context.Context, repo string, n int) (*api.PR, error) {
	for _, pr := range s.prs[repo] {
		if pr.Number == n {
			return &pr, nil
		}
	}
	return nil, errors.New("not found")
}

func (s *stubVCS) ListMyPRs(_ context.Context, repo string) ([]api.PR, error) {
	return s.prs[repo], nil
}

func (s *stubVCS) ListTeamPRs(_ context.Context, _ string, _ []string) ([]api.PR, error) {
	return nil, nil
}

type stubBeads struct {
	created, closed int
}

func (s *stubBeads) EnsureMergeRequest(_ context.Context, _ string, _ beads.MergeRequestFields) (string, bool, error) {
	s.created++
	return "stub-1", false, nil
}

func (s *stubBeads) UpdateMergeRequest(_ context.Context, _ string, _ beads.MergeRequestFields) error {
	return nil
}

func (s *stubBeads) CloseMergeRequest(_ context.Context, _, _ string) error {
	s.closed++
	return nil
}

func (s *stubBeads) ListMergeRequests(_ context.Context, _ bool) ([]beads.MergeRequest, error) {
	return nil, nil
}

func (s *stubBeads) GetMergeRequest(_ context.Context, _ string) (*beads.MergeRequest, error) {
	return nil, nil
}

func (s *stubBeads) CreateProcessingCycle(_ context.Context, _, _ string, _ bool) (string, error) {
	return "", nil
}

func (s *stubBeads) FindOpenProcessingCycle(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}
func (s *stubBeads) CloseProcessingCycle(_ context.Context, _, _ string) error { return nil }
func (s *stubBeads) ListChildrenOfPR(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (s *stubBeads) CloseFeedback(_ context.Context, _, _ string) error { return nil }

// ----------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------

func setStubsForSync(t *testing.T, vcs *stubVCS, bd *stubBeads, cfg *config.Config) func() {
	t.Helper()
	prevCfg := loadConfigForCLI
	prevEng := newSyncEngineForCLI
	prevFlags := syFlags

	loadConfigForCLI = func(_ context.Context) (*config.Config, error) { return cfg, nil }
	newSyncEngineForCLI = func(c *config.Config) (*sync.Engine, error) {
		return sync.New(sync.Deps{
			Cfg:      c,
			VCS:      map[string]sync.VCSProvider{"github": vcs},
			Beads:    bd,
			StateDir: t.TempDir(),
		})
	}

	return func() {
		loadConfigForCLI = prevCfg
		newSyncEngineForCLI = prevEng
		syFlags = prevFlags
	}
}

func minimalCLICfg() *config.Config {
	return &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: "/tmp/wr",
		Repos: []config.RepoConfig{
			{Remote: "foo/bar", VCS: "github"},
		},
	}
}

func samplePR(n int) api.PR {
	return api.PR{
		Repo: "foo/bar", Number: n, State: "open",
		Branch: "feat/x", Base: "main", Author: "phillipg",
		URL: "https://github.com/foo/bar/pull/" + itoa(n),
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string('0'+rune(n%10)) + out
		n /= 10
	}
	return out
}

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

func TestSyncCommand_HumanOutput(t *testing.T) {
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(1), samplePR(2)}}}
	bd := &stubBeads{}
	defer setStubsForSync(t, vcs, bd, minimalCLICfg())()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"sync"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("sync: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "2 PR(s) observed") {
		t.Fatalf("expected total PR count in human output, got %q", out)
	}
}

func TestSyncCommand_JSONOutput(t *testing.T) {
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(1)}}}
	bd := &stubBeads{}
	defer setStubsForSync(t, vcs, bd, minimalCLICfg())()

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{"sync", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("sync --json: %v", err)
	}
	var s sync.Summary
	if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v\nstdout: %s", err, stdout.String())
	}
	if s.TotalPRs != 1 {
		t.Fatalf("TotalPRs: got %d want 1", s.TotalPRs)
	}
}

// TestSyncCommand_EnvJSON verifies PGPR_OUTPUT=json (no --json flag) emits
// JSON output. Covers the A15 env-var fallback.
func TestSyncCommand_EnvJSON(t *testing.T) {
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(1)}}}
	bd := &stubBeads{}
	defer setStubsForSync(t, vcs, bd, minimalCLICfg())()
	t.Setenv("PGPR_OUTPUT", "json")
	// Defensive reset: prior tests may have toggled the flag.
	syFlags.jsonOutput = false

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{"sync"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("sync (env=json): %v", err)
	}
	var s sync.Summary
	if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
		t.Fatalf("expected JSON from PGPR_OUTPUT=json, got %q\nerr: %v",
			stdout.String(), err)
	}
	if s.TotalPRs != 1 {
		t.Fatalf("TotalPRs: got %d want 1", s.TotalPRs)
	}
}

func TestSyncCommand_SinglePRRequiresRepo(t *testing.T) {
	defer setStubsForSync(t, &stubVCS{}, &stubBeads{}, minimalCLICfg())()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"sync", "--pr", "42"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error for --pr without --repo")
	}
	if !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("error message: %v", err)
	}
}

func TestSyncCommand_DaemonInvalidInterval(t *testing.T) {
	defer setStubsForSync(t, &stubVCS{}, &stubBeads{}, minimalCLICfg())()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"sync", "--daemon", "--interval", "not-a-duration"})

	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid --interval") {
		t.Fatalf("expected --interval parse error, got %v", err)
	}
}

func TestSyncCommand_PropagatesConfigError(t *testing.T) {
	prev := loadConfigForCLI
	defer func() { loadConfigForCLI = prev }()
	loadConfigForCLI = func(_ context.Context) (*config.Config, error) {
		return nil, errors.New("no config")
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"sync"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error from missing config")
	}
}

// ----------------------------------------------------------------------
// Producer review kill-switch: live per-dispatch re-read (bead pg2-8vp9e)
// ----------------------------------------------------------------------

// fakeBridgeBeads is a minimal beadsbridge.BeadClient double for the producer
// handler test. It records EnsureDraftReviewBead calls so the test can observe
// whether draft-review PRODUCTION was suppressed by the review kill switch. The
// pr.updated mine path also touches EnsureMergeRequest, SetMergeRequestCoOwned,
// and GetMergeRequest (via reconcilePriority); those return benign zero values
// that let the bridge reach (or skip) the draft-review call.
type fakeBridgeBeads struct {
	drCalls int // EnsureDraftReviewBead invocations
}

func (f *fakeBridgeBeads) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "mr-1", false, nil
}
func (f *fakeBridgeBeads) SetMergeRequestCoOwned(context.Context, string, bool) error { return nil }
func (f *fakeBridgeBeads) SetMergeRequestCoOwnedWith(context.Context, string, bool, *beads.MergeRequest) error {
	return nil
}

func (f *fakeBridgeBeads) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return nil, nil
}
func (f *fakeBridgeBeads) CloseMergeRequest(context.Context, string, string) error { return nil }
func (f *fakeBridgeBeads) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakeBridgeBeads) CreateProcessingCycle(context.Context, beads.CreateProcessingCycleInput) (string, error) {
	return "", nil
}

func (f *fakeBridgeBeads) ResolveProcessingCycle(context.Context, string, string) (beads.ProcessingCycleState, error) {
	return beads.ProcessingCycleState{}, nil
}

func (f *fakeBridgeBeads) AppendProcessingCycleNote(context.Context, string, string, string, []string) error {
	return nil
}
func (f *fakeBridgeBeads) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (f *fakeBridgeBeads) CloseFeedback(context.Context, string, string) error        { return nil }
func (f *fakeBridgeBeads) EnsureDraftReviewBead(context.Context, string, string, bool) (string, error) {
	f.drCalls++
	return "dr-1", nil
}
func (f *fakeBridgeBeads) EnsureDraftReviewMineLabel(context.Context, string) error { return nil }
func (f *fakeBridgeBeads) EnsureAttentionBead(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeBridgeBeads) CloseAttentionBead(context.Context, string, string) error { return nil }
func (f *fakeBridgeBeads) GetMergeRequest(context.Context, string) (*beads.MergeRequest, error) {
	return nil, nil
}

func (f *fakeBridgeBeads) GetMergeRequestUncached(context.Context, string) (*beads.MergeRequest, error) {
	return nil, nil
}
func (f *fakeBridgeBeads) SetPriority(context.Context, string, int) error    { return nil }
func (f *fakeBridgeBeads) AddLabel(context.Context, string, string) error    { return nil }
func (f *fakeBridgeBeads) RemoveLabel(context.Context, string, string) error { return nil }

// cliCfgWithReview builds a CLI config with one repo (Path set so the producer's
// repo→path index resolves) and review.enabled = enabled.
func cliCfgWithReview(t *testing.T, enabled bool) *config.Config {
	t.Helper()
	b := enabled
	return &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: t.TempDir(),
		Repos:        []config.RepoConfig{{Remote: "foo/bar", VCS: "github", Path: t.TempDir()}},
		Review:       config.ReviewConfig{Enabled: &b},
	}
}

// TestBeadsBridgeHandler_ReReadsReviewEnabledPerDispatch is the producer-side
// regression test for bead pg2-8vp9e (follow-up to the consumer fix pg2-bw30):
// review.enabled MUST be honored PER DISPATCH, not latched when the handler is
// constructed. With the handler built once, flipping review.enabled on the
// engine's live config via ReplaceCfg (as a per-poll disk reload does) must
// change whether the producer emits draft-review beads on the very next
// dispatch — with no handler reconstruction and no daemon restart.
//
// Before the fix, produceDraftReviews was captured once at construction, so the
// disabled dispatch still produced a draft-review bead (drCalls kept climbing)
// and this test failed.
func TestBeadsBridgeHandler_ReReadsReviewEnabledPerDispatch(t *testing.T) {
	fake := &fakeBridgeBeads{}
	prev := newBeadClientForRepo
	newBeadClientForRepo = func(string) beadsbridge.BeadClient { return fake }
	defer func() { newBeadClientForRepo = prev }()

	engine, err := sync.New(sync.Deps{
		Cfg: cliCfgWithReview(t, true),
		VCS: map[string]sync.VCSProvider{"github": &stubVCS{}},
	})
	if err != nil {
		t.Fatalf("sync.New: %v", err)
	}

	// Build the handler ONCE, bound to the engine's live config.
	h := newBeadsBridgeHandler(engine.Config)

	// A ready mine PR — the case that produces a draft-review bead by default.
	payload, _ := json.Marshal(store.PRPayload{Repo: "foo/bar", Number: 7, Ownership: "mine", Draft: false})
	ev := store.Event{Type: store.EventPRUpdated, Payload: payload}
	ctx := context.Background()

	// Dispatch 1 — review.enabled=true: producer emits a draft-review bead.
	if err := h(ctx, ev); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if fake.drCalls != 1 {
		t.Fatalf("enabled dispatch must produce a draft-review bead; drCalls=%d", fake.drCalls)
	}

	// Flip review.enabled OFF on the LIVE config (as a per-poll disk reload
	// would apply). No handler reconstruction, no restart.
	engine.ReplaceCfg(cliCfgWithReview(t, false))

	// Dispatch 2 — review.enabled=false: producer MUST suppress draft-review
	// production (WithoutDraftReviews applied), so drCalls stays 1.
	if err := h(ctx, ev); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if fake.drCalls != 1 {
		t.Fatalf("disabled dispatch must NOT produce a draft-review bead (config change latched?); drCalls=%d", fake.drCalls)
	}

	// Flip back ON: production resumes on the next dispatch, still no restart.
	engine.ReplaceCfg(cliCfgWithReview(t, true))
	if err := h(ctx, ev); err != nil {
		t.Fatalf("dispatch 3: %v", err)
	}
	if fake.drCalls != 2 {
		t.Fatalf("re-enabled dispatch must resume producing a draft-review bead; drCalls=%d", fake.drCalls)
	}
}
