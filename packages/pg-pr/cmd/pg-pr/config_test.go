package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
)

// withConfigStub installs a deterministic loader for the duration of t and
// restores the original after.
func withConfigStub(t *testing.T, cfg *config.Config, err error) {
	t.Helper()
	orig := newConfigLoader
	t.Cleanup(func() { newConfigLoader = orig })
	newConfigLoader = func(context.Context) (*config.Config, error) { return cfg, err }
}

func validCfg() *config.Config {
	return &config.Config{
		Path:         "/tmp/test/pg-pr.yaml",
		SelfLogin:    "me",
		WorktreeRoot: "/tmp/wt",
		Repos: []config.RepoConfig{
			{
				Remote: "owner/repo",
				VCS:    "github",
				CICD:   []string{"github-actions"},
				Issues: "jira",
				Path:   "/tmp/wt/repo",
			},
		},
	}
}

func TestConfigShow_Human(t *testing.T) {
	withConfigStub(t, validCfg(), nil)
	cfFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"config", "show"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"self_login: me",
		"worktree_root: /tmp/wt",
		"repos: 1",
		"owner/repo",
		"github-actions",
		"jira",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestConfigShow_JSON(t *testing.T) {
	withConfigStub(t, validCfg(), nil)
	cfFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"config", "show", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if parsed["self_login"] != "me" {
		t.Fatalf("self_login = %v", parsed["self_login"])
	}
	// repos is an array of objects with snake_case keys.
	repos, ok := parsed["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("repos = %v", parsed["repos"])
	}
	repo := repos[0].(map[string]any)
	if repo["remote"] != "owner/repo" {
		t.Fatalf("remote = %v", repo["remote"])
	}
}

func TestConfigValidate_OK(t *testing.T) {
	withConfigStub(t, validCfg(), nil)
	cfFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"config", "validate"})

	if err := rootCmd.Execute(); err != nil {
		// Note: /tmp/wt/repo probably doesn't exist on disk; that's a
		// warning, not an error, so the command should still exit 0.
		t.Fatalf("execute: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
}

func TestConfigValidate_Errors(t *testing.T) {
	bad := &config.Config{
		Path:      "/tmp/bad.yaml",
		SelfLogin: "", // missing!
		Repos: []config.RepoConfig{
			{Remote: "no-slash", VCS: "made-up", Issues: "also-bogus"},
		},
	}
	withConfigStub(t, bad, nil)
	cfFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"config", "validate"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected non-zero exit on invalid config\nstdout=%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "config invalid") {
		t.Fatalf("error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"self_login",
		"repos[0].remote",
		"repos[0].vcs",
		"repos[0].issues",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("validation output missing %q:\n%s", want, out)
		}
	}
}

func TestConfigValidate_JSON(t *testing.T) {
	bad := &config.Config{
		Path:         "/tmp/bad.yaml",
		SelfLogin:    "",
		WorktreeRoot: "/tmp/wt",
		Repos:        []config.RepoConfig{{Remote: "owner/repo", VCS: "made-up"}},
	}
	withConfigStub(t, bad, nil)
	cfFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"config", "validate", "--json"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected non-zero exit")
	}
	var report config.ValidationReport
	if jerr := json.Unmarshal(stdout.Bytes(), &report); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, stdout.String())
	}
	if !report.HasErrors() {
		t.Fatalf("expected errors, got %+v", report)
	}
}

func TestConfigShow_EnvJSON(t *testing.T) {
	withConfigStub(t, validCfg(), nil)
	t.Setenv("PGPR_OUTPUT", "json")
	cfFlags.jsonOutput = false

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"config", "show"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v\nstderr=%s", err, stderr.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected JSON from PGPR_OUTPUT=json: %v\n%s", err, stdout.String())
	}
}
