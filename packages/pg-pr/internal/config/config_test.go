package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadFile_ClaudeBin(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipg
worktree_root: /tmp/wr
repos:
  - remote: owner/name
claude_bin: /run/current-system/sw/bin/claude
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.ClaudeBin != "/run/current-system/sw/bin/claude" {
		t.Fatalf("claude_bin: got %q want /run/current-system/sw/bin/claude", cfg.ClaudeBin)
	}
}

func TestLoadFile_ClaudeBinDefaultsToEmpty(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipg
worktree_root: /tmp/wr
repos:
  - remote: owner/name
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.ClaudeBin != "" {
		t.Fatalf("claude_bin: got %q want empty (unset)", cfg.ClaudeBin)
	}
}

func TestLoadFile_AgentsBlock(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipg
worktree_root: /tmp/wr
repos:
  - remote: owner/name
agents:
  - login: claude[bot]
    approval_regex: '(?im)^verdict:\s*approve'
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(cfg.Agents))
	}
	a := cfg.Agents[0]
	if a.Login != "claude[bot]" {
		t.Errorf("login: got %q want %q", a.Login, "claude[bot]")
	}
	if a.ApprovalRegex != `(?im)^verdict:\s*approve` {
		t.Errorf("approval_regex: got %q", a.ApprovalRegex)
	}
}

// fakeEnv injects fixed env-var + home-dir values into Load.
type fakeEnv struct {
	vars map[string]string
	home string
}

func (f fakeEnv) Getenv(k string) string       { return f.vars[k] }
func (f fakeEnv) UserHomeDir() (string, error) { return f.home, nil }

// writeYAML writes content to <dir>/config.yaml and returns the full path.
func writeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalYAML = `
self_login: phillipg
worktree_root: ~/Code/reviews
repos:
  - path: /tmp/some-repo
    remote: owner/name
    vcs: github
    team_members: [phillipg, alice]
    watch_labels: [needs-review]
`

func TestLoadFile_Minimal(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, minimalYAML)

	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.SelfLogin != "phillipg" {
		t.Fatalf("self_login: got %q", cfg.SelfLogin)
	}
	if !strings.HasSuffix(cfg.WorktreeRoot, "/Code/reviews") {
		t.Fatalf("worktree_root not expanded: %q", cfg.WorktreeRoot)
	}
	if strings.HasPrefix(cfg.WorktreeRoot, "~") {
		t.Fatalf("worktree_root still contains tilde: %q", cfg.WorktreeRoot)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(cfg.Repos))
	}
	r := cfg.Repos[0]
	if r.Remote != "owner/name" {
		t.Fatalf("remote: got %q", r.Remote)
	}
	if r.VCS != "github" {
		t.Fatalf("vcs: got %q", r.VCS)
	}
	if len(r.TeamMembers) != 2 || r.TeamMembers[0] != "phillipg" {
		t.Fatalf("team_members: got %v", r.TeamMembers)
	}
	if cfg.Path != p {
		t.Fatalf("cfg.Path: got %q want %q", cfg.Path, p)
	}
}

func TestLoadFile_DefaultsVCSToGitHub(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: phillipg
worktree_root: /tmp/wr
repos:
  - remote: owner/name
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Repos[0].VCS != "github" {
		t.Fatalf("VCS default: got %q want github", cfg.Repos[0].VCS)
	}
}

func TestLoadFile_Validation(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "missing self_login",
			body: `
worktree_root: /tmp/wr
repos:
  - remote: a/b
`,
			want: "self_login",
		},
		{
			name: "missing worktree_root",
			body: `
self_login: x
repos:
  - remote: a/b
`,
			want: "worktree_root",
		},
		{
			name: "no repos",
			body: `
self_login: x
worktree_root: /tmp/wr
`,
			want: "at least one repo",
		},
		{
			name: "missing remote",
			body: `
self_login: x
worktree_root: /tmp/wr
repos:
  - vcs: github
`,
			want: "remote is required",
		},
		{
			name: "remote not owner/name",
			body: `
self_login: x
worktree_root: /tmp/wr
repos:
  - remote: notownername
`,
			want: "owner/name",
		},
		{
			name: "duplicate remote",
			body: `
self_login: x
worktree_root: /tmp/wr
repos:
  - remote: a/b
  - remote: a/b
`,
			want: "duplicate remote",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeYAML(t, dir, tc.body)
			_, err := LoadFile(p)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error msg: got %q want contains %q", err, tc.want)
			}
		})
	}
}

func TestLoadFromEnv_PGPRConfigOverride(t *testing.T) {
	dir := t.TempDir()
	override := writeYAML(t, dir, minimalYAML)

	cfg, err := LoadFromEnv(fakeEnv{
		vars: map[string]string{"PG_PR_CONFIG": override},
		home: dir,
	})
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.Path != override {
		t.Fatalf("path: got %q want %q", cfg.Path, override)
	}
}

func TestLoadFromEnv_PGPRConfigMissingErrors(t *testing.T) {
	_, err := LoadFromEnv(fakeEnv{
		vars: map[string]string{"PG_PR_CONFIG": "/nonexistent/pg-pr.yaml"},
	})
	if err == nil {
		t.Fatalf("expected error for missing PG_PR_CONFIG path")
	}
	if !strings.Contains(err.Error(), "PG_PR_CONFIG") {
		t.Fatalf("error msg: got %q want references PG_PR_CONFIG", err)
	}
}

func TestLoadFromEnv_XDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	_ = writeYAML(t, filepath.Join(xdg, "pg-pr"), minimalYAML)

	cfg, err := LoadFromEnv(fakeEnv{
		vars: map[string]string{"XDG_CONFIG_HOME": xdg},
		home: "/home/should-not-be-used",
	})
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if !strings.Contains(cfg.Path, xdg) {
		t.Fatalf("expected path under %q, got %q", xdg, cfg.Path)
	}
}

func TestLoadFromEnv_HomeDotConfig(t *testing.T) {
	home := t.TempDir()
	_ = writeYAML(t, filepath.Join(home, ".config", "pg-pr"), minimalYAML)

	cfg, err := LoadFromEnv(fakeEnv{
		vars: map[string]string{},
		home: home,
	})
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if !strings.Contains(cfg.Path, ".config/pg-pr/config.yaml") {
		t.Fatalf("path: got %q want under ~/.config/pg-pr", cfg.Path)
	}
}

func TestLoadFromEnv_NoConfigErrorMentionsPaths(t *testing.T) {
	home := t.TempDir() // no config under home
	_, err := LoadFromEnv(fakeEnv{vars: map[string]string{}, home: home})
	if err == nil {
		t.Fatalf("expected ErrNoConfig")
	}
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("expected ErrNoConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), home) {
		t.Fatalf("expected error to mention checked path %q, got %q", home, err)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/abs/path", "/abs/path"},
		{"~", home},
		{"~/sub", filepath.Join(home, "sub")},
		{"./rel", "./rel"},
	}
	for _, c := range cases {
		got, err := expandHome(c.in)
		if err != nil {
			t.Errorf("expandHome(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("expandHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadFile_Notfound(t *testing.T) {
	_, err := LoadFile("/definitely/not/a/path/config.yaml")
	if err == nil {
		t.Fatalf("expected not-found error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

// ----------------------------------------------------------------------
// Validate
// ----------------------------------------------------------------------

func TestValidate_AllValid(t *testing.T) {
	tmp := t.TempDir()
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: tmp,
		Repos: []RepoConfig{
			{
				Remote: "owner/repo",
				VCS:    "github",
				CICD:   []string{"github-actions"},
				Issues: "jira",
				Path:   tmp,
			},
		},
	}
	rep, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("expected no errors, got %+v", rep.Issues)
	}
}

func TestValidate_MissingFields(t *testing.T) {
	cfg := &Config{}
	rep, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasErrors() {
		t.Fatalf("expected errors, got %+v", rep)
	}
	paths := issuePaths(rep)
	for _, want := range []string{"self_login", "worktree_root", "repos"} {
		if !contains(paths, want) {
			t.Errorf("expected error for %q, got %v", want, paths)
		}
	}
}

func TestValidate_UnknownProvider(t *testing.T) {
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos: []RepoConfig{
			{Remote: "owner/repo", VCS: "bogus", CICD: []string{"also-bogus"}, Issues: "still-bogus"},
		},
	}
	rep, _ := cfg.Validate()
	paths := issuePaths(rep)
	for _, want := range []string{"repos[0].vcs", "repos[0].cicd[0]", "repos[0].issues"} {
		if !contains(paths, want) {
			t.Errorf("expected error for %q, got %v", want, paths)
		}
	}
}

func TestValidate_ExecProviderAccepted(t *testing.T) {
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos: []RepoConfig{
			{
				Remote: "owner/repo",
				VCS:    "exec:my-vcs",
				CICD:   []string{"exec:my-cicd"},
				Issues: "exec:my-issues",
			},
		},
	}
	rep, _ := cfg.Validate()
	if rep.HasErrors() {
		t.Fatalf("exec: providers should be accepted, got: %+v", rep.Issues)
	}
}

func TestValidate_MissingPathIsWarning(t *testing.T) {
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos: []RepoConfig{
			{Remote: "owner/repo", VCS: "github", CICD: []string{"github-actions"}, Path: "/nope/does/not/exist"},
		},
	}
	rep, _ := cfg.Validate()
	if rep.HasErrors() {
		t.Fatalf("missing path should be warning, not error: %+v", rep.Issues)
	}
	found := false
	for _, i := range rep.Issues {
		if i.Path == "repos[0].path" && i.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warning for repos[0].path, got %+v", rep.Issues)
	}
}

func TestValidate_EmptyCICDIsWarning(t *testing.T) {
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos:        []RepoConfig{{Remote: "owner/repo", VCS: "github"}},
	}
	rep, _ := cfg.Validate()
	if rep.HasErrors() {
		t.Fatalf("empty cicd should be warning, got: %+v", rep.Issues)
	}
	found := false
	for _, i := range rep.Issues {
		if i.Path == "repos[0].cicd" && i.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warning for repos[0].cicd: %+v", rep.Issues)
	}
}

func TestValidate_NilConfig(t *testing.T) {
	var cfg *Config
	if _, err := cfg.Validate(); err == nil {
		t.Fatal("expected error for nil config")
	}
}

// issuePaths returns the .Path of each issue in rep — used for set checks.
func issuePaths(rep *ValidationReport) []string {
	out := make([]string, len(rep.Issues))
	for i, x := range rep.Issues {
		out[i] = x.Path
	}
	return out
}

func contains(ss []string, want string) bool {
	return slices.Contains(ss, want)
}

func TestExcludedCIChecksParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
self_login: me
worktree_root: /tmp/wt
repos:
  - remote: owner/name
    excluded_ci_checks:
      - "^policy-bot"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	got := cfg.Repos[0].ExcludedCIChecks
	if len(got) != 1 || got[0] != "^policy-bot" {
		t.Errorf("ExcludedCIChecks = %v, want [^policy-bot]", got)
	}
}

// ----------------------------------------------------------------------
// ApproverAllowlist / VerdictGenerations (pg2-4dz88.1.3)
// ----------------------------------------------------------------------

func TestApproverAllowlistParsed(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: me
worktree_root: /tmp/wt
repos:
  - remote: owner/name
agents:
  - login: approver-one
    approval_regex: '(?im)^GEN2-CLEAN$'
  - login: bot-not-an-approver
    approval_regex: '(?im)^GEN2-CLEAN$'
approver_allowlist:
  - approver-one
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.ApproverAllowlist) != 1 || cfg.ApproverAllowlist[0] != "approver-one" {
		t.Errorf("ApproverAllowlist = %v, want [approver-one]", cfg.ApproverAllowlist)
	}
}

// TestApproverAllowlistAbsentIsZeroValue proves an old deployment config,
// unmodified, keeps loading without error and without enabling the
// mechanism: absent approver_allowlist decodes to a nil/empty slice.
func TestApproverAllowlistAbsentIsZeroValue(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, minimalYAML)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.ApproverAllowlist) != 0 {
		t.Errorf("expected empty ApproverAllowlist for config predating the field, got %v", cfg.ApproverAllowlist)
	}
	if len(cfg.VerdictGenerations) != 0 {
		t.Errorf("expected empty VerdictGenerations for config predating the field, got %v", cfg.VerdictGenerations)
	}
}

// TestVerdictGenerationsParsed_PreservesOrder proves a synthetic
// multi-generation grammar round-trips from YAML in DECLARED ORDER —
// ordering is load-bearing for a future sibling leaf's "highest declared
// generation wins" tie-break.
func TestVerdictGenerationsParsed_PreservesOrder(t *testing.T) {
	dir := t.TempDir()
	p := writeYAML(t, dir, `
self_login: me
worktree_root: /tmp/wt
repos:
  - remote: owner/name
verdict_generations:
  - id: gen1
    body_marker: X-TEST-MARKER-V1
    findings_patterns:
      - '(?im)^GEN1-FINDINGS:\s*(\d+)$'
    authority_patterns:
      - '(?im)^GEN1-VERDICT:\s*approve$'
  - id: gen2
    body_marker: X-TEST-MARKER-V2
    findings_patterns:
      - '(?im)^GEN2-FINDINGS:\s*(\d+)$'
    authority_patterns:
      - '(?im)^GEN2-CLEAN$'
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.VerdictGenerations) != 2 {
		t.Fatalf("expected 2 generations, got %d", len(cfg.VerdictGenerations))
	}
	if cfg.VerdictGenerations[0].ID != "gen1" || cfg.VerdictGenerations[1].ID != "gen2" {
		t.Fatalf("expected declared order [gen1 gen2], got [%s %s]",
			cfg.VerdictGenerations[0].ID, cfg.VerdictGenerations[1].ID)
	}
	g1 := cfg.VerdictGenerations[0]
	if g1.BodyMarker != "X-TEST-MARKER-V1" {
		t.Errorf("gen1 body_marker: got %q", g1.BodyMarker)
	}
	if len(g1.FindingsPatterns) != 1 || g1.FindingsPatterns[0] != `(?im)^GEN1-FINDINGS:\s*(\d+)$` {
		t.Errorf("gen1 findings_patterns: got %v", g1.FindingsPatterns)
	}
	if len(g1.AuthorityPatterns) != 1 || g1.AuthorityPatterns[0] != `(?im)^GEN1-VERDICT:\s*approve$` {
		t.Errorf("gen1 authority_patterns: got %v", g1.AuthorityPatterns)
	}
	g2 := cfg.VerdictGenerations[1]
	if g2.BodyMarker != "X-TEST-MARKER-V2" {
		t.Errorf("gen2 body_marker: got %q", g2.BodyMarker)
	}
}

func TestValidate_ApproverAllowlistEmptyEntry(t *testing.T) {
	cfg := &Config{
		SelfLogin:         "me",
		WorktreeRoot:      "/tmp",
		Repos:             []RepoConfig{{Remote: "owner/repo"}},
		ApproverAllowlist: []string{"approver-one", "  "},
	}
	rep, _ := cfg.Validate()
	paths := issuePaths(rep)
	if !contains(paths, "approver_allowlist[1]") {
		t.Errorf("expected error for approver_allowlist[1], got %v", paths)
	}
}

func TestValidate_VerdictGenerationRequiredFields(t *testing.T) {
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: "/tmp",
		Repos:        []RepoConfig{{Remote: "owner/repo"}},
		VerdictGenerations: []VerdictGeneration{
			{ID: "", BodyMarker: ""},
		},
	}
	rep, _ := cfg.Validate()
	paths := issuePaths(rep)
	for _, want := range []string{"verdict_generations[0].id", "verdict_generations[0].body_marker"} {
		if !contains(paths, want) {
			t.Errorf("expected error for %q, got %v", want, paths)
		}
	}
}

func TestValidate_VerdictGenerationValid(t *testing.T) {
	tmp := t.TempDir()
	cfg := &Config{
		SelfLogin:    "me",
		WorktreeRoot: tmp,
		Repos:        []RepoConfig{{Remote: "owner/repo", VCS: "github", CICD: []string{"github-actions"}}},
		VerdictGenerations: []VerdictGeneration{
			{ID: "gen1", BodyMarker: "X-TEST-MARKER", AuthorityPatterns: []string{"(?im)^GEN2-CLEAN$"}},
		},
		ApproverAllowlist: []string{"approver-one"},
	}
	rep, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("expected no errors for a fully-specified generation, got %+v", rep.Issues)
	}
}
