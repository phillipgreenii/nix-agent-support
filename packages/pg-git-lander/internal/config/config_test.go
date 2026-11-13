package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

type dirPaths string

func (d dirPaths) Dir() string { return string(d) }

type fx struct {
	t    *testing.T
	repo *gitfixture.Repo
	ldr  *config.Loader
	xdg  string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "config", InitialCommit: true})
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
	xdg := filepath.Join(t.TempDir(), "pg-git-lander")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fx{t: t, repo: repo, ldr: &config.Loader{Git: g, Paths: dirPaths(xdg)}, xdg: xdg}
}

func (f *fx) writeXDG(content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.xdg, "config.toml"), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// xdgFor writes an XDG file with a [[repo]] entry for this clone, followed
// by extra.
func (f *fx) xdgFor(entry, extra string) {
	f.t.Helper()
	f.writeXDG(extra + "\n[[repo]]\nroot = \"" + f.repo.Dir + "\"\n" + entry)
}

func (f *fx) commit(msg string, files map[string]string) {
	f.t.Helper()
	if _, err := f.repo.Commit(f.t.Context(), msg, files); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) git(args ...string) {
	f.t.Helper()
	r := f.ldr.Git.Run(f.t.Context(), f.repo.Dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v: %s", args, r.Stderr)
	}
}

func (f *fx) load(ref string) (*config.Config, error) {
	f.t.Helper()
	pre, err := f.ldr.LoadXDG(f.t.Context(), f.repo.Dir)
	if err != nil {
		return nil, err
	}
	return pre.Load(f.t.Context(), ref)
}

func (f *fx) mustLoad(ref string) *config.Config {
	f.t.Helper()
	c, err := f.load(ref)
	if err != nil {
		f.t.Fatalf("load: %v", err)
	}
	return c
}

func asErr(t *testing.T, err error) *config.Error {
	t.Helper()
	var ce *config.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *config.Error, got %v", err)
	}
	return ce
}

const land = "refs/heads/main"

func TestNoConfigurationMeansDefaults(t *testing.T) {
	f := newFx(t)
	c := f.mustLoad(land)
	if len(c.Rules) != 0 || len(c.Hooks) != 0 || c.ConflictDefault != nil {
		t.Fatalf("expected no rules or hooks: %+v", c)
	}
	if c.Lock.LeaseTTL != 30*time.Second || c.Lock.Wait != 60*time.Second || !c.RunGitHooks ||
		c.MaxRaceRetries != 2 || c.MaxConflictRounds != 10 || c.AutofixAttempts != 2 ||
		c.Revalidate != config.RevalidateFull {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.TrustRepoConfig || c.IgnoreRepoConfig || c.Primary != "" {
		t.Fatalf("flags wrong: %+v", c)
	}
	if len(c.Sources) != 2 || c.Sources[0].Present || c.Sources[1].Present {
		t.Fatalf("sources: %+v", c.Sources)
	}
	if c.HooksFor("land", "before_move") != nil {
		t.Fatal("hooks for an unset point must be empty")
	}
}

func TestEveryKeyAcceptedFromXDGDefaults(t *testing.T) {
	f := newFx(t)
	f.writeXDG(`
[defaults]
primary = "trunk"
run_git_hooks = false
max_race_retries = 7
max_conflict_rounds = 8
[defaults.lock]
lease_ttl = "45s"
wait = "2m"
[defaults.autofix]
attempts = 3
[defaults.hooks]
land.before = [{ run = ["a"] }]
[[defaults.conflict]]
glob = "x"
resolve = ["r"]
[defaults.conflict_default]
resolve = ["d"]
`)
	c := f.mustLoad("")
	if c.Primary != "trunk" || c.RunGitHooks || c.MaxRaceRetries != 7 || c.MaxConflictRounds != 8 ||
		c.AutofixAttempts != 3 || c.Lock.LeaseTTL != 45*time.Second || c.Lock.Wait != 2*time.Minute {
		t.Fatalf("keys: %+v", c)
	}
	if h := c.HooksFor("land", "before"); len(h) != 1 || h[0].Cwd != "repo" || h[0].Timeout != config.DefaultTimeout {
		t.Fatalf("hook defaults: %+v", h)
	}
	if len(c.Rules) != 1 || c.Rules[0].ID != "xdg-defaults:1:x" || c.Rules[0].RunPer != "file" || c.ConflictDefault == nil {
		t.Fatalf("rules: %+v", c.Rules)
	}
}

func TestLayeringKeyByKeyAndHookListReplacedWhole(t *testing.T) {
	f := newFx(t)
	f.xdgFor(`trust_repo_config = true
max_conflict_rounds = 4
[repo.hooks]
land.after = [{ run = ["entry-after"] }]
`, `
[defaults]
run_git_hooks = false
max_race_retries = 5
max_conflict_rounds = 6
[defaults.autofix]
attempts = 9
[defaults.hooks]
land.before_move = [{ run = ["d1"] }, { run = ["d2"] }]
land.after = [{ run = ["d-after"] }]
rebase.after_rebase = [{ run = ["d-rebase"] }]
`)
	f.commit("cfg", map[string]string{config.RepoFileName: `
max_race_retries = 3
[hooks.land]
before_move = [{ run = ["r1"], timeout = "20m", skip_exit_codes = [13, 127], cwd = "repo", env = { A = "b" } }]
after = [{ run = ["repo-after"] }]
`})
	c := f.mustLoad(land)
	if c.RunGitHooks { // only the defaults set it
		t.Fatal("run_git_hooks from defaults lost")
	}
	if c.MaxRaceRetries != 3 { // repo file beats defaults
		t.Fatalf("max_race_retries = %d", c.MaxRaceRetries)
	}
	if c.MaxConflictRounds != 4 { // entry beats defaults
		t.Fatalf("max_conflict_rounds = %d", c.MaxConflictRounds)
	}
	if c.AutofixAttempts != 9 {
		t.Fatalf("autofix = %d", c.AutofixAttempts)
	}
	bm := c.HooksFor("land", "before_move")
	if len(bm) != 1 || bm[0].Run[0] != "r1" || bm[0].Timeout != 20*time.Minute ||
		len(bm[0].SkipExitCodes) != 2 || bm[0].Cwd != "repo" || bm[0].Env["A"] != "b" {
		t.Fatalf("repo list must replace the defaults list whole: %+v", bm)
	}
	if a := c.HooksFor("land", "after"); len(a) != 1 || a[0].Run[0] != "entry-after" {
		t.Fatalf("entry list must replace the repo list: %+v", a)
	}
	if r := c.HooksFor("rebase", "after_rebase"); len(r) != 1 || r[0].Run[0] != "d-rebase" {
		t.Fatalf("an untouched point keeps the defaults list: %+v", r)
	}
}

func TestRuleOrderAndIDs(t *testing.T) {
	f := newFx(t)
	f.xdgFor(`trust_repo_config = true
[[repo.conflict]]
glob = "e1"
resolve = ["e"]
[[repo.conflict]]
glob = "e2"
resolve = ["e"]
`, `
[[defaults.conflict]]
glob = "d1"
resolve = ["d"]
`)
	f.commit("cfg", map[string]string{config.RepoFileName: `
[[conflict]]
glob = "f1"
resolve = ["f"]
[[conflict]]
glob = "packages/*/go.sum"
run_per = "dir"
resolve = ["f"]
verify = ["go", "build"]
timeout = "3m"
`})
	c := f.mustLoad(land)
	var ids []string
	for _, r := range c.Rules {
		ids = append(ids, r.ID)
	}
	want := "xdg-repo:1:e1,xdg-repo:2:e2,repo-file:1:f1,repo-file:2:packages/*/go.sum,xdg-defaults:1:d1"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("rule order/ids:\n got %s\nwant %s", got, want)
	}
	r := c.Rules[3]
	if r.RunPer != "dir" || r.Timeout != 3*time.Minute || len(r.Verify) != 2 {
		t.Fatalf("rule fields: %+v", r)
	}
}

func TestTrust(t *testing.T) {
	cmds := map[string]string{config.RepoFileName: "[hooks.land]\nafter = [{ run = [\"x\"] }]\n"}

	t.Run("untrusted repo naming commands refuses with the XDG line", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", cmds)
		_, err := f.load(land)
		ce := asErr(t, err)
		if ce.Word != outcome.Refused || ce.Reason != outcome.RefUntrustedConfig {
			t.Fatalf("got %+v", ce)
		}
		for _, w := range []string{filepath.Join(f.xdg, "config.toml"), "[[repo]]", "root = ", "trust_repo_config = true"} {
			if !strings.Contains(ce.Hint, w) {
				t.Fatalf("hint %q lacks %q", ce.Hint, w)
			}
		}
	})
	t.Run("untrusted entry gets a set-the-flag hint", func(t *testing.T) {
		f := newFx(t)
		f.xdgFor("trust_repo_config = false\n", "")
		f.commit("cfg", cmds)
		_, err := f.load(land)
		ce := asErr(t, err)
		if ce.Reason != outcome.RefUntrustedConfig || !strings.Contains(ce.Hint, "trust_repo_config = true") {
			t.Fatalf("got %+v", ce)
		}
	})
	t.Run("a conflict rule counts as naming commands", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "[[conflict]]\nglob = \"a\"\nresolve = [\"x\"]\n"})
		if ce := asErr(t, mustFail(t, f, land)); ce.Reason != outcome.RefUntrustedConfig {
			t.Fatalf("got %+v", ce)
		}
	})
	t.Run("a file naming no commands is applied", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "max_race_retries = 6\n[autofix]\nattempts = 4\n"})
		c := f.mustLoad(land)
		if c.MaxRaceRetries != 6 || c.AutofixAttempts != 4 {
			t.Fatalf("not applied: %+v", c)
		}
	})
	t.Run("trusted runs the commands", func(t *testing.T) {
		f := newFx(t)
		f.xdgFor("trust_repo_config = true\n", "")
		f.commit("cfg", cmds)
		c := f.mustLoad(land)
		if !c.TrustRepoConfig || len(c.HooksFor("land", "after")) != 1 {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("an empty hook list names no commands", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "[hooks.land]\nafter = []\n"})
		f.mustLoad(land)
	})
}

func mustFail(t *testing.T, f *fx, ref string) error {
	t.Helper()
	_, err := f.load(ref)
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func TestIgnoreRepoConfigSkipsTheFileEntirely(t *testing.T) {
	f := newFx(t)
	f.xdgFor("ignore_repo_config = true\n", "")
	// Broken and command-naming: neither matters.
	f.commit("cfg", map[string]string{config.RepoFileName: "max_race_retries = \"x\"\n[hooks.land]\nafter = [{ run = [\"x\"] }]\n"})
	c := f.mustLoad(land)
	if !c.IgnoreRepoConfig || c.MaxRaceRetries != 2 || len(c.Hooks) != 0 {
		t.Fatalf("got %+v", c)
	}
	if len(c.Sources) != 1 {
		t.Fatalf("the repo file must not be a source: %+v", c.Sources)
	}
}

func TestRepoFileIsReadFromTheTipNotTheTreeOrBranch(t *testing.T) {
	f := newFx(t)
	f.commit("main cfg", map[string]string{config.RepoFileName: "max_race_retries = 3\n"})

	// BRANCH edits the file; the working tree edits it again, uncommitted.
	f.git("checkout", "-b", "feature")
	f.commit("branch cfg", map[string]string{config.RepoFileName: "max_race_retries = 4\n"})
	if err := os.WriteFile(filepath.Join(f.repo.Dir, config.RepoFileName), []byte("max_race_retries = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A remote-tracking ref holds yet another version.
	f.git("update-ref", "refs/remotes/origin/main", "feature")

	if got := f.mustLoad(land).MaxRaceRetries; got != 3 {
		t.Fatalf("land must read TARGET's tip: got %d", got)
	}
	pre, err := f.ldr.LoadXDG(t.Context(), f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	tip := pre.RebaseTip(t.Context())
	if tip != "refs/heads/main" {
		t.Fatalf("rebase tip = %s", tip)
	}
	c, err := pre.Load(t.Context(), tip)
	if err != nil || c.MaxRaceRetries != 3 {
		t.Fatalf("rebase must read the primary's tip: %v %+v", err, c)
	}
	if src := c.Sources[1]; src.Ref != tip || len(src.Tip) != 40 || !src.Present {
		t.Fatalf("source: %+v", src)
	}
}

func TestReloadSeesTheNewTipAndResume(t *testing.T) {
	f := newFx(t)
	f.commit("v1", map[string]string{config.RepoFileName: "max_race_retries = 3\n"})
	pre, err := f.ldr.LoadXDG(t.Context(), f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := pre.Load(t.Context(), land)
	if err != nil {
		t.Fatal(err)
	}
	f.commit("v2", map[string]string{config.RepoFileName: "max_race_retries = 8\n"})
	c2, err := pre.Load(t.Context(), land) // the reload after a race, or at --continue
	if err != nil {
		t.Fatal(err)
	}
	if c1.MaxRaceRetries != 3 || c2.MaxRaceRetries != 8 || c1.Digest == c2.Digest {
		t.Fatalf("reload must re-read: %d %d digests equal=%v", c1.MaxRaceRetries, c2.MaxRaceRetries, c1.Digest == c2.Digest)
	}
	c3, _ := pre.Load(t.Context(), land)
	if c3.Digest != c2.Digest || c2.Digest == "" {
		t.Fatal("digest must be deterministic")
	}
}

func TestRepoConfigErrorNamesFileLineKeyLayer(t *testing.T) {
	cases := []struct {
		name, file, key string
		line            int
	}{
		{"wrong type", "# c\nmax_race_retries = \"x\"\n", "max_race_retries", 2},
		{"bad duration", "[hooks.land]\nafter = [{ run = [\"x\"], timeout = \"soon\" }]\n", "hooks.land.after[0].timeout", 2},
		{"bad cwd", "[hooks.land]\nafter = [{ run = [\"x\"], cwd = \"elsewhere\" }]\n", "hooks.land.after[0].cwd", 2},
		{"empty argv", "\n\n[hooks.land]\nafter = [{ run = [] }]\n", "hooks.land.after[0].run", 4},
		{"bad run_per", "[[conflict]]\nglob = \"a\"\nresolve = [\"x\"]\nrun_per = \"all\"\n", "conflict[0].run_per", 4},
		{"bad glob", "[[conflict]]\nglob = \"[a\"\nresolve = [\"x\"]\n", "conflict[0].glob", 2},
		{"negative", "max_conflict_rounds = -1\n", "max_conflict_rounds", 1},
		{"zero attempts", "[autofix]\nattempts = 0\n", "autofix.attempts", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			f.commit("cfg", map[string]string{config.RepoFileName: tc.file})
			ce := asErr(t, mustFail(t, f, land))
			if ce.Word != outcome.Refused || ce.Reason != outcome.RefRepoConfigError || ce.Loc == nil {
				t.Fatalf("got %+v", ce)
			}
			if ce.Loc.Key != tc.key || ce.Loc.Line != tc.line || ce.Loc.Layer != "repo-file" ||
				!strings.HasSuffix(ce.Loc.File, config.RepoFileName) {
				t.Fatalf("loc = %+v, want key %s line %d", *ce.Loc, tc.key, tc.line)
			}
		})
	}
	t.Run("syntax error", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "a = 1\nb = = 2\n"})
		ce := asErr(t, mustFail(t, f, land))
		if ce.Reason != outcome.RefRepoConfigError || ce.Loc.Line != 2 {
			t.Fatalf("got %+v", ce.Loc)
		}
	})
}

func TestUnknownKeysInTheRepoFileAreWarnings(t *testing.T) {
	f := newFx(t)
	f.commit("cfg", map[string]string{config.RepoFileName: "from_the_future = 1\nmax_race_retries = 3\n[autofix]\nattempts = 2\nmode = \"x\"\n"})
	c := f.mustLoad(land)
	if c.MaxRaceRetries != 3 {
		t.Fatal("known keys must still apply")
	}
	got := map[string]int{}
	for _, w := range c.Warnings {
		if w.Word != outcome.WarnUnknownRepoConfigKey || !strings.HasSuffix(w.File, config.RepoFileName) {
			t.Fatalf("warning %+v", w)
		}
		got[w.Key] = w.Line
	}
	if got["from_the_future"] != 1 || got["autofix.mode"] != 5 || len(got) != 2 {
		t.Fatalf("warnings: %+v", c.Warnings)
	}
}

func TestRepoFileCannotSetPreLockKeys(t *testing.T) {
	f := newFx(t)
	f.commit("cfg", map[string]string{config.RepoFileName: "primary = \"evil\"\ntrust_repo_config = true\nignore_repo_config = true\n[lock]\nwait = \"1s\"\n"})
	c := f.mustLoad(land)
	if c.Primary != "" || c.TrustRepoConfig || c.IgnoreRepoConfig || c.Lock.Wait != 60*time.Second {
		t.Fatalf("repo file leaked a pre-lock key: %+v", c)
	}
	if len(c.Warnings) != 4 {
		t.Fatalf("warnings: %+v", c.Warnings)
	}
}

func TestInvalidXDGIsConfigError(t *testing.T) {
	cases := []struct {
		name, file, key string
		line            int
	}{
		{"syntax", "[defaults]\nx = = 1\n", "", 2},
		{"unknown key", "[defaults]\nbogus = 1\n", "defaults.bogus", 2},
		{"bad duration", "[defaults.lock]\nwait = \"x\"\n", "defaults.lock.wait", 2},
		{"entry without root", "# c\n[[repo]]\ntrust_repo_config = true\n", "repo[0]", 2},
		{"entry bad type", "[[repo]]\nroot = \"/x\"\ntrust_repo_config = \"yes\"\n", "repo[0].trust_repo_config", 3},
		{"second entry", "[[repo]]\nroot = \"/x\"\n[[repo]]\nroot = \"/y\"\nmax_race_retries = \"a\"\n", "repo[1].max_race_retries", 5},
		{"unknown top-level", "mystery = 1\n", "mystery", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			f.writeXDG(tc.file)
			ce := asErr(t, mustFail(t, f, land))
			if ce.Word != outcome.ConfigError || ce.Loc == nil || ce.Loc.Layer != "xdg" ||
				ce.Loc.Key != tc.key || ce.Loc.Line != tc.line || ce.Loc.File != filepath.Join(f.xdg, "config.toml") {
				t.Fatalf("got %+v loc=%+v", ce, ce.Loc)
			}
		})
	}
}

func TestEntryMatchesAnyWorktreeOfTheClone(t *testing.T) {
	f := newFx(t)
	f.xdgFor("trust_repo_config = true\nmax_race_retries = 9\n", "")
	wt := filepath.Join(f.repo.Root(), "linked")
	f.git("worktree", "add", "-b", "wt", wt)
	pre, err := f.ldr.LoadXDG(t.Context(), wt)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pre.Load(t.Context(), "")
	if err != nil || !c.TrustRepoConfig || c.MaxRaceRetries != 9 {
		t.Fatalf("entry must match a linked worktree: %v %+v", err, c)
	}
	if want, _ := filepath.EvalSymlinks(f.repo.Dir); pre.Repo != want {
		t.Fatalf("Repo = %s, want the main worktree %s", pre.Repo, want)
	}
	// A different clone does not match.
	other := gittest.New(t, gitfixture.RepoOptions{Suite: "config-other", InitialCommit: true})
	pre2, err := f.ldr.LoadXDG(t.Context(), other.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if pre2.TrustRepoConfig {
		t.Fatal("entry must not match another clone")
	}
}

func TestPrimaryResolutionOrder(t *testing.T) {
	ctx := context.Background()
	f := newFx(t)
	pre, _ := f.ldr.LoadXDG(ctx, f.repo.Dir)
	if got := pre.ResolvePrimary(ctx); got != "main" {
		t.Fatalf("fallback = %s", got)
	}
	f.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	if got := pre.ResolvePrimary(ctx); got != "develop" {
		t.Fatalf("origin/HEAD = %s", got)
	}
	if tip := pre.RebaseTip(ctx); tip != "refs/heads/develop" {
		t.Fatalf("rebase tip follows the primary: %s", tip)
	}
	f.writeXDG("[defaults]\nprimary = \"trunk\"\n")
	pre, _ = f.ldr.LoadXDG(ctx, f.repo.Dir)
	if got := pre.ResolvePrimary(ctx); got != "trunk" {
		t.Fatalf("XDG primary must win: %s", got)
	}
	f.xdgFor("primary = \"entry-main\"\n", "[defaults]\nprimary = \"trunk\"\n")
	pre, _ = f.ldr.LoadXDG(ctx, f.repo.Dir)
	if got := pre.ResolvePrimary(ctx); got != "entry-main" {
		t.Fatalf("entry primary must beat defaults: %s", got)
	}
}

func TestLockKeysComeFromXDGOnlyBeforeAnyLock(t *testing.T) {
	f := newFx(t)
	f.xdgFor("[repo.lock]\nwait = \"5s\"\n", "[defaults.lock]\nlease_ttl = \"9s\"\n")
	pre, err := f.ldr.LoadXDG(t.Context(), f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Lock.LeaseTTL != 9*time.Second || pre.Lock.Wait != 5*time.Second {
		t.Fatalf("lock = %+v", pre.Lock)
	}
}

func TestMissingRepoFileAndBadRef(t *testing.T) {
	f := newFx(t)
	c := f.mustLoad(land)
	if len(c.Sources) != 2 || c.Sources[1].Present || len(c.Sources[1].Tip) != 40 {
		t.Fatalf("sources: %+v", c.Sources)
	}
	if _, err := f.load("refs/heads/does-not-exist"); err == nil {
		t.Fatal("an unreadable tip must be an error, not silently no config")
	}
}

func TestRevalidateKey(t *testing.T) {
	for _, v := range []string{"none", "paths", "full"} {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "revalidate = \"" + v + "\"\n"})
		if c := f.mustLoad(land); c.Revalidate != v {
			t.Fatalf("revalidate = %q, want %q", c.Revalidate, v)
		}
	}
	t.Run("an unknown value is a repo config error naming the key", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "revalidate = \"sometimes\"\n"})
		ce := asErr(t, mustFail(t, f, land))
		if ce.Reason != outcome.RefRepoConfigError || ce.Loc == nil || ce.Loc.Key != "revalidate" {
			t.Fatalf("got %+v", ce)
		}
	})
	t.Run("a non-string value is refused", func(t *testing.T) {
		f := newFx(t)
		f.commit("cfg", map[string]string{config.RepoFileName: "revalidate = 3\n"})
		asErr(t, mustFail(t, f, land))
	})
	t.Run("the repo file beats the XDG defaults", func(t *testing.T) {
		f := newFx(t)
		f.xdgFor("[defaults]\nrevalidate = \"none\"\n", "")
		if c := f.mustLoad(land); c.Revalidate != config.RevalidateNone {
			t.Fatalf("revalidate = %q", c.Revalidate)
		}
		f.commit("cfg", map[string]string{config.RepoFileName: "revalidate = \"paths\"\n"})
		if c := f.mustLoad(land); c.Revalidate != config.RevalidatePaths {
			t.Fatalf("revalidate = %q", c.Revalidate)
		}
	})
}
