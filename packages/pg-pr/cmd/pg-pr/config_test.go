package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	// Regression guard for pg2-4dz88.8.3: the removed pr_body_template field
	// must never resurface in the rendered payload.
	if _, present := repo["pr_body_template"]; present {
		t.Fatalf("config show --json emitted removed pr_body_template key: %v", repo)
	}
}

// TestConfigShowAndValidate_PRBodyTemplateStaleKey proves the removed
// pr_body_template config key is silently ignored end-to-end (pg2-4dz88.8.3):
// a config FILE (not a stubbed struct — the field no longer exists on
// config.RepoConfig, so there's nothing to stub) that still declares
// pr_body_template loads through the real newConfigLoader and both
// `config show --json` and `config validate` succeed with no error, no
// warning, and no mention of the removed key anywhere in their output.
func TestConfigShowAndValidate_PRBodyTemplateStaleKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
self_login: me
worktree_root: ` + dir + `
repos:
  - remote: owner/repo
    vcs: github
    cicd: [github-actions]
    issues: jira
    path: ` + dir + `
    pr_body_template: .github/PULL_REQUEST_TEMPLATE.md
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_PR_CONFIG", path)
	cfFlags.jsonOutput = false

	var showOut, showErr bytes.Buffer
	rootCmd.SetOut(&showOut)
	rootCmd.SetErr(&showErr)
	rootCmd.SetArgs([]string{"config", "show", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("config show --json: %v\nstderr=%s", err, showErr.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(showOut.Bytes(), &parsed); err != nil {
		t.Fatalf("decode: %v\n%s", err, showOut.String())
	}
	repos, ok := parsed["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("repos = %v", parsed["repos"])
	}
	repo := repos[0].(map[string]any)
	if _, present := repo["pr_body_template"]; present {
		t.Fatalf("config show --json emitted removed pr_body_template key for a stale config file: %v", repo)
	}

	// config show --json above left the shared cfFlags.jsonOutput var (bound
	// to BOTH configShowCmd's and configValidateCmd's --json flag) set to
	// true; reset it so this validate call renders the human-readable report
	// its own args ask for, not JSON left over from the previous command.
	cfFlags.jsonOutput = false
	var validateOut, validateErr bytes.Buffer
	rootCmd.SetOut(&validateOut)
	rootCmd.SetErr(&validateErr)
	rootCmd.SetArgs([]string{"config", "validate"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("config validate: %v\nstdout=%s\nstderr=%s", err, validateOut.String(), validateErr.String())
	}
	if strings.Contains(validateOut.String(), "pr_body_template") {
		t.Fatalf("config validate output mentions removed pr_body_template key:\n%s", validateOut.String())
	}
	if !strings.Contains(validateOut.String(), "ok: no validation issues") {
		t.Fatalf("expected a clean validation report for a config file with only a stale pr_body_template key, got:\n%s", validateOut.String())
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
