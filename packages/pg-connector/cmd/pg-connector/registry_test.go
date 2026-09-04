package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeEnv struct {
	vars map[string]string
	home string
}

func (f fakeEnv) Getenv(k string) string       { return f.vars[k] }
func (f fakeEnv) UserHomeDir() (string, error) { return f.home, nil }

func TestRegistry_List_PrIsListValued(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - pg-connector-pr-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	backends, err := reg.List("pr")
	if err != nil {
		t.Fatalf("List(pr): %v", err)
	}
	if len(backends) != 1 || backends[0] != "pg-connector-pr-github" {
		t.Fatalf("backends = %+v", backends)
	}
}

func TestRegistry_List_IssueAndCIAreListValued(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  issue:
    - pg-connector-issue-jira
    - pg-connector-issue-github
  ci:
    - pg-connector-ci-actions
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	issues, err := reg.List("issue")
	if err != nil {
		t.Fatalf("List(issue): %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %+v", issues)
	}
	ci, err := reg.List("ci")
	if err != nil {
		t.Fatalf("List(ci): %v", err)
	}
	if len(ci) != 1 || ci[0] != "pg-connector-ci-actions" {
		t.Fatalf("ci = %+v", ci)
	}
}

func TestRegistry_Single_ScmIsSingleValued(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  scm: pg-connector-scm-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	backend, err := reg.Single("scm")
	if err != nil {
		t.Fatalf("Single(scm): %v", err)
	}
	if backend != "pg-connector-scm-github" {
		t.Fatalf("backend = %q", backend)
	}
}

func TestRegistry_List_RejectsScalarForListType(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr: not-a-list
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.List("pr"); err == nil {
		t.Fatal("expected error decoding a scalar as a list-valued entity type")
	}
}

func TestRegistry_Single_RejectsListForScalarType(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  scm:
    - a
    - b
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.Single("scm"); err == nil {
		t.Fatal("expected error decoding a list as a single-valued entity type")
	}
}

func TestRegistry_NoExecPrefixDistinction(t *testing.T) {
	// Every registry value is a bare binary name; an "exec:"-prefixed
	// string is not special-cased, stripped, or rejected — it just passes
	// through as-is like any other string.
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - exec:something
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	backends, err := reg.List("pr")
	if err != nil {
		t.Fatalf("List(pr): %v", err)
	}
	if len(backends) != 1 || backends[0] != "exec:something" {
		t.Fatalf("backends = %+v", backends)
	}
}

func TestRegistry_MissingEntityType_ReturnsEmpty(t *testing.T) {
	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	list, err := reg.List("pr")
	if err != nil || len(list) != 0 {
		t.Fatalf("List(pr) = %v, %v", list, err)
	}
	single, err := reg.Single("scm")
	if err != nil || single != "" {
		t.Fatalf("Single(scm) = %q, %v", single, err)
	}
}

func TestRegistry_AllBackends_MixesListAndScalar(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - pg-connector-pr-github
  issue:
    - pg-connector-issue-jira
  scm: pg-connector-scm-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	all, err := reg.AllBackends()
	if err != nil {
		t.Fatalf("AllBackends: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %+v", all)
	}
}

func TestRegistry_TopLevelIsFlatTypeKeyed(t *testing.T) {
	// connector.<type> parses as flat and type-keyed at the top level —
	// confirm the "connector" key is a plain map (not nested further).
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - a
  issue:
    - b
  ci:
    - c
  scm: d
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	for _, entityType := range []string{"pr", "issue", "ci"} {
		if _, err := reg.List(entityType); err != nil {
			t.Errorf("List(%s): %v", entityType, err)
		}
	}
	if _, err := reg.Single("scm"); err != nil {
		t.Errorf("Single(scm): %v", err)
	}
}

func TestLoadRegistryFromEnv_ExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("connector:\n  pr:\n    - explicit-backend\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := fakeEnv{vars: map[string]string{"PG_PR_CONFIG": path}}
	reg, err := loadRegistryFromEnv(env)
	if err != nil {
		t.Fatalf("loadRegistryFromEnv: %v", err)
	}
	backends, _ := reg.List("pr")
	if len(backends) != 1 || backends[0] != "explicit-backend" {
		t.Fatalf("backends = %+v", backends)
	}
}

func TestLoadRegistryFromEnv_ExplicitOverrideMissingFile(t *testing.T) {
	env := fakeEnv{vars: map[string]string{"PG_PR_CONFIG": "/does/not/exist/config.yaml"}}
	_, err := loadRegistryFromEnv(env)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected does-not-exist error, got %v", err)
	}
}

func TestLoadRegistryFromEnv_XDGCandidate(t *testing.T) {
	dir := t.TempDir()
	xdgDir := filepath.Join(dir, "xdg", "pg-pr")
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(xdgDir, "config.yaml")
	if err := os.WriteFile(path, []byte("connector:\n  pr:\n    - xdg-backend\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := fakeEnv{vars: map[string]string{"XDG_CONFIG_HOME": filepath.Join(dir, "xdg")}, home: filepath.Join(dir, "home")}
	reg, err := loadRegistryFromEnv(env)
	if err != nil {
		t.Fatalf("loadRegistryFromEnv: %v", err)
	}
	backends, _ := reg.List("pr")
	if len(backends) != 1 || backends[0] != "xdg-backend" {
		t.Fatalf("backends = %+v", backends)
	}
}

func TestLoadRegistryFromEnv_NoConfigFound(t *testing.T) {
	dir := t.TempDir()
	env := fakeEnv{vars: map[string]string{"XDG_CONFIG_HOME": filepath.Join(dir, "nope")}, home: filepath.Join(dir, "also-nope")}
	_, err := loadRegistryFromEnv(env)
	if err == nil {
		t.Fatal("expected ErrNoConfig")
	}
	if !strings.Contains(err.Error(), "no config file found") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryCandidates_UsesPgPrDirectory(t *testing.T) {
	// The env-var name AND the on-disk directory name carry over from
	// pg-pr unchanged: pg-connector reads the SAME config.yaml pg-pr does.
	env := fakeEnv{vars: map[string]string{"XDG_CONFIG_HOME": "/xdg"}, home: "/home/u"}
	candidates := registryCandidates(env)
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0] != filepath.Join("/xdg", "pg-pr", "config.yaml") {
		t.Errorf("candidates[0] = %q", candidates[0])
	}
	if candidates[1] != filepath.Join("/home/u", ".config", "pg-pr", "config.yaml") {
		t.Errorf("candidates[1] = %q", candidates[1])
	}
}
