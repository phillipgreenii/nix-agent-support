package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMultiEntityConfigFor writes a connector.<type> registry exercising
// every entity type (unlike pr_test.go's writeConfigFor, which only ever
// registers "pr") and points $PG_PR_CONFIG at it, returning the path
// written so a test can assert ConfigShowResult.ConfigPath against it.
func writeMultiEntityConfigFor(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := "connector:\n" +
		"  pr:\n" +
		"    - pg-connector-pr-github\n" +
		"  issue:\n" +
		"    - pg-connector-issue-beads\n" +
		"  scm: pg-connector-scm-git\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	return cfg
}

func TestConfigShow_JSON_ReportsResolvedPathAndBackends(t *testing.T) {
	cfg := writeMultiEntityConfigFor(t)

	stdout, code := executePr(t, []string{"config", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var result ConfigShowResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode ConfigShowResult: %v (stdout=%s)", err, stdout)
	}
	if result.ConfigPath != cfg {
		t.Fatalf("config_path = %q, want %q", result.ConfigPath, cfg)
	}
	if len(result.PR) != 1 || result.PR[0] != "pg-connector-pr-github" {
		t.Fatalf("pr = %+v", result.PR)
	}
	if len(result.Issue) != 1 || result.Issue[0] != "pg-connector-issue-beads" {
		t.Fatalf("issue = %+v", result.Issue)
	}
	if len(result.CI) != 0 {
		t.Fatalf("ci = %+v, want empty (no connector.ci entry)", result.CI)
	}
	if result.Scm != "pg-connector-scm-git" {
		t.Fatalf("scm = %q, want pg-connector-scm-git", result.Scm)
	}
}

func TestConfigShow_Human_ReportsResolvedPathAndBackends(t *testing.T) {
	cfg := writeMultiEntityConfigFor(t)

	stdout, code := executePr(t, []string{"config", "show", "--output", "human"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, cfg) {
		t.Fatalf("stdout = %q, want it to name the resolved config path %q", stdout, cfg)
	}
	if !strings.Contains(stdout, "pg-connector-pr-github") || !strings.Contains(stdout, "pg-connector-scm-git") {
		t.Fatalf("stdout = %q, want it to name the registered backends", stdout)
	}
	if !strings.Contains(stdout, "ci: (none registered)") {
		t.Fatalf("stdout = %q, want ci reported as not registered", stdout)
	}
}

func TestConfigShow_NoConfigFound_ReturnsCLIError(t *testing.T) {
	// A misconfigured host (no $PG_PR_CONFIG, no XDG/home candidate) must
	// surface loadRegistryFromEnv's own ErrNoConfig as a CLI-level failure
	// (exit 1) — the same failure mode "config show" shares with every
	// other fan-out-shaped verb's own un-wrapped LoadRegistry error
	// (auth.go/config_validate.go's own RunE bodies; see
	// TestRun_AuthStatus_NoConfigIsGenericFailure in main_test.go for the
	// existing precedent this mirrors: exit code only, not stdout content,
	// since a raw returned error never reaches cobra's own SilenceErrors:
	// true output — only main's real run() prints it, on stderr).
	t.Setenv("PG_PR_CONFIG", "/does/not/exist/config.yaml")
	if code := run([]string{"config", "show"}); code != 1 {
		t.Fatalf("run(config show) = %d, want 1", code)
	}
}

func TestConfigShow_MissingEntry_ReportsEmptyNotError(t *testing.T) {
	// registry.go's List/Single contract: an entity type with no
	// connector.<type> entry at all returns (nil/"", nil), never an error
	// — "config show" must surface that as an empty/absent field, not fail
	// the whole command.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - only-pr-backend\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, code := executePr(t, []string{"config", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var result ConfigShowResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode ConfigShowResult: %v (stdout=%s)", err, stdout)
	}
	if len(result.Issue) != 0 || len(result.CI) != 0 || result.Scm != "" {
		t.Fatalf("result = %+v, want issue/ci/scm all empty", result)
	}
}

func TestResolveConfigPath_ExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := fakeEnv{vars: map[string]string{"PG_PR_CONFIG": path}}
	got, err := resolveConfigPath(env)
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if got != path {
		t.Fatalf("resolveConfigPath = %q, want %q", got, path)
	}
}

func TestResolveConfigPath_ExplicitOverrideMissingFile(t *testing.T) {
	env := fakeEnv{vars: map[string]string{"PG_PR_CONFIG": "/does/not/exist/config.yaml"}}
	_, err := resolveConfigPath(env)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected does-not-exist error, got %v", err)
	}
}
