package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
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
