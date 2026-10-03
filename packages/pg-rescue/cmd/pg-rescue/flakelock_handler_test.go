package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tests for the pg-rescue-flake-lock-conflict handler (bead pg2-3ybxg), run
// through the REAL pg-rescue wrapper (this test binary re-executed as main,
// see TestMain) against scratch git repositories: a bare origin plus two
// clones, where one clone pushes and the other pulls with
// `git pull --rebase` under pg-rescue.
//
// The handler is found on PATH (the nix check lists it in testDeps). For a
// local `go test`, where it is not installed, it falls back to the script
// source run directly under bash with the strictness the builder injects.
// `nix` is a fake that writes a deterministic lock and records its arguments
// (the sandbox has no network); `nix flake lock` is unsupported by the fake on
// purpose, so a handler that used it would fail loudly.

const handlerName = "pg-rescue-flake-lock-conflict"

const fakeNix = `#!/bin/sh
echo "$*" >> "$FAKE_NIX_LOG"
if [ "$1 $2" != "flake update" ]; then
  echo "fake nix: unsupported invocation: $*" >&2
  exit 64
fi
shift 2
printf 'relocked: %s\n' "$*" > flake.lock
`

const lockBase = `{
  "nodes": {
    "nixpkgs": {
      "locked": {
        "rev": "base"
      }
    },
    "root": {
      "inputs": {}
    }
  },
  "root": "root",
  "version": 7
}
`

func lockWithNode(name, rev string) string {
	return strings.Replace(lockBase, "  \"nodes\": {\n", "  \"nodes\": {\n    \""+name+"\": {\n      \"locked\": {\n        \"rev\": \""+rev+"\"\n      }\n    },\n", 1)
}

func lockWithNixpkgsRev(rev string) string {
	return strings.Replace(lockBase, `"rev": "base"`, `"rev": "`+rev+`"`, 1)
}

type lockEnv struct {
	t       *testing.T
	bin     string
	nixLog  string
	cfg     string
	origin  string
	work    string // the clone that pulls under pg-rescue
	other   string // the clone that pushes first
	stateHm string
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newLockEnv builds the fakes on PATH and a seeded origin with two clones.
func newLockEnv(t *testing.T) *lockEnv {
	t.Helper()
	root := t.TempDir()
	e := &lockEnv{
		t: t, bin: filepath.Join(root, "bin"), nixLog: filepath.Join(root, "nix.log"),
		cfg: filepath.Join(root, "config.toml"), origin: filepath.Join(root, "origin.git"),
		work: filepath.Join(root, "work"), other: filepath.Join(root, "other"), stateHm: filepath.Join(root, "state"),
	}
	if err := os.MkdirAll(e.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.bin, "nix"), fakeNix)

	// `pg-rescue` for the handler's `pg-rescue result`: this test binary as main.
	writeFile(t, filepath.Join(e.bin, "pg-rescue"),
		"#!/bin/sh\nexec env GO_WANT_HELPER_PROCESS=1 '"+os.Args[0]+"' -test.run='^$' -- \"$@\"\n")

	if _, err := exec.LookPath(handlerName); err != nil {
		_, file, _, _ := runtime.Caller(0)
		src := filepath.Join(filepath.Dir(file), "..", "..", "..", handlerName, handlerName, handlerName+".sh")
		if _, err := os.Stat(src); err != nil {
			t.Fatalf("%s is neither on PATH nor at %s", handlerName, src)
		}
		writeFile(t, filepath.Join(e.bin, handlerName), "#!/bin/sh\nexec bash -euo pipefail '"+src+"' \"$@\"\n")
	}

	writeFile(t, e.cfg, "[handler.flock]\ncommand = [\""+handlerName+"\"]\n")

	gitIn(t, root, "init", "--bare", "-b", "main", e.origin)
	seed := filepath.Join(root, "seed")
	gitIn(t, root, "clone", e.origin, seed)
	e.identify(seed)
	writeFile(t, filepath.Join(seed, "flake.lock"), lockBase)
	writeFile(t, filepath.Join(seed, "README.md"), "readme\n")
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-m", "seed")
	gitIn(t, seed, "push", "origin", "HEAD:main")
	for _, c := range []string{e.work, e.other} {
		gitIn(t, root, "clone", e.origin, c)
		e.identify(c)
	}
	return e
}

func (e *lockEnv) identify(dir string) {
	gitIn(e.t, dir, "config", "user.name", "Test")
	gitIn(e.t, dir, "config", "user.email", "test@example.com")
	gitIn(e.t, dir, "config", "commit.gpgsign", "false")
}

func (e *lockEnv) commit(dir, msg string, files map[string]string) {
	e.t.Helper()
	for name, content := range files {
		writeFile(e.t, filepath.Join(dir, name), content)
	}
	gitIn(e.t, dir, "add", ".")
	gitIn(e.t, dir, "commit", "-m", msg)
}

// pull runs `git pull --rebase` in the work clone under the real pg-rescue.
func (e *lockEnv) pull() (code int, stdout, stderr string) {
	e.t.Helper()
	return runMain(e.t, []string{
		"PATH=" + e.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_NIX_LOG=" + e.nixLog,
		"XDG_STATE_HOME=" + e.stateHm,
	}, "--config", e.cfg, "--handlers", "flock", "-C", e.work, "-v", "--", "git", "pull", "--rebase")
}

func (e *lockEnv) midRebase() bool {
	e.t.Helper()
	p := strings.TrimSpace(gitIn(e.t, e.work, "rev-parse", "--git-path", "rebase-merge"))
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.work, p)
	}
	_, err := os.Stat(p)
	return err == nil
}

func (e *lockEnv) nixCalls() string {
	b, _ := os.ReadFile(e.nixLog)
	return string(b)
}

func (e *lockEnv) read(name string) string {
	b, err := os.ReadFile(filepath.Join(e.work, name))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// requireResolved asserts the work clone ended up cleanly rebased: the local
// commit(s) on top of the pushed one, a clean tree, flake.lock as the fake nix
// wrote it, and the wrapper exit 0 with verify (the re-run `git pull --rebase`,
// which needs a clean tree) passed.
func (e *lockEnv) requireResolved(code int, stderr, wantLock string, wantSubjects []string) {
	e.t.Helper()
	if code != 0 {
		e.t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, w := range []string{"flock: resolved", "relocked flake.lock", "-- verify: passed (exit 0) --", "resolved by flock"} {
		if !strings.Contains(stderr, w) {
			e.t.Errorf("stderr lacks %q:\n%s", w, stderr)
		}
	}
	if e.midRebase() {
		e.t.Error("the repository is still mid-rebase")
	}
	if st := gitIn(e.t, e.work, "status", "--porcelain"); st != "" {
		e.t.Errorf("working tree is not clean:\n%s", st)
	}
	if got := e.read("flake.lock"); got != wantLock {
		e.t.Errorf("flake.lock = %q; want %q", got, wantLock)
	}
	subjects := strings.Fields(strings.ReplaceAll(gitIn(e.t, e.work, "log", "--format=%s", "-n", "10"), " ", "_"))
	if strings.Join(subjects, ",") != strings.Join(wantSubjects, ",") {
		e.t.Errorf("history = %v; want %v", subjects, wantSubjects)
	}
	if strings.Contains(e.nixCalls(), "flake lock") {
		e.t.Error("the handler ran `nix flake lock`")
	}
}

func TestFlakeLockHandlerResolvesLockOnlyConflictTargetingConflictedInputs(t *testing.T) {
	e := newLockEnv(t)
	// Both sides insert a different input at the same spot: the conflict
	// markers enclose both node headers, so the names are extractable.
	e.commit(e.other, "other:_add_alpha", map[string]string{"flake.lock": lockWithNode("alpha", "a1")})
	gitIn(t, e.other, "push", "origin", "HEAD:main")
	e.commit(e.work, "local:_add_beta", map[string]string{"flake.lock": lockWithNode("beta", "b1")})

	code, _, stderr := e.pull()
	e.requireResolved(code, stderr, "relocked: alpha beta\n", []string{"local:_add_beta", "other:_add_alpha", "seed"})
	if got := e.nixCalls(); got != "flake update alpha beta\n" {
		t.Errorf("nix calls = %q; want a targeted `flake update alpha beta`", got)
	}
}

func TestFlakeLockHandlerFallsBackToBareUpdateWhenNoNamesExtracted(t *testing.T) {
	e := newLockEnv(t)
	// Only a rev line differs, so the conflict markers enclose no node header.
	e.commit(e.other, "other:_bump", map[string]string{"flake.lock": lockWithNixpkgsRev("o1")})
	gitIn(t, e.other, "push", "origin", "HEAD:main")
	e.commit(e.work, "local:_bump", map[string]string{"flake.lock": lockWithNixpkgsRev("w1")})

	code, _, stderr := e.pull()
	e.requireResolved(code, stderr, "relocked: \n", []string{"local:_bump", "other:_bump", "seed"})
	if got := e.nixCalls(); got != "flake update\n" {
		t.Errorf("nix calls = %q; want a bare `flake update`", got)
	}
}

func TestFlakeLockHandlerDeclinesWhenAnotherFileConflicts(t *testing.T) {
	e := newLockEnv(t)
	e.commit(e.other, "other:_both", map[string]string{"flake.lock": lockWithNode("alpha", "a1"), "README.md": "upstream\n"})
	gitIn(t, e.other, "push", "origin", "HEAD:main")
	e.commit(e.work, "local:_both", map[string]string{"flake.lock": lockWithNode("beta", "b1"), "README.md": "local\n"})

	code, _, stderr := e.pull()
	if code != 1 {
		t.Errorf("exit = %d; want the command's own code, 1; stderr:\n%s", code, stderr)
	}
	for _, w := range []string{"flock: declined (exit 2)", "Conflict spans source files, not just flake.lock", "README.md"} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr lacks %q:\n%s", w, stderr)
		}
	}
	if !e.midRebase() {
		t.Error("a declined handler must leave the rebase stopped for the next handler")
	}
	if got := e.nixCalls(); got != "" {
		t.Errorf("nix was run despite the decline: %q", got)
	}
	if !strings.Contains(e.read("flake.lock"), "<<<<<<<") {
		t.Error("a declined handler must not touch flake.lock")
	}
}

func TestFlakeLockHandlerDeclinesWhenRebaseStopsAgain(t *testing.T) {
	e := newLockEnv(t)
	e.commit(e.other, "other:_add_alpha", map[string]string{"flake.lock": lockWithNode("alpha", "a1")})
	gitIn(t, e.other, "push", "origin", "HEAD:main")
	e.commit(e.work, "local:_add_beta", map[string]string{"flake.lock": lockWithNode("beta", "b1")})
	// A second local commit touching the same lock conflicts again once the
	// first is replayed onto the fake-relocked lock.
	e.commit(e.work, "local:_rev_beta", map[string]string{"flake.lock": lockWithNode("beta", "b2")})

	code, _, stderr := e.pull()
	if code != 1 {
		t.Errorf("exit = %d; want 1; stderr:\n%s", code, stderr)
	}
	for _, w := range []string{"flock: declined (exit 2)", "Rebase stopped again at ", "local:_rev_beta"} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr lacks %q:\n%s", w, stderr)
		}
	}
	if !e.midRebase() {
		t.Error("the rebase should be left stopped on the second commit")
	}
	if got := e.nixCalls(); got != "flake update alpha beta\n" {
		t.Errorf("nix calls = %q; want exactly the first commit's relock", got)
	}
}
