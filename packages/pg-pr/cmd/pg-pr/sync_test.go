package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
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

// TestSyncCommand_BroadenFlag proves the --broaden flag (pg2-qzatr) is
// registered with a false default and threads through cobra's flag parsing
// into syFlags.broaden — the value production code (newSyncEngineForCLI)
// wires verbatim into sync.Deps.BroadenOneShotSync. The Deps field's actual
// effect on tryEnumerateEnriched's fan-out/merge is covered at the engine
// level in internal/sync (TestTryEnumerateEnriched_Broaden*); this test
// covers only the CLI-flag-to-syFlags wiring, since newSyncEngineForCLI is
// stubbed out by setStubsForSync for every other test in this file (it
// would otherwise construct a real github.New() provider).
func TestSyncCommand_BroadenFlag(t *testing.T) {
	if f := syncCmd.Flags().Lookup("broaden"); f == nil {
		t.Fatal("expected a --broaden flag to be registered on sync")
	} else if f.DefValue != "false" {
		t.Errorf("expected --broaden to default to false, got %q", f.DefValue)
	}

	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(1)}}}
	bd := &stubBeads{}
	defer setStubsForSync(t, vcs, bd, minimalCLICfg())()

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{"sync", "--broaden"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("sync --broaden: %v", err)
	}
	if !syFlags.broaden {
		t.Error("expected --broaden to set syFlags.broaden = true")
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
// fakeBridgeBeads is a minimal beadsbridge.BeadClient double shared by the
// tests below.
// ----------------------------------------------------------------------

type fakeBridgeBeads struct {
	// findUncachedErr, when non-nil, is returned by
	// FindByRepoAndNumberUncached — used to simulate a beadsbridge dependency
	// failure (e.g. a wrapped prlock.ErrTimeout give-up) surfacing through
	// Handle, for the sync CLI exit-code test (bead pg2-4dz88.6.3). nil (the
	// zero value) preserves every other test's existing behavior.
	findUncachedErr error

	// children models a parent-child bead tree for cascade-close tests
	// (pg2-kij93): keyed by parent id (a merge-request or cycle bead),
	// valued by that parent's direct children. ListChildrenOfPR and
	// ListFeedbackChildrenOfCycle both read it — they share the same
	// mechanism in production too. A nil/absent map entry (the zero value)
	// preserves every pre-existing test's "no children" behavior.
	children map[string][]string

	// closedFeedback/closedCycles/closedMR RECORD every cascade close call,
	// in order, so a test can assert exactly what a cascade touched instead
	// of trusting a stub that silently returns nil (pg2-kij93: this fake
	// previously implemented CloseFeedback as a pure no-op, which made any
	// assertion built on it vacuous).
	closedFeedback []string
	closedCycles   []string
	closedMR       []string
	// closeReasons records the reason each close call above was given,
	// keyed by bead id — used to assert a feedback grandchild's close reason
	// is distinguishable from the cycle/PR's own (pg2-kij93's "never
	// individually triaged" acceptance criterion).
	closeReasons map[string]string
}

func (f *fakeBridgeBeads) FindByRepoAndNumberUncached(context.Context, string, int) (*beads.MergeRequest, error) {
	if f.findUncachedErr != nil {
		return nil, f.findUncachedErr
	}
	return nil, nil
}

func (f *fakeBridgeBeads) ReconcileMergeRequest(context.Context, *beads.MergeRequest, string, beads.MergeRequestFields, bool, bool, bool) (string, bool, error) {
	return "mr-1", false, nil
}

func (f *fakeBridgeBeads) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return nil, nil
}

func (f *fakeBridgeBeads) CloseMergeRequest(_ context.Context, id, reason string) error {
	f.closedMR = append(f.closedMR, id)
	f.recordReason(id, reason)
	return nil
}

func (f *fakeBridgeBeads) ListChildrenOfPR(_ context.Context, id string) ([]string, error) {
	return append([]string(nil), f.children[id]...), nil
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

func (f *fakeBridgeBeads) CloseProcessingCycle(_ context.Context, id, reason string) error {
	f.closedCycles = append(f.closedCycles, id)
	f.recordReason(id, reason)
	return nil
}

func (f *fakeBridgeBeads) CloseFeedback(_ context.Context, id, reason string) error {
	f.closedFeedback = append(f.closedFeedback, id)
	f.recordReason(id, reason)
	return nil
}

// ListFeedbackChildrenOfCycle shares ListChildrenOfPR's tree, matching the
// real beads.Client (both are the same parent-child dep-list scoped to one
// bead — see its doc comment).
func (f *fakeBridgeBeads) ListFeedbackChildrenOfCycle(ctx context.Context, cycleID string) ([]string, error) {
	return f.ListChildrenOfPR(ctx, cycleID)
}

func (f *fakeBridgeBeads) recordReason(id, reason string) {
	if f.closeReasons == nil {
		f.closeReasons = map[string]string{}
	}
	f.closeReasons[id] = reason
}

// ----------------------------------------------------------------------
// One-shot `--pr`/`--repo` lock give-up exit-code wiring (bead pg2-4dz88.6.3)
// ----------------------------------------------------------------------

// TestSyncCommand_SinglePR_LockGiveUpSurfacesBusyExit proves the CLI-level
// half of bead pg2-4dz88.6.3's exit-code requirement: the one-shot
// `pg-pr sync --pr/--repo` path relays a beadsbridge.Handler.Handle failure —
// here a prlock.ErrTimeout cross-process lock give-up — as the command's own
// error, instead of RunOutbox's fire-once contract (internal/store/outbox.go)
// silently absorbing it. main.exitCodeFor then classifies that as exitBusy,
// not the generic path.
//
// The give-up itself (real flock contention across two Lockers) is proven at
// the primitive level by internal/prlock's own tests and at the beadsbridge
// wiring level by internal/beadsbridge's cross-process lock tests (bead
// pg2-4dz88.6.3); this test's job is only the CLI plumbing above that: does a
// Handle failure escape flushOutbox/RunOutbox's discard and reach this
// command's exit code. So the beadsbridge dependency is faked to return an
// error WRAPPING prlock.ErrTimeout directly — Handle propagates whatever
// h.project returns unwrapped (see bridge.go's Handle), so this exercises the
// exact same error-shape a real give-up would produce.
func TestSyncCommand_SinglePR_LockGiveUpSurfacesBusyExit(t *testing.T) {
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(9)}}}
	bd := &stubBeads{}
	cfg := minimalCLICfg()
	cfg.Repos[0].Path = t.TempDir() // required for newBeadsBridgeHandler's repo->path index
	defer setStubsForSync(t, vcs, bd, cfg)()

	fake := &fakeBridgeBeads{
		findUncachedErr: fmt.Errorf("beadsbridge: await cross-process projection lock for foo/bar#9: %w", prlock.ErrTimeout),
	}
	prevClient := newBeadClientForRepo
	newBeadClientForRepo = func(string) beadsbridge.BeadClient { return fake }
	defer func() { newBeadClientForRepo = prevClient }()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"sync", "--pr", "9", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected the lock give-up to surface as the command's error")
	}
	if !errors.Is(err, prlock.ErrTimeout) {
		t.Fatalf("execute error = %v, want an error wrapping prlock.ErrTimeout", err)
	}
	if got := exitCodeFor(err); got != exitBusy {
		t.Errorf("exitCodeFor(err) = %d, want exitBusy (%d)", got, exitBusy)
	}
}
