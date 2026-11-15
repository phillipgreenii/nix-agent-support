package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
)

func TestParseDurationGrammar(t *testing.T) {
	if d, err := config.ParseDuration("none"); err != nil || !d.None {
		t.Fatalf("none: %+v %v", d, err)
	}
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "2d": 48 * time.Hour, "3h": 3 * time.Hour, "5m": 5 * time.Minute} {
		d, err := config.ParseDuration(in)
		if err != nil || d.D != want || d.None {
			t.Fatalf("%s: %+v %v", in, d, err)
		}
	}
	for _, bad := range []string{"0s", "-1m", "1.5h", "1w", "", "s", "10", "+5s", "99999999999999d"} {
		if _, err := config.ParseDuration(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	for in, want := range map[string]string{"90s": "90s", "2m": "2m", "3d": "3d", "none": "none"} {
		d, _ := config.ParseDuration(in)
		if d.String() != want {
			t.Fatalf("%s -> %s", in, d.String())
		}
	}
	d, _ := config.ParseDuration("120s")
	if d.String() != "2m" {
		t.Fatal(d.String())
	}
	d, _ = config.ParseDuration("72h")
	if d.String() != "3d" {
		t.Fatal(d.String())
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	f, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil || len(f.Repos) != 0 {
		t.Fatalf("missing: %+v %v", f, err)
	}
	if _, err := config.Load(write(t, "[repos.\"/x\"]\nbogus = 1\n")); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown key: %v", err)
	}
	if _, err := config.Load(write(t, "[repos.\"/x\"]\ndefault-ttl = \"0s\"\n")); err == nil {
		t.Fatal("bad duration accepted")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := config.DefaultPath(); got != "/xdg/pg-gwm/config.toml" {
		t.Fatal(got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	if got := config.DefaultPath(); got != "/h/.config/pg-gwm/config.toml" {
		t.Fatal(got)
	}
}

func TestForDefaults(t *testing.T) {
	main := t.TempDir()
	r := repo.Repo{Main: main, Name: "app"}
	c := config.File{}.For(r)
	if c.WorktreeRoot != main+".worktrees" || c.WorktreeDirTemplate != "{{repo}}-wt-{{index}}" ||
		c.LockWait.D != 10*time.Second || c.TombstoneRetention.D != 7*24*time.Hour || c.Reuse || c.Clean ||
		!c.DefaultTTL.None || c.GitTimeout.D != 2*time.Minute || c.CreateTimeout.D != 30*time.Minute ||
		c.IndexLockStaleAfter.D != 10*time.Minute || c.ReuseHookPolicy != "run" || c.Min != nil || c.Max != nil {
		t.Fatalf("%+v", c)
	}
	f, err := config.Load(write(t, "[repos.\""+main+"\"]\ndefault-ttl = \"1h\"\nmin = 1\nkeep-list = [\"a\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	c = f.For(r)
	if c.DefaultTTL.D != time.Hour || c.DefaultTTL.None || *c.Min != 1 || len(c.KeepList) != 1 || c.LockWait.D != 10*time.Second {
		t.Fatalf("%+v", c)
	}
}

func TestForMatchesThroughSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(real)
	f, err := config.Load(write(t, "[repos.\""+resolved+"\"]\nmin = 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, real, resolved} {
		if c := f.For(repo.Repo{Main: p}); c.Min == nil || *c.Min != 2 {
			t.Fatalf("%s: %+v", p, c)
		}
	}
}

func TestRenderTemplates(t *testing.T) {
	in := config.TemplateInput{
		Repo: "app", Index: 3, Name: "n", LeaseID: "l-1", Owner: "o",
		Metadata: map[string]string{"role": "r"}, Min: 1, Max: 4, Count: 2,
	}
	if got, err := config.RenderDir("{{repo}}-wt-{{index}}", in); err != nil || got != "app-wt-3" {
		t.Fatalf("%q %v", got, err)
	}
	for i, want := range map[int]string{0: "slot-a", 25: "slot-z", 26: "slot-aa", 27: "slot-ab"} {
		in.Index = i
		if got, err := config.RenderDir("slot-{{index | letter}}", in); err != nil || got != want {
			t.Fatalf("%d: %q %v", i, got, err)
		}
	}
	if got, err := config.RenderDir(`{{metadata "role"}}-{{name}}-{{lease_id}}-{{owner}}-{{min}}-{{max}}-{{count}}`, in); err != nil || got != "r-n-l-1-o-1-4-2" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"a/b", "", "{{nope}}", "{{", "..", "{{metadata \"absent\"}}"} {
		if got, err := config.RenderDir(bad, in); err == nil {
			t.Fatalf("%q rendered %q", bad, got)
		}
	}
}

func TestCheck(t *testing.T) {
	f, err := config.Load(write(t, `
[repos."/a/one"]
min = 3
max = 2
[repos."/b/two"]
worktree-root = "/shared"
worktree-dir-template = "wt-{{index}}"
[repos."/c/three"]
worktree-root = "/shared"
worktree-dir-template = "wt-{{index}}"
[repos."/d/four"]
worktree-dir-template = "{{bogus}}"
keep-list = ["../x"]
reuse-reclaimed-worktrees = true
`))
	if err != nil {
		t.Fatal(err)
	}
	got := config.Check(f)
	has := func(level, repo, sub string) {
		t.Helper()
		for _, g := range got {
			if g.Level == level && g.Repo == repo && strings.Contains(g.Message, sub) {
				return
			}
		}
		t.Fatalf("no %s finding for %s containing %q in %+v", level, repo, sub, got)
	}
	has(config.LevelError, "/a/one", "min 3")
	has(config.LevelError, "/c/three", `"wt-0" under /shared`)
	has(config.LevelError, "/d/four", "worktree-dir-template")
	has(config.LevelError, "/d/four", "keep list")
	has(config.LevelWarning, "/d/four", "clean-reused-worktree")
	if len(config.Check(config.File{})) != 0 {
		t.Fatal("empty file has findings")
	}
	// Default templates under different default roots never collide.
	ok := config.File{Repos: map[string]config.RepoConfig{"/a/app": {}, "/b/app": {}}}
	if got := config.Check(ok); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestCheckLive(t *testing.T) {
	two, four := 2, 4
	f := config.File{Repos: map[string]config.RepoConfig{
		"/main":      {Max: &four},
		"/main/sub":  {},
		"/gone":      {},
		"/uncounted": {Max: &two},
	}}
	resolve := func(p string) (repo.Repo, error) {
		switch p {
		case "/gone":
			return repo.Repo{}, errors.New("no such directory")
		case "/main/sub":
			return repo.Repo{Main: "/main"}, nil
		}
		return repo.Repo{Main: p}, nil
	}
	count := func(r repo.Repo) (int, error) {
		if r.Main == "/uncounted" {
			return 0, errors.New("state unreadable")
		}
		return 5, nil
	}
	got := config.CheckLive(context.Background(), f, resolve, count)
	want := map[string]string{"/main": "exceed max 4", "/main/sub": "not a main checkout", "/gone": "does not resolve", "/uncounted": "cannot count"}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for _, g := range got {
		if g.Level != config.LevelWarning || !strings.Contains(g.Message, want[g.Repo]) {
			t.Fatalf("%+v", g)
		}
	}
}
