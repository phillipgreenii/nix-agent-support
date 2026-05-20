package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
