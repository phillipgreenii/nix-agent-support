package primarycommit

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hermeticEnviron removes git env vars inherited from a parent `git commit`'s hook
// environment (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX,
// GIT_OBJECT_DIRECTORY, GIT_COMMON_DIR). These variables repoint tempdir git calls at
// the real repo, breaking test hermeticity when tests are run from a git commit hook —
// same pattern and same fix as pg2-f6cgn / commit 98f8c95d in packages/pb, ported here
// for pg2-rrhw2: the sibling fixture in internal/engine (which shares this exact
// pattern) corrupted the AMBIENT repo's shared .git/config this way, confirmed by
// reproduction — `GIT_DIR=<ambient>/.git git -C <dir> config user.email …` silently
// writes into <ambient>, not <dir>, and <dir>/.git is never even created. This file's
// own three `git()` helpers share the identical `-C d`-with-no-other-isolation
// pattern, so they share the identical exposure.
//
// t.Setenv cannot fix this: GIT_DIR="" is not "unset" to git — it is a fatal "the
// empty string is not a valid path" — so the only reliable fix is to omit these
// variables from the subprocess's OWN environment entirely.
func hermeticEnviron() []string {
	skipVars := map[string]bool{
		"GIT_DIR": true, "GIT_INDEX_FILE": true, "GIT_WORK_TREE": true,
		"GIT_PREFIX": true, "GIT_OBJECT_DIRECTORY": true, "GIT_COMMON_DIR": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; !skipVars[k] {
			env = append(env, kv)
		}
	}
	return env
}

// Contract test: pins our assumptions about the on-disk git file formats the resolver
// reads (.git dir-vs-file, .git/HEAD ref line, .git/config section). Uses real `git`
// only to BUILD fixtures; the resolver itself never shells out. Requires git on PATH.
func TestFileResolver_Contract(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	git := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = hermeticEnviron()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", d, args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "trunk")
	git(dir, "config", "user.email", "t@example.com")
	git(dir, "config", "user.name", "t")

	r := NewFileResolver()

	// Main working tree is canonical (.git is a directory).
	if c, err := r.IsCanonical(dir); err != nil || !c {
		t.Fatalf("IsCanonical(main) = %v, %v; want true", c, err)
	}
	// CurrentBranch reads .git/HEAD — works on an UNBORN branch.
	if b, err := r.CurrentBranch(dir); err != nil || b != "trunk" {
		t.Fatalf("CurrentBranch = %q, %v; want trunk", b, err)
	}
	// No config key -> default main.
	if p, err := r.PrimaryBranch(dir); err != nil || p != "main" {
		t.Fatalf("PrimaryBranch(default) = %q, %v; want main", p, err)
	}
	// Config override wins.
	git(dir, "config", "pgii-integrate-branch.primaryBranch", "mainline")
	if p, _ := r.PrimaryBranch(dir); p != "mainline" {
		t.Fatalf("PrimaryBranch(config) = %q; want mainline", p)
	}
	// A linked worktree is NOT canonical (.git is a gitdir: file).
	git(dir, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(dir, "worktree", "add", "-q", "-b", "feature", wt)
	if c, err := r.IsCanonical(wt); err != nil || c {
		t.Fatalf("IsCanonical(worktree) = %v, %v; want false", c, err)
	}
}

// Contract test: exercises two real code paths the above test never reaches because
// ".git" is always found on the FIRST os.Lstat there.
//   - gitRoot's walk-up loop: querying from a directory several levels below the repo
//     root forces gitRoot to walk up parent-by-parent until it finds ".git".
//   - CurrentBranch's detached-HEAD path: after a real `git checkout --detach`,
//     .git/HEAD holds a raw SHA (no "ref: refs/heads/" prefix), so CurrentBranch must
//     return "" rather than a branch name.
func TestFileResolver_WalkUpAndDetached(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	git := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = hermeticEnviron()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", d, args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "trunk")
	git(dir, "config", "user.email", "t@example.com")
	git(dir, "config", "user.name", "t")
	git(dir, "commit", "--allow-empty", "-q", "-m", "init")

	r := NewFileResolver()

	// Nested directory several levels below the repo root: ".git" is NOT found on the
	// first Lstat, so gitRoot must walk up (a/b/c -> a/b -> a -> dir) to find it.
	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", nested, err)
	}
	if c, err := r.IsCanonical(nested); err != nil || !c {
		t.Fatalf("IsCanonical(nested) = %v, %v; want true", c, err)
	}
	if b, err := r.CurrentBranch(nested); err != nil || b != "trunk" {
		t.Fatalf("CurrentBranch(nested) = %q, %v; want trunk", b, err)
	}

	// Detach HEAD: .git/HEAD now holds a raw commit SHA, not "ref: refs/heads/...".
	git(dir, "checkout", "-q", "--detach")
	if b, err := r.CurrentBranch(dir); err != nil || b != "" {
		t.Fatalf("CurrentBranch(detached) = %q, %v; want \"\"", b, err)
	}
}

// TestFileResolver_MissingDir pins pg2-5adzj: IsCanonical must refuse to walk UP from a
// directory that does not exist, and must signal that distinctly (ErrDirNotExist)
// rather than silently answering false the way "genuinely off primary" or "genuinely
// not canonical" would. Without the dirExists guard, a MISSING nested path used to
// walk past itself and land on whichever real ancestor DOES have a ".git" — here, the
// canonical repo itself — and report canonical=true, exactly the false "primary"
// finding pg2-5adzj reports (measured against production asks.db row 326758, whose
// referenced worktree had already been removed by the time of replay).
func TestFileResolver_MissingDir(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	git := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = hermeticEnviron()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", d, args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "main")
	git(dir, "config", "user.email", "t@example.com")
	git(dir, "config", "user.name", "t")
	git(dir, "commit", "--allow-empty", "-q", "-m", "init")

	r := NewFileResolver()

	// A nested path that was NEVER created: gitRoot would otherwise walk up from it
	// straight to `dir`'s own ".git" and (wrongly) report canonical=true.
	ghost := filepath.Join(dir, ".worktrees", "ghost-never-created")
	if c, err := r.IsCanonical(ghost); c || !errors.Is(err, ErrDirNotExist) {
		t.Fatalf("IsCanonical(never-created) = %v, %v; want false, ErrDirNotExist", c, err)
	}

	// The SAME path, but as a genuinely nested EXISTING subdirectory with no ".git" of
	// its own — the case TestFileResolver_WalkUpAndDetached exercises separately — MUST
	// keep resolving to canonical=true. This is the regression guard: the dirExists
	// check must gate on the STARTING directory's existence only, never on whether it
	// has its own ".git".
	if err := os.MkdirAll(ghost, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", ghost, err)
	}
	if c, err := r.IsCanonical(ghost); err != nil || !c {
		t.Fatalf("IsCanonical(existing nested subdir) = %v, %v; want true, nil", c, err)
	}

	// A REMOVED worktree — created, then torn down, mirroring the exact production
	// shape (a `.worktrees/<bead>` cleaned up after landing).
	wt := filepath.Join(dir, ".worktrees", "landed-and-removed")
	git(dir, "worktree", "add", "-q", "-b", "landed", wt)
	git(dir, "worktree", "remove", wt)
	if c, err := r.IsCanonical(wt); c || !errors.Is(err, ErrDirNotExist) {
		t.Fatalf("IsCanonical(removed worktree) = %v, %v; want false, ErrDirNotExist", c, err)
	}
}

// writeRepo creates a canonical-looking repo (a real .git DIRECTORY) at a temp path
// with the given .git/config contents, so FileResolver's file reads have something to
// parse without shelling out to git. Returns the repo root.
func writeRepo(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile(config): %v", err)
	}
	return root
}

// writeGlobal writes a temp global config file and points GIT_CONFIG_GLOBAL at it.
func writeGlobal(t *testing.T, config string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile(global): %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
}

// TestFileResolver_PushDefault exercises the tc-2phi8 push.default reader: local
// .git/config wins over the global config, and either is read on its own.
func TestFileResolver_PushDefault(t *testing.T) {
	r := NewFileResolver()

	t.Run("local only", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		root := writeRepo(t, "[push]\n\tdefault = matching\n")
		if v, err := r.PushDefault(root); err != nil || v != "matching" {
			t.Fatalf("PushDefault(local) = %q, %v; want matching", v, err)
		}
	})

	t.Run("global only", func(t *testing.T) {
		writeGlobal(t, "[push]\n\tdefault = current\n")
		root := writeRepo(t, "[core]\n\tbare = false\n") // no push.default locally
		if v, err := r.PushDefault(root); err != nil || v != "current" {
			t.Fatalf("PushDefault(global) = %q, %v; want current", v, err)
		}
	})

	t.Run("local overrides global", func(t *testing.T) {
		writeGlobal(t, "[push]\n\tdefault = matching\n")
		root := writeRepo(t, "[push]\n\tdefault = nothing\n")
		if v, err := r.PushDefault(root); err != nil || v != "nothing" {
			t.Fatalf("PushDefault(local over global) = %q, %v; want nothing", v, err)
		}
	})

	t.Run("unset in both", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		root := writeRepo(t, "[core]\n\tbare = false\n")
		if v, err := r.PushDefault(root); err != nil || v != "" {
			t.Fatalf("PushDefault(unset) = %q, %v; want \"\"", v, err)
		}
	})
}

// TestFileResolver_Aliases exercises the tc-2phi8 alias reader: the [alias] section of
// the local and global configs are merged, local overriding global per-alias, keys lowered.
func TestFileResolver_Aliases(t *testing.T) {
	r := NewFileResolver()

	t.Run("local only", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		root := writeRepo(t, "[alias]\n\tci = commit -am x\n\tst = status\n")
		got, err := r.Aliases(root)
		want := map[string]string{"ci": "commit -am x", "st": "status"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Aliases(local) = %v, %v; want %v", got, err, want)
		}
	})

	t.Run("global only", func(t *testing.T) {
		writeGlobal(t, "[alias]\n\tp = push origin HEAD:main\n")
		root := writeRepo(t, "[core]\n\tbare = false\n")
		got, err := r.Aliases(root)
		want := map[string]string{"p": "push origin HEAD:main"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Aliases(global) = %v, %v; want %v", got, err, want)
		}
	})

	t.Run("local overrides global, union of names", func(t *testing.T) {
		writeGlobal(t, "[alias]\n\tp = status\n\tg = gc\n")
		root := writeRepo(t, "[alias]\n\tp = push origin HEAD:main\n")
		got, err := r.Aliases(root)
		// p overridden by local; g inherited from global.
		want := map[string]string{"p": "push origin HEAD:main", "g": "gc"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Aliases(local over global) = %v, %v; want %v", got, err, want)
		}
	})

	t.Run("case-insensitive keys lowered", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		root := writeRepo(t, "[alias]\n\tCI = commit -am x\n")
		got, _ := r.Aliases(root)
		if got["ci"] != "commit -am x" {
			t.Fatalf("Aliases key not lowered: got %v", got)
		}
	})

	t.Run("none defined -> nil", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		root := writeRepo(t, "[core]\n\tbare = false\n")
		if got, err := r.Aliases(root); err != nil || got != nil {
			t.Fatalf("Aliases(none) = %v, %v; want nil", got, err)
		}
	})
}
