package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// yamlTags returns the yaml tag name (before any comma option) for every
// field of typ, skipping the "-" (never-serialized) tag. typ must be a
// struct type.
func yamlTags(typ reflect.Type) []string {
	var tags []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		tags = append(tags, name)
	}
	sort.Strings(tags)
	return tags
}

// TestConfigCoversAllSection78Keys mechanically enforces the acceptance
// criterion "Config struct covers every section-7.8 key": it walks Config's
// own fields (plus the nested Repos/Agents/Jira/Urgency/Sync/Serve/Open
// struct types) and compares their yaml tags against the exact 21-key list
// pinned in this docket's packet contract (verbatim from the design's
// section 7.8 table,
// docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// lines 997-1011). A future rename or removal of any key fails this test
// loudly rather than silently shrinking coverage.
func TestConfigCoversAllSection78Keys(t *testing.T) {
	got := yamlTags(reflect.TypeOf(Config{}))
	want := []string{
		"self_login", "self_issue_owner", "team_members", "watch_labels", "repos",
		"ticket_patterns", "agents", "approver_allowlist",
		"verdict_generations", "check_interpreters",
		"ci_only_attempts_threshold", "review_exempt_checks", "area_labels", "jira", "category_vocabulary",
		"urgency", "agent_tracker_backend", "actor", "sync",
		"heartbeat_period", "stale_after", "serve", "open",
		// links (bead pg2-apuyx) postdates the section-7.8 table: the
		// read-only `links` verb's URL knobs.
		"links",
		// attention (bead pg2-5l0x4.2): the read-time attention evaluator's block.
		"attention",
		// freshness (bead pg2-ll4dw.2): the per-source data-age contract.
		"freshness",
		// Entity-change-flow keys (design 9.10).
		"watch", "sweep", "hydration", "change_log_retention", "consumer_stale_after",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Config top-level yaml keys:\n  got:  %v\n  want: %v", got, want)
	}

	// The three grouped keys that carry their own sub-keys per the section
	// 7.8 table: repos[] (remote, beads_dir), jira (high_priority_values,
	// incident_labels, incident_issue_types), urgency (labels, keywords,
	// thresholds), sync (mode), serve (addr, log), open (chrome_bin),
	// agents[] (login, approval_regex, policy).
	cases := []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{"RepoConfig", reflect.TypeOf(RepoConfig{}), []string{"remote", "beads_dir"}},
		{"AgentConfig", reflect.TypeOf(AgentConfig{}), []string{"login", "approval_regex", "policy"}},
		{"JiraConfig", reflect.TypeOf(JiraConfig{}), []string{"high_priority_values", "incident_labels", "incident_issue_types", "in_progress_statuses"}},
		{"UrgencyConfig", reflect.TypeOf(UrgencyConfig{}), []string{"labels", "keywords", "thresholds"}},
		// sync.retry (bead pg2-xb6fs) postdates the section-7.8 table: the
		// automatic-retry bounds for a recorded sync_error.
		{"SyncConfig", reflect.TypeOf(SyncConfig{}), []string{"mode", "retry"}},
		{"SyncRetryConfig", reflect.TypeOf(SyncRetryConfig{}), []string{"max_retries", "initial_backoff", "max_backoff"}},
		{"ServeConfig", reflect.TypeOf(ServeConfig{}), []string{"addr", "log"}},
		{"OpenConfig", reflect.TypeOf(OpenConfig{}), []string{"chrome_bin"}},
		{"LinksConfig", reflect.TypeOf(LinksConfig{}), []string{"issue_url_template"}},
		{"FreshnessConfig", reflect.TypeOf(FreshnessConfig{}), []string{"source_stale_after", "sources"}},
		{"FreshnessSourceConfig", reflect.TypeOf(FreshnessSourceConfig{}), []string{"label", "stale_after"}},
		{"AttentionConfig", reflect.TypeOf(AttentionConfig{}), []string{"rules", "ordering"}},
		{"AttentionRuleConfig", reflect.TypeOf(AttentionRuleConfig{}), []string{"enabled", "severity", "stale_after_days"}},
		{"AttentionOrderingConfig", reflect.TypeOf(AttentionOrderingConfig{}), []string{"ties"}},
		{"WatchConfig", reflect.TypeOf(WatchConfig{}), []string{"pr", "issue", "thread"}},
		{"WatchTypeConfig", reflect.TypeOf(WatchTypeConfig{}), []string{"queries"}},
		{"WatchThreadConfig", reflect.TypeOf(WatchThreadConfig{}), []string{"queries", "active_window"}},
		{"SweepConfig", reflect.TypeOf(SweepConfig{}), []string{"max_age", "max_per_poll", "reconcile_age"}},
		{"HydrationConfig", reflect.TypeOf(HydrationConfig{}), []string{"max_per_poll"}},
	}
	for _, c := range cases {
		gotSub := yamlTags(c.typ)
		wantSub := append([]string{}, c.want...)
		sort.Strings(wantSub)
		if !reflect.DeepEqual(gotSub, wantSub) {
			t.Errorf("%s yaml keys:\n  got:  %v\n  want: %v", c.name, gotSub, wantSub)
		}
	}
}

// fakeEnv injects fixed env-var + home-dir values into Load.
type fakeEnv struct {
	vars map[string]string
	home string
}

func (f fakeEnv) Getenv(k string) string       { return f.vars[k] }
func (f fakeEnv) UserHomeDir() (string, error) { return f.home, nil }

// testdataFixture returns the absolute path to a fixture under this
// package's testdata/ directory. go test runs with the package directory as
// the working directory, so a relative path works (runtime.Caller does not:
// -trimpath rewrites its file path to a module path).
func testdataFixture(name string) string {
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		panic(err)
	}
	return abs
}

// TestLoadFile_FullExample loads the checked-in testdata/config.example.yaml
// fixture — which declares every one of the 21 section-7.8 keys — and
// asserts each one parses into the expected field. This both proves Load
// actually works end-to-end against a real file and gives
// cmd/pg-desk/identifier_allowlist_test.go's guard a real fixture under
// testdata/ to cover (acceptance criterion: "identifier-allowlist guard
// extended to packages/pg-desk fixtures").
func TestLoadFile_FullExample(t *testing.T) {
	examplePath, beadsDir := stageExample(t)
	cfg, err := LoadFile(examplePath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if cfg.SelfLogin != "phillipgreenii" {
		t.Errorf("self_login: got %q", cfg.SelfLogin)
	}
	if want := []string{"phillipgreenii", "teammate"}; !reflect.DeepEqual(cfg.TeamMembers, want) {
		t.Errorf("team_members: got %v want %v", cfg.TeamMembers, want)
	}
	if want := []string{"needs-review"}; !reflect.DeepEqual(cfg.WatchLabels, want) {
		t.Errorf("watch_labels: got %v want %v", cfg.WatchLabels, want)
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("repos: expected 1 entry, got %d", len(cfg.Repos))
	}
	if cfg.Repos[0].Remote != "phillipgreenii/example-repo" {
		t.Errorf("repos[0].remote: got %q", cfg.Repos[0].Remote)
	}
	if cfg.Repos[0].BeadsDir != beadsDir {
		t.Errorf("repos[0].beads_dir: got %q", cfg.Repos[0].BeadsDir)
	}

	if want := []string{"PROJ-[0-9]+"}; !reflect.DeepEqual(cfg.TicketPatterns, want) {
		t.Errorf("ticket_patterns: got %v want %v", cfg.TicketPatterns, want)
	}

	if len(cfg.Agents) != 1 {
		t.Fatalf("agents: expected 1 entry, got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].Login != "review-bot" || cfg.Agents[0].Policy != "advisory" {
		t.Errorf("agents[0]: got %+v", cfg.Agents[0])
	}

	if want := []string{"teammate"}; !reflect.DeepEqual(cfg.ApproverAllowlist, want) {
		t.Errorf("approver_allowlist: got %v want %v", cfg.ApproverAllowlist, want)
	}

	if len(cfg.VerdictGenerations) != 1 || cfg.VerdictGenerations[0].ID != "v1" {
		t.Errorf("verdict_generations: got %+v", cfg.VerdictGenerations)
	}

	if len(cfg.CheckInterpreters) != 1 || cfg.CheckInterpreters[0].Type != "approval-gate" {
		t.Errorf("check_interpreters: got %+v", cfg.CheckInterpreters)
	}

	if want := []string{"slow-nightly"}; !reflect.DeepEqual(cfg.ReviewExemptChecks, want) {
		t.Errorf("review_exempt_checks: got %v want %v", cfg.ReviewExemptChecks, want)
	}

	if len(cfg.AreaLabels) != 2 || cfg.AreaLabels[1].Field != "branch" || !reflect.DeepEqual(cfg.AreaLabels[0].Labels, []string{"alpha"}) {
		t.Errorf("area_labels: got %+v", cfg.AreaLabels)
	}

	if cfg.CIOnlyAttemptsThreshold != 3 {
		t.Errorf("ci_only_attempts_threshold: got %d", cfg.CIOnlyAttemptsThreshold)
	}

	if cfg.Jira == nil {
		t.Fatal("jira: expected non-nil")
	}
	if want := []string{"Highest", "High"}; !reflect.DeepEqual(cfg.Jira.HighPriorityValues, want) {
		t.Errorf("jira.high_priority_values: got %v want %v", cfg.Jira.HighPriorityValues, want)
	}
	if want := []string{"incident"}; !reflect.DeepEqual(cfg.Jira.IncidentLabels, want) {
		t.Errorf("jira.incident_labels: got %v want %v", cfg.Jira.IncidentLabels, want)
	}
	if want := []string{"Incident"}; !reflect.DeepEqual(cfg.Jira.IncidentIssueTypes, want) {
		t.Errorf("jira.incident_issue_types: got %v want %v", cfg.Jira.IncidentIssueTypes, want)
	}
	if want := []string{"In Progress", "Doing"}; !reflect.DeepEqual(cfg.Jira.InProgressStatuses, want) {
		t.Errorf("jira.in_progress_statuses: got %v want %v", cfg.Jira.InProgressStatuses, want)
	}

	if want := []string{"fix", "bug"}; !reflect.DeepEqual(cfg.CategoryVocabulary["bugfix"], want) {
		t.Errorf("category_vocabulary[bugfix]: got %v want %v", cfg.CategoryVocabulary["bugfix"], want)
	}

	if cfg.Urgency == nil {
		t.Fatal("urgency: expected non-nil")
	}
	if want := []string{"urgent"}; !reflect.DeepEqual(cfg.Urgency.Labels, want) {
		t.Errorf("urgency.labels: got %v want %v", cfg.Urgency.Labels, want)
	}
	if want := []string{"outage"}; !reflect.DeepEqual(cfg.Urgency.Keywords, want) {
		t.Errorf("urgency.keywords: got %v want %v", cfg.Urgency.Keywords, want)
	}
	if cfg.Urgency.Thresholds["high"] != 3 || cfg.Urgency.Thresholds["critical"] != 5 {
		t.Errorf("urgency.thresholds: got %v", cfg.Urgency.Thresholds)
	}

	if cfg.AgentTrackerBackend != "beads" {
		t.Errorf("agent_tracker_backend: got %q", cfg.AgentTrackerBackend)
	}
	if cfg.Actor != "pg-desk" {
		t.Errorf("actor: got %q", cfg.Actor)
	}

	if cfg.Sync.Mode != "off" {
		t.Errorf("sync.mode: got %q", cfg.Sync.Mode)
	}
	maxRetries, initialBackoff, maxBackoff, err := cfg.Sync.Retry.Resolve()
	if err != nil {
		t.Fatalf("sync.retry: Resolve: %v", err)
	}
	if maxRetries != 5 || initialBackoff != 2*time.Minute || maxBackoff != time.Hour {
		t.Errorf("sync.retry: got (%d, %s, %s), want (5, 2m0s, 1h0m0s)", maxRetries, initialBackoff, maxBackoff)
	}

	if cfg.HeartbeatPeriod != "5m" {
		t.Errorf("heartbeat_period: got %q", cfg.HeartbeatPeriod)
	}
	if cfg.StaleAfter != "30m" {
		t.Errorf("stale_after: got %q", cfg.StaleAfter)
	}

	if cfg.Serve.Addr != "127.0.0.1:8090" {
		t.Errorf("serve.addr: got %q", cfg.Serve.Addr)
	}
	if !strings.HasSuffix(cfg.Serve.Log, "/Library/Logs/pg-desk-serve.log") {
		t.Errorf("serve.log: got %q", cfg.Serve.Log)
	}
	if strings.HasPrefix(cfg.Serve.Log, "~") {
		t.Errorf("serve.log: tilde not expanded: %q", cfg.Serve.Log)
	}

	if cfg.Open.ChromeBin != "/usr/bin/example-browser" {
		t.Errorf("open.chrome_bin: got %q", cfg.Open.ChromeBin)
	}

	// Entity-change-flow keys (design 9.10).
	if got := cfg.WatchQueries("pr"); !reflect.DeepEqual(got, []string{"mine", "team"}) {
		t.Errorf("watch.pr.queries: got %v", got)
	}
	if got := cfg.WatchQueries("issue"); !reflect.DeepEqual(got, []string{"assigned-to-me", "work-items"}) {
		t.Errorf("watch.issue.queries: got %v", got)
	}
	if got := cfg.WatchQueries("thread"); !reflect.DeepEqual(got, []string{"mentions"}) {
		t.Errorf("watch.thread.queries: got %v", got)
	}
	if cfg.ThreadActiveWindow() != 7*24*time.Hour || cfg.SweepMaxAge() != 6*time.Hour ||
		cfg.SweepMaxPerPoll() != 20 || cfg.HydrationMaxPerPoll() != 50 ||
		cfg.ReconcileAge() != 30*time.Minute ||
		cfg.ChangeLogRetention() != 14*24*time.Hour || cfg.ConsumerStaleAfter() != 7*24*time.Hour {
		t.Errorf("change-flow scalars not parsed: %+v", cfg)
	}

	if cfg.SourceStaleAfter() != 15*time.Minute || cfg.Freshness.Sources["pr-github"].Label != "PRs" || cfg.Freshness.Sources["pr-github"].StaleAfter != "30m" {
		t.Errorf("freshness block not parsed: %+v", cfg.Freshness)
	}

	if cfg.Path != examplePath {
		t.Errorf("cfg.Path: got %q", cfg.Path)
	}
}

// makeBeadsDir creates a minimal valid beads workspace directory (a
// .beads dir carrying config.yaml) under dir and returns its path.
func makeBeadsDir(t *testing.T, dir string) string {
	t.Helper()
	b := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(b, "config.yaml"), "issue_prefix: x\n"); err != nil {
		t.Fatal(err)
	}
	return b
}

// stageExample copies testdata/config.example.yaml into a temp dir with its
// placeholder beads_dir rewritten to a real (temp) beads workspace, since
// config load now requires every beads_dir to exist. Returns the staged
// config path and the substituted beads_dir.
func stageExample(t *testing.T) (string, string) {
	t.Helper()
	raw, err := os.ReadFile(testdataFixture("config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	beadsDir := makeBeadsDir(t, dir)
	out := strings.Replace(string(raw), "/tmp/example-repo/.beads", beadsDir, 1)
	if out == string(raw) {
		t.Fatal("fixture placeholder beads_dir not found")
	}
	p := filepath.Join(dir, "example.yaml")
	if err := writeFile(p, out); err != nil {
		t.Fatal(err)
	}
	return p, beadsDir
}

func TestLoadFile_BeadsDirMissingFailsNamingRepoAndPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone", ".beads")
	p := writeYAML(t, dir, "self_login: phillipgreenii\nrepos:\n  - remote: phillipgreenii/example-repo\n    beads_dir: "+missing+"\n")
	_, err := LoadFile(p)
	if err == nil {
		t.Fatal("expected an error for a nonexistent beads_dir")
	}
	for _, want := range []string{"phillipgreenii/example-repo", missing, "beads_dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
}

func TestLoadFile_BeadsDirNotADirectoryFails(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file")
	if err := writeFile(f, "x"); err != nil {
		t.Fatal(err)
	}
	p := writeYAML(t, dir, "self_login: a\nrepos:\n  - remote: o/r\n    beads_dir: "+f+"\n")
	if _, err := LoadFile(p); err == nil || !strings.Contains(err.Error(), f) {
		t.Fatalf("expected a not-a-directory error naming %s, got %v", f, err)
	}
}

func TestLoadFile_BeadsDirNotABeadsWorkspaceFails(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "plain")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	p := writeYAML(t, dir, "self_login: a\nrepos:\n  - remote: o/r\n    beads_dir: "+empty+"\n")
	_, err := LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), empty) || !strings.Contains(err.Error(), "o/r") {
		t.Fatalf("expected a not-a-beads-workspace error naming repo and path, got %v", err)
	}
}

func TestLoadFile_BeadsDirValidWorkspaceLoads(t *testing.T) {
	dir := t.TempDir()
	b := makeBeadsDir(t, dir)
	p := writeYAML(t, dir, "self_login: a\nrepos:\n  - remote: o/r\n    beads_dir: "+b+"\n")
	if _, err := LoadFile(p); err != nil {
		t.Fatalf("valid beads workspace should load: %v", err)
	}
}

func writeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	if err := writeFile(p, content); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSyncRetryResolve_Defaults: an absent sync.retry block resolves to the
// documented defaults (10 retries, 1m initial backoff, 30m cap).
func TestSyncRetryResolve_Defaults(t *testing.T) {
	maxRetries, initialBackoff, maxBackoff, err := SyncRetryConfig{}.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if maxRetries != 10 || initialBackoff != time.Minute || maxBackoff != 30*time.Minute {
		t.Fatalf("defaults = (%d, %s, %s), want (10, 1m0s, 30m0s)", maxRetries, initialBackoff, maxBackoff)
	}
}

// TestSyncRetryResolve_ExplicitZeroDisablesRetry: max_retries: 0 is honored
// (not replaced by the default).
func TestSyncRetryResolve_ExplicitZeroDisablesRetry(t *testing.T) {
	zero := 0
	maxRetries, _, _, err := SyncRetryConfig{MaxRetries: &zero}.Resolve()
	if err != nil || maxRetries != 0 {
		t.Fatalf("Resolve(max_retries: 0) = (%d, %v), want (0, nil)", maxRetries, err)
	}
}

func TestLoadFile_SyncRetryInvalidValuesFail(t *testing.T) {
	for name, block := range map[string]string{
		"negative max_retries":      "max_retries: -1",
		"unparseable backoff":       "initial_backoff: soon",
		"zero backoff":              "initial_backoff: 0s",
		"negative max_backoff":      "max_backoff: -5m",
		"max below initial backoff": "initial_backoff: 10m\n    max_backoff: 5m",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeYAML(t, dir, "self_login: a\nrepos:\n  - remote: o/r\nsync:\n  retry:\n    "+block+"\n")
			_, err := LoadFile(p)
			if err == nil || !strings.Contains(err.Error(), "sync.retry") {
				t.Fatalf("LoadFile: err = %v, want a sync.retry validation error", err)
			}
		})
	}
}

func TestLoadFile_Minimal(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipgreenii
repos:
  - remote: phillipgreenii/example-repo
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.SelfLogin != "phillipgreenii" {
		t.Fatalf("self_login: got %q", cfg.SelfLogin)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Remote != "phillipgreenii/example-repo" {
		t.Fatalf("repos: got %+v", cfg.Repos)
	}
	if cfg.Path != p {
		t.Fatalf("cfg.Path: got %q want %q", cfg.Path, p)
	}
}

func TestLoadFile_RequiresSelfLogin(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
repos:
  - remote: phillipgreenii/example-repo
`)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("expected an error for missing self_login")
	}
}

func TestLoadFile_RequiresAtLeastOneRepo(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipgreenii
`)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("expected an error for missing repos")
	}
}

func TestLoadFile_RequiresRepoRemote(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipgreenii
repos:
  - beads_dir: /tmp/somewhere
`)
	if _, err := LoadFile(p); err == nil {
		t.Fatal("expected an error for a repo with no remote")
	}
}

func TestLoadFile_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFile(filepath.Join(dir, "does-not-exist.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected a not-exist error, got %v", err)
	}
}

func TestLoadFromEnv_ExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipgreenii
repos:
  - remote: phillipgreenii/example-repo
`)
	env := fakeEnv{vars: map[string]string{"PG_DESK_CONFIG": p}}
	cfg, err := LoadFromEnv(env)
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.Path != p {
		t.Fatalf("cfg.Path: got %q want %q", cfg.Path, p)
	}
}

func TestLoadFromEnv_ExplicitOverrideMissing(t *testing.T) {
	dir := t.TempDir()
	env := fakeEnv{vars: map[string]string{"PG_DESK_CONFIG": filepath.Join(dir, "nope.yaml")}}
	if _, err := LoadFromEnv(env); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "PG_DESK_CONFIG") {
		t.Fatalf("expected the error to name $PG_DESK_CONFIG, got %v", err)
	}
}

func TestLoadFromEnv_XDGFallback(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	if err := writeFile(filepath.Join(xdg, "pg-desk", "config.yaml"), `
self_login: phillipgreenii
repos:
  - remote: phillipgreenii/example-repo
`); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv{vars: map[string]string{"XDG_CONFIG_HOME": xdg}}
	cfg, err := LoadFromEnv(env)
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.SelfLogin != "phillipgreenii" {
		t.Fatalf("self_login: got %q", cfg.SelfLogin)
	}
}

func TestLoadFromEnv_NoConfigFound(t *testing.T) {
	dir := t.TempDir()
	env := fakeEnv{home: filepath.Join(dir, "home")}
	_, err := LoadFromEnv(env)
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("expected ErrNoConfig, got %v", err)
	}
}

// writeFile writes content to path, creating parent directories as needed.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestLoadFile_LinksIssueURLTemplate(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, "self_login: a\nrepos:\n  - remote: o/r\nlinks:\n  issue_url_template: https://tracker.example.invalid/browse/%s\n")
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got, want := cfg.Links.IssueURLTemplate, "https://tracker.example.invalid/browse/%s"; got != want {
		t.Errorf("links.issue_url_template = %q; want %q", got, want)
	}
}

func TestLoadFile_LinksIssueURLTemplateInvalidFails(t *testing.T) {
	for name, tmpl := range map[string]string{
		"no placeholder":         "https://tracker.example.invalid/browse/",
		"two placeholders":       "https://tracker.example.invalid/%s/%s",
		"non-http scheme":        "javascript:alert(%s)",
		"relative":               "/browse/%s",
		"other printf verb":      "https://tracker.example.invalid/%d/%s",
		"whitespace in template": "https://tracker.example.invalid/a b/%s",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeYAML(t, dir, "self_login: a\nrepos:\n  - remote: o/r\nlinks:\n  issue_url_template: \""+tmpl+"\"\n")
			_, err := LoadFile(p)
			if err == nil || !strings.Contains(err.Error(), "links.issue_url_template") {
				t.Fatalf("LoadFile: err = %v, want a links.issue_url_template validation error", err)
			}
		})
	}
}

const changeFlowBase = "self_login: a\nrepos:\n  - remote: o/r\n"

func TestChangeFlowKeys_Defaults(t *testing.T) {
	cfg, err := LoadFile(writeYAML(t, t.TempDir(), changeFlowBase))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	for _, typ := range []string{"pr", "issue", "thread", "bogus"} {
		if q := cfg.WatchQueries(typ); q != nil {
			t.Errorf("WatchQueries(%q) = %v, want nil", typ, q)
		}
	}
	if got := cfg.ThreadActiveWindow(); got != 7*24*time.Hour {
		t.Errorf("ThreadActiveWindow = %v, want 7d", got)
	}
	if got := cfg.SweepMaxAge(); got != 6*time.Hour {
		t.Errorf("SweepMaxAge = %v, want 6h", got)
	}
	if got := cfg.SweepMaxPerPoll(); got != 20 {
		t.Errorf("SweepMaxPerPoll = %d, want 20", got)
	}
	if got := cfg.ReconcileAge(); got != 30*time.Minute {
		t.Errorf("ReconcileAge = %v, want 30m", got)
	}
	if got := cfg.HydrationMaxPerPoll(); got != 50 {
		t.Errorf("HydrationMaxPerPoll = %d, want 50", got)
	}
	if got := cfg.ChangeLogRetention(); got != 0 {
		t.Errorf("ChangeLogRetention = %v, want 0", got)
	}
	if got := cfg.ConsumerStaleAfter(); got != 0 {
		t.Errorf("ConsumerStaleAfter = %v, want 0", got)
	}
}

func TestChangeFlowKeys_Explicit(t *testing.T) {
	cfg, err := LoadFile(writeYAML(t, t.TempDir(), changeFlowBase+`
watch:
  pr:
    queries: [mine, team]
  issue:
    queries: [assigned-to-me, work-items]
  thread:
    queries: [mentions]
    active_window: 3d
sweep:
  max_age: 90m
  reconcile_age: 10m
  max_per_poll: 5
hydration:
  max_per_poll: 9
change_log_retention: 14d
consumer_stale_after: 36h
`))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.WatchQueries("pr"); !reflect.DeepEqual(got, []string{"mine", "team"}) {
		t.Errorf("pr queries = %v", got)
	}
	if got := cfg.WatchQueries("issue"); !reflect.DeepEqual(got, []string{"assigned-to-me", "work-items"}) {
		t.Errorf("issue queries = %v", got)
	}
	if got := cfg.WatchQueries("thread"); !reflect.DeepEqual(got, []string{"mentions"}) {
		t.Errorf("thread queries = %v", got)
	}
	if got := cfg.ThreadActiveWindow(); got != 3*24*time.Hour {
		t.Errorf("ThreadActiveWindow = %v", got)
	}
	if got := cfg.SweepMaxAge(); got != 90*time.Minute {
		t.Errorf("SweepMaxAge = %v", got)
	}
	if got := cfg.SweepMaxPerPoll(); got != 5 {
		t.Errorf("SweepMaxPerPoll = %d", got)
	}
	if got := cfg.ReconcileAge(); got != 10*time.Minute {
		t.Errorf("ReconcileAge = %v, want the lowered 10m", got)
	}
	if got := cfg.HydrationMaxPerPoll(); got != 9 {
		t.Errorf("HydrationMaxPerPoll = %d", got)
	}
	if got := cfg.ChangeLogRetention(); got != 14*24*time.Hour {
		t.Errorf("ChangeLogRetention = %v", got)
	}
	if got := cfg.ConsumerStaleAfter(); got != 36*time.Hour {
		t.Errorf("ConsumerStaleAfter = %v", got)
	}
}

func TestParseDayDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"7d":     7 * 24 * time.Hour,
		"14d":    14 * 24 * time.Hour,
		"6h":     6 * time.Hour,
		"1d12h":  36 * time.Hour,
		"0.5d":   12 * time.Hour,
		" 2d ":   48 * time.Hour,
		"1500ms": 1500 * time.Millisecond,
	} {
		got, err := parseDayDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDayDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"d", "xd", "7", "-1d", "0d", "0h", "-5h", "1d-2h", "7dd", "abc"} {
		if d, err := parseDayDuration(in); err == nil {
			t.Errorf("parseDayDuration(%q) = %v, want error", in, d)
		}
	}
}

func TestLoadFile_ChangeFlowInvalidValuesFail(t *testing.T) {
	for name, tc := range map[string]struct{ block, key string }{
		"malformed active_window": {"watch:\n  thread:\n    active_window: soon", "watch.thread.active_window"},
		"negative active_window":  {"watch:\n  thread:\n    active_window: -1d", "watch.thread.active_window"},
		"malformed max_age":       {"sweep:\n  max_age: xyz", "sweep.max_age"},
		"malformed reconcile_age": {"sweep:\n  reconcile_age: xyz", "sweep.reconcile_age"},
		"zero reconcile_age":      {"sweep:\n  reconcile_age: 0s", "sweep.reconcile_age"},
		"malformed retention":     {"change_log_retention: 14", "change_log_retention"},
		"zero retention":          {"change_log_retention: 0d", "change_log_retention"},
		"malformed stale after":   {"consumer_stale_after: forever", "consumer_stale_after"},
		"zero sweep cap":          {"sweep:\n  max_per_poll: 0", "sweep.max_per_poll"},
		"negative sweep cap":      {"sweep:\n  max_per_poll: -3", "sweep.max_per_poll"},
		"zero hydration cap":      {"hydration:\n  max_per_poll: 0", "hydration.max_per_poll"},
		"negative hydration cap":  {"hydration:\n  max_per_poll: -1", "hydration.max_per_poll"},
		"empty pr query":          {"watch:\n  pr:\n    queries: [mine, \"\"]", "watch.pr.queries"},
		"empty issue query":       {"watch:\n  issue:\n    queries: [\" \"]", "watch.issue.queries"},
		"duplicate thread query":  {"watch:\n  thread:\n    queries: [mentions, mentions]", "watch.thread.queries"},
		"duplicate pr query":      {"watch:\n  pr:\n    queries: [a, b, a]", "watch.pr.queries"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFile(writeYAML(t, t.TempDir(), changeFlowBase+tc.block+"\n"))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("LoadFile: err = %v, want an error naming %q", err, tc.key)
			}
		})
	}
}

// TestLoadFile_LegacyKeysStillLoad pins that the keys slated for removal in
// Phase 10 (sync, agent_tracker_backend, heartbeat_period) still load next to
// the new change-flow keys, so the primary branch stays deployable.
func TestLoadFile_LegacyKeysStillLoad(t *testing.T) {
	cfg, err := LoadFile(writeYAML(t, t.TempDir(), changeFlowBase+`
sync:
  mode: off
agent_tracker_backend: beads
heartbeat_period: 5m
watch:
  pr:
    queries: [mine]
`))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Sync.Mode != "off" || cfg.AgentTrackerBackend != "beads" || cfg.HeartbeatPeriod != "5m" {
		t.Fatalf("legacy keys not preserved: %+v", cfg)
	}
}

func TestLoadFile_AttentionBlock(t *testing.T) {
	dir := t.TempDir()
	base := "self_login: me\nrepos:\n  - remote: acme/api\n"

	cfg, err := LoadFile(writeYAML(t, dir, base))
	if err != nil {
		t.Fatalf("a config with no attention block must load: %v", err)
	}
	if len(cfg.Attention.Rules) != 0 {
		t.Errorf("no attention block must leave Rules empty, got %v", cfg.Attention.Rules)
	}

	cfg, err = LoadFile(writeYAML(t, dir, base+"attention:\n  rules:\n    pr.own-ci-failing:\n      enabled: false\n      severity: low\n"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	r := cfg.Attention.Rules["pr.own-ci-failing"]
	if r.Enabled == nil || *r.Enabled || r.Severity != "low" {
		t.Errorf("attention rule not parsed: %+v", r)
	}

	_, err = LoadFile(writeYAML(t, dir, base+"attention:\n  rules:\n    pr.own-ci-failing:\n      severity: urgent\n"))
	if err == nil || !strings.Contains(err.Error(), "attention.rules.pr.own-ci-failing.severity") {
		t.Errorf("a severity outside low|medium|high must be rejected naming the key, got %v", err)
	}

	cfg, err = LoadFile(writeYAML(t, dir, base+"attention:\n  rules:\n    issue.stale-in-progress:\n      stale_after_days: 10\n"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if d := cfg.Attention.Rules["issue.stale-in-progress"].StaleAfterDays; d == nil || *d != 10 {
		t.Errorf("stale_after_days not parsed: %v", d)
	}
	for _, bad := range []string{"0", "-3"} {
		_, err = LoadFile(writeYAML(t, dir, base+"attention:\n  rules:\n    issue.stale-in-progress:\n      stale_after_days: "+bad+"\n"))
		if err == nil || !strings.Contains(err.Error(), "attention.rules.issue.stale-in-progress.stale_after_days") {
			t.Errorf("stale_after_days %s must be rejected naming the key, got %v", bad, err)
		}
	}
}

func TestInProgressStatuses(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want []string
	}{
		{"nil config", nil, DefaultInProgressStatuses},
		{"no jira block", &Config{}, DefaultInProgressStatuses},
		{"empty list takes the default", &Config{Jira: &JiraConfig{}}, DefaultInProgressStatuses},
		{"configured list replaces the default", &Config{Jira: &JiraConfig{InProgressStatuses: []string{"Doing", "Review"}}}, []string{"Doing", "Review"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.InProgressStatuses(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("InProgressStatuses() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFreshnessKeys_DefaultsAndExplicit(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadFile(writeYAML(t, dir, changeFlowBase))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.SourceStaleAfter(); got != 15*time.Minute {
		t.Errorf("default SourceStaleAfter = %v, want 15m", got)
	}
	var nilCfg *Config
	if got := nilCfg.SourceStaleAfter(); got != DefaultSourceStaleAfter {
		t.Errorf("nil Config SourceStaleAfter = %v, want the default", got)
	}

	cfg, err = LoadFile(writeYAML(t, t.TempDir(), changeFlowBase+`freshness:
  source_stale_after: 1h
  sources:
    pr-github:
      label: PRs
      stale_after: 30m
`))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.SourceStaleAfter(); got != time.Hour {
		t.Errorf("SourceStaleAfter = %v, want 1h", got)
	}
	if got := cfg.Freshness.Sources["pr-github"]; got.Label != "PRs" || got.StaleAfter != "30m" {
		t.Errorf("sources[pr-github] = %+v", got)
	}
}

func TestFreshnessKeys_InvalidFail(t *testing.T) {
	for name, tc := range map[string]struct{ block, key string }{
		"unparseable threshold": {"freshness:\n  source_stale_after: soon\n", "freshness.source_stale_after"},
		"zero threshold":        {"freshness:\n  source_stale_after: 0s\n", "freshness.source_stale_after"},
		"negative threshold":    {"freshness:\n  source_stale_after: -5m\n", "freshness.source_stale_after"},
		"bad per-source":        {"freshness:\n  sources:\n    pr-github:\n      stale_after: later\n", "freshness.sources.pr-github.stale_after"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFile(writeYAML(t, t.TempDir(), changeFlowBase+tc.block))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("LoadFile: err = %v, want an error naming %s", err, tc.key)
			}
		})
	}
}
