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
func (s *stubBeads) CloseMergeRequest(_ context.Context, _, _ string) error {
	s.closed++
	return nil
}
func (s *stubBeads) ListMergeRequests(_ context.Context, _ bool) ([]beads.MergeRequest, error) {
	return nil, nil
}

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

func TestSyncCommand_DaemonStubMessage(t *testing.T) {
	defer setStubsForSync(t, &stubVCS{}, &stubBeads{}, minimalCLICfg())()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"sync", "--daemon"})

	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "Phase 3") {
		t.Fatalf("expected Phase 3 deferral message, got %v", err)
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
