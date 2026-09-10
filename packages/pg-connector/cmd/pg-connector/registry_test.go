package main

import (
	"encoding/json"
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

func TestParseRegistry_RejectsUnknownKeyUnderConnector(t *testing.T) {
	// A typo'd entity-type key (e.g. "prs" for "pr") under connector: must
	// be rejected outright rather than silently ignored — left silent, it
	// decodes to zero backends for that type and AllBackends/config
	// validate report exit 3 ("all backends down"), indistinguishable
	// from a genuinely all-down host [bug A16].
	_, err := parseRegistry([]byte(`
connector:
  prs:
    - pg-connector-pr-github
`), "test.yaml")
	if err == nil {
		t.Fatal("expected an error for an unrecognized key under connector:")
	}
	if !strings.Contains(err.Error(), "prs") {
		t.Fatalf("error = %v, want it to name the unrecognized key", err)
	}
}

func TestParseRegistry_KnownKeysUnderConnectorAreAccepted(t *testing.T) {
	// Every entityTypes key, together, must still parse cleanly — the new
	// unknown-key check must not become over-strict and reject the
	// legitimate keys it exists to allow.
	_, err := parseRegistry([]byte(`
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
}

func TestRegistry_List_RejectsEmptyList(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr: []
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.List("pr"); err == nil {
		t.Fatal("expected an error for an explicit empty list")
	}
}

func TestRegistry_List_RejectsDuplicateBackendName(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - pg-connector-pr-github
    - pg-connector-pr-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.List("pr"); err == nil {
		t.Fatal("expected an error for a duplicate backend name in one list")
	}
}

func TestRegistry_List_RejectsPathSeparatorInName(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - ../evil
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.List("pr"); err == nil {
		t.Fatal("expected an error for a backend name containing a path separator")
	}
}

func TestRegistry_Single_RejectsPathSeparatorInName(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  scm: sub/dir-backend
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.Single("scm"); err == nil {
		t.Fatal("expected an error for a backend name containing a path separator")
	}
}

func TestRegistry_Single_RejectsEmptyScalar(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  scm: ""
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.Single("scm"); err == nil {
		t.Fatal("expected an error for an explicit empty scalar")
	}
}

func TestRegistry_AllBackends_DedupesBackendRegisteredUnderMultipleTypes(t *testing.T) {
	// A single binary implementing more than one capability — mandatory
	// per INV-REG-1's multi-capability backends — is registered under
	// each type it supports. AllBackends must report it once, not once
	// per type it's registered under [bug A27].
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - shared-backend
  issue:
    - shared-backend
    - only-issue-backend
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	all, err := reg.AllBackends()
	if err != nil {
		t.Fatalf("AllBackends: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %+v, want exactly [shared-backend, only-issue-backend]", all)
	}
	if all[0] != "shared-backend" || all[1] != "only-issue-backend" {
		t.Fatalf("all = %+v, want shared-backend first (pr, its first registered type) then only-issue-backend", all)
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

func TestRegistry_AttentionSources_AbsentKeyReturnsEmpty(t *testing.T) {
	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	sources, err := reg.AttentionSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("AttentionSources() = %v, %v", sources, err)
	}
}

func TestRegistry_SearchSources_AbsentKeyReturnsEmpty(t *testing.T) {
	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	sources, err := reg.SearchSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("SearchSources() = %v, %v", sources, err)
	}
}

func TestRegistry_AttentionSources_ExplicitlyEmptyListIsRejected(t *testing.T) {
	reg, err := parseRegistry([]byte(`
attention:
  sources: []
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.AttentionSources(); err == nil {
		t.Fatal("expected an error for an explicitly-empty attention.sources list")
	}
}

func TestRegistry_SearchSources_ExplicitlyEmptyListIsRejected(t *testing.T) {
	reg, err := parseRegistry([]byte(`
search:
  sources: []
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.SearchSources(); err == nil {
		t.Fatal("expected an error for an explicitly-empty search.sources list")
	}
}

func TestRegistry_AttentionSources_PopulatedList(t *testing.T) {
	reg, err := parseRegistry([]byte(`
attention:
  sources:
    - pg-connector-pr-github
    - pg-connector-attention-zr-stale-review
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	sources, err := reg.AttentionSources()
	if err != nil {
		t.Fatalf("AttentionSources: %v", err)
	}
	if len(sources) != 2 || sources[0] != "pg-connector-pr-github" || sources[1] != "pg-connector-attention-zr-stale-review" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestRegistry_SearchSources_PopulatedList(t *testing.T) {
	reg, err := parseRegistry([]byte(`
search:
  sources:
    - pg-connector-pr-github
    - pg-connector-issue-jira
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	sources, err := reg.SearchSources()
	if err != nil {
		t.Fatalf("SearchSources: %v", err)
	}
	if len(sources) != 2 || sources[0] != "pg-connector-pr-github" || sources[1] != "pg-connector-issue-jira" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestRegistry_AttentionSources_RejectsInvalidEntry(t *testing.T) {
	reg, err := parseRegistry([]byte(`
attention:
  sources:
    - ../evil
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.AttentionSources(); err == nil {
		t.Fatal("expected an error for a backend name containing a path separator")
	}
}

func TestRegistry_SearchSources_RejectsEmptyNameEntry(t *testing.T) {
	reg, err := parseRegistry([]byte(`
search:
  sources:
    - ""
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.SearchSources(); err == nil {
		t.Fatal("expected an error for an empty backend name")
	}
}

func TestRegistry_AttentionSources_RejectsDuplicateEntry(t *testing.T) {
	reg, err := parseRegistry([]byte(`
attention:
  sources:
    - pg-connector-pr-github
    - pg-connector-pr-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.AttentionSources(); err == nil {
		t.Fatal("expected an error for a duplicate backend name in one list")
	}
}

func TestRegistry_SearchSources_RejectsDuplicateEntry(t *testing.T) {
	reg, err := parseRegistry([]byte(`
search:
  sources:
    - pg-connector-issue-jira
    - pg-connector-issue-jira
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := reg.SearchSources(); err == nil {
		t.Fatal("expected an error for a duplicate backend name in one list")
	}
}

func TestRegistry_AttentionSources_SharedNameWithConnectorTypeSucceeds(t *testing.T) {
	// Cross-registration is explicitly authorized: a backend implementing
	// list_attention alongside its normal entity-type ops may appear under
	// both connector.<type> and attention.sources with no error.
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - pg-connector-pr-github
attention:
  sources:
    - pg-connector-pr-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	prBackends, err := reg.List("pr")
	if err != nil {
		t.Fatalf("List(pr): %v", err)
	}
	if len(prBackends) != 1 || prBackends[0] != "pg-connector-pr-github" {
		t.Fatalf("prBackends = %+v", prBackends)
	}
	attentionSources, err := reg.AttentionSources()
	if err != nil {
		t.Fatalf("AttentionSources: %v", err)
	}
	if len(attentionSources) != 1 || attentionSources[0] != "pg-connector-pr-github" {
		t.Fatalf("attentionSources = %+v", attentionSources)
	}
}

func TestRegistry_SearchSources_SharedNameWithConnectorTypeSucceeds(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  issue:
    - pg-connector-issue-jira
search:
  sources:
    - pg-connector-issue-jira
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	issueBackends, err := reg.List("issue")
	if err != nil {
		t.Fatalf("List(issue): %v", err)
	}
	if len(issueBackends) != 1 || issueBackends[0] != "pg-connector-issue-jira" {
		t.Fatalf("issueBackends = %+v", issueBackends)
	}
	searchSources, err := reg.SearchSources()
	if err != nil {
		t.Fatalf("SearchSources: %v", err)
	}
	if len(searchSources) != 1 || searchSources[0] != "pg-connector-issue-jira" {
		t.Fatalf("searchSources = %+v", searchSources)
	}
}

func TestRegistry_AttentionSourcesAndSearchSources_AreIndependentTopLevelKeys(t *testing.T) {
	// attention.sources/search.sources are siblings of connector:, never
	// nested inside it. AllBackends()'s existing behavior (connector.<type>
	// only) stays unchanged by this packet.
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - pg-connector-pr-github
attention:
  sources:
    - pg-connector-attention-zr-stale-review
search:
  sources:
    - pg-connector-issue-jira
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	all, err := reg.AllBackends()
	if err != nil {
		t.Fatalf("AllBackends: %v", err)
	}
	if len(all) != 1 || all[0] != "pg-connector-pr-github" {
		t.Fatalf("AllBackends = %+v, want only the connector.pr entry (attention/search sources not enumerated)", all)
	}
}

func TestRegistry_AttentionSources_TypoedSourcesSubKeyTreatedAsAbsent(t *testing.T) {
	// A mistyped sub-key under attention: (e.g. "souce" for "sources")
	// decodes to an empty Sources list today — the same "no entry"
	// behavior an absent attention: key already produces — rather than
	// being rejected. Tightening that is a separate, unscoped concern
	// [Binding decisions].
	reg, err := parseRegistry([]byte(`
attention:
  souce:
    - pg-connector-pr-github
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	sources, err := reg.AttentionSources()
	if err != nil || len(sources) != 0 {
		t.Fatalf("AttentionSources() = %v, %v", sources, err)
	}
}

func TestRegistry_BackendConfig_ReturnsVerbatimBlock(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - pg-connector-pr-github
backends:
  pg-connector-pr-github:
    rate_reserve_points: 500
    queries:
      team: "is:open author:@me"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	config, err := reg.BackendConfig("pg-connector-pr-github")
	if err != nil {
		t.Fatalf("BackendConfig: %v", err)
	}
	var decoded struct {
		RateReservePoints int               `json:"rate_reserve_points"`
		Queries           map[string]string `json:"queries"`
	}
	if err := json.Unmarshal(config, &decoded); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if decoded.RateReservePoints != 500 {
		t.Fatalf("rate_reserve_points = %d", decoded.RateReservePoints)
	}
	if decoded.Queries["team"] != "is:open author:@me" {
		t.Fatalf("queries = %+v", decoded.Queries)
	}
}

func TestRegistry_BackendConfig_AbsentReturnsNilNotError(t *testing.T) {
	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	config, err := reg.BackendConfig("no-such-backend")
	if err != nil {
		t.Fatalf("BackendConfig: %v", err)
	}
	if config != nil {
		t.Fatalf("config = %q, want nil", config)
	}
}

func TestRegistry_BackendConfig_NilRegistry(t *testing.T) {
	var reg *Registry
	config, err := reg.BackendConfig("anything")
	if err != nil || config != nil {
		t.Fatalf("BackendConfig on nil registry = %q, %v", config, err)
	}
}

func TestRegistry_BackendQueryNames_SortedList(t *testing.T) {
	reg, err := parseRegistry([]byte(`
backends:
  pg-connector-issue-beads:
    queries:
      ready: "ready"
      all-open: "list --status open"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	names, err := reg.BackendQueryNames("pg-connector-issue-beads")
	if err != nil {
		t.Fatalf("BackendQueryNames: %v", err)
	}
	if len(names) != 2 || names[0] != "all-open" || names[1] != "ready" {
		t.Fatalf("names = %v", names)
	}
}

func TestRegistry_BackendQueryNames_NoConfigBlock(t *testing.T) {
	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	names, err := reg.BackendQueryNames("no-such-backend")
	if err != nil || names != nil {
		t.Fatalf("BackendQueryNames = %v, %v", names, err)
	}
}

func TestRegistry_BackendQueryNames_ConfigWithNoQueriesKey(t *testing.T) {
	reg, err := parseRegistry([]byte(`
backends:
  pg-connector-pr-github:
    rate_reserve_points: 500
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	names, err := reg.BackendQueryNames("pg-connector-pr-github")
	if err != nil || names != nil {
		t.Fatalf("BackendQueryNames = %v, %v", names, err)
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
