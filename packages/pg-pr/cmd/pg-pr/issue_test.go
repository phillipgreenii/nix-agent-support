package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
)

// fakeIssuesProv is a deterministic issues.Provider for CLI tests.
type fakeIssuesProv struct {
	issue *api.Issue
	err   error
	calls []string // ticket IDs received
}

func (f *fakeIssuesProv) GetIssue(_ context.Context, id string) (*api.Issue, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.issue, nil
}

// withIssueStubs replaces the loader / branch-detect / provider factory
// hooks for the duration of the test.
func withIssueStubs(t *testing.T, cfg *config.Config, repo string, prov *fakeIssuesProv) {
	t.Helper()
	origLoader := newIssueLoader
	origDetect := newIssueBranchDetect
	origProv := newIssueProvider
	t.Cleanup(func() {
		newIssueLoader = origLoader
		newIssueBranchDetect = origDetect
		newIssueProvider = origProv
	})
	newIssueLoader = func(context.Context) (*config.Config, error) { return cfg, nil }
	newIssueBranchDetect = func(context.Context) (*api.BranchInfo, error) {
		return &api.BranchInfo{Repo: repo, Branch: "main"}, nil
	}
	newIssueProvider = func(name string) (issues.Provider, error) {
		// Echo the requested name into the fake for assertion via t.Log if needed.
		t.Logf("requested provider: %s", name)
		return prov, nil
	}
}

func TestIssueShow_HumanAutoProvider(t *testing.T) {
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "owner/repo", VCS: "github", Issues: "jira"},
		},
	}
	prov := &fakeIssuesProv{issue: &api.Issue{
		ID: "ZR-42", Title: "Do the thing", State: "In Progress", URL: "https://example.com/ZR-42",
	}}
	withIssueStubs(t, cfg, "owner/repo", prov)
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "ZR-42"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"provider: jira",
		"id: ZR-42",
		"title: Do the thing",
		"state: In Progress",
		"url: https://example.com/ZR-42",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if len(prov.calls) != 1 || prov.calls[0] != "ZR-42" {
		t.Fatalf("provider calls = %+v", prov.calls)
	}
}

func TestIssueShow_JSON(t *testing.T) {
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "owner/repo", VCS: "github", Issues: "jira"},
		},
	}
	prov := &fakeIssuesProv{issue: &api.Issue{
		ID: "ZR-1", Title: "T", State: "Open", URL: "u",
	}}
	withIssueStubs(t, cfg, "owner/repo", prov)
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "ZR-1", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var got api.Issue
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if got.ID != "ZR-1" {
		t.Fatalf("id = %s", got.ID)
	}
}

func TestIssueShow_ExplicitProviderWins(t *testing.T) {
	// Config has owner/repo with `issues: jira`, but --provider should
	// override and the branch-detect path should not even be consulted.
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "owner/repo", VCS: "github", Issues: "jira"},
		},
	}
	prov := &fakeIssuesProv{issue: &api.Issue{ID: "GH-99", Title: "from gh", State: "Open"}}
	// branch-detect deliberately returns wrong repo to prove it isn't used.
	origDetect := newIssueBranchDetect
	defer func() { newIssueBranchDetect = origDetect }()
	newIssueBranchDetect = func(context.Context) (*api.BranchInfo, error) {
		t.Fatal("branch.Detect should not be called when --provider is set")
		return nil, nil
	}
	withIssueStubs(t, cfg, "should-not-matter", prov)
	// re-install our fail-loud branch detect after withIssueStubs
	newIssueBranchDetect = func(context.Context) (*api.BranchInfo, error) {
		t.Fatal("branch.Detect should not be called when --provider is set")
		return nil, nil
	}
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "GH-99", "--provider", "github-issues"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "provider: github-issues") {
		t.Fatalf("expected provider override in output: %s", stdout.String())
	}
}

func TestIssueShow_NoIssuesProviderConfigured(t *testing.T) {
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "owner/repo", VCS: "github"}, // no Issues
		},
	}
	prov := &fakeIssuesProv{}
	withIssueStubs(t, cfg, "owner/repo", prov)
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "X-1"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error, got stdout=%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "no issues provider") {
		t.Fatalf("error = %v", err)
	}
}

func TestIssueShow_UnknownRepoForcesExplicit(t *testing.T) {
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "other/repo", VCS: "github", Issues: "jira"},
		},
	}
	prov := &fakeIssuesProv{}
	withIssueStubs(t, cfg, "owner/repo", prov)
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "X-1"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error, got stdout=%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "not in the pg-pr config") {
		t.Fatalf("error = %v", err)
	}
}

func TestIssueShow_ProviderError(t *testing.T) {
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "owner/repo", VCS: "github", Issues: "jira"},
		},
	}
	prov := &fakeIssuesProv{err: errors.New("upstream 401")}
	withIssueStubs(t, cfg, "owner/repo", prov)
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "ZR-9"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "upstream 401") {
		t.Fatalf("error = %v", err)
	}
}

func TestIssueShow_EnvJSON(t *testing.T) {
	cfg := &config.Config{
		SelfLogin: "me", WorktreeRoot: "/tmp",
		Repos: []config.RepoConfig{
			{Remote: "owner/repo", VCS: "github", Issues: "jira"},
		},
	}
	prov := &fakeIssuesProv{issue: &api.Issue{ID: "X-1", Title: "T", State: "Open"}}
	withIssueStubs(t, cfg, "owner/repo", prov)
	t.Setenv("PGPR_OUTPUT", "json")
	isFlags.provider = ""
	isFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"issue", "show", "X-1"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var got api.Issue
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("expected JSON from env: %v\n%s", err, stdout.String())
	}
}
