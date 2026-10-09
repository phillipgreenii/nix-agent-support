//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/runlog"
	"github.com/phillipgreenii/pg-rescue/internal/testenv"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestMain(m *testing.M) {
	code := testenv.Run(m)
	// binary() builds into a dir under $TMPDIR on first use; remove it so
	// repeated runs leave nothing behind.
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

const flakeLockHandler = "pg-rescue-flake-lock-conflict"

// fakeNix stands in for nix, which the sandbox cannot run. Only
// `nix flake update` is supported, so a handler that took another route (for
// example `nix flake lock`) fails loudly.
const fakeNix = `#!/bin/sh
echo "$*" >> "$FAKE_NIX_LOG"
if [ "$1 $2" != "flake update" ]; then
  echo "fake nix: unsupported invocation: $*" >&2
  exit 64
fi
shift 2
printf 'relocked: %s\n' "$*" > flake.lock
`

// fakeClaude stands in for claude: it records that it ran and what it was
// given on stdin, then answers with an envelope whose verdict is "declined"
// (the agent looked and chose not to touch a source-file conflict).
const fakeClaude = `#!/bin/sh
cat > "$FAKE_CLAUDE_DIR/stdin"
pwd > "$FAKE_CLAUDE_DIR/cwd"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"{\"outcome\":\"declined\",\"summary\":\"will not edit a source-file conflict\"}","session_id":"sess-it"}'
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
	return strings.Replace(lockBase, "  \"nodes\": {\n",
		"  \"nodes\": {\n    \""+name+"\": {\n      \"locked\": {\n        \"rev\": \""+rev+"\"\n      }\n    },\n", 1)
}

// moduleRoot is packages/pg-rescue, found from this file's own location.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
)

// binary returns the path of a built pg-rescue binary. In the nix check the
// package is on PATH. Elsewhere (a developer's `go test -tags integration`)
// it is built once from ./cmd/<name>.
func binary(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "pg-rescue-it-bin-")
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	out := filepath.Join(buildDir, name)
	if _, err := os.Stat(out); err == nil {
		return out
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/"+name)
	cmd.Dir = moduleRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s is not on PATH and go build failed: %v\n%s", name, err, b)
	}
	return out
}

// flakeLockBinary is the handler: on PATH in the nix check, otherwise the
// script source run under bash with the strictness the builder injects.
func flakeLockBinary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath(flakeLockHandler); err == nil {
		return p
	}
	src := filepath.Join(moduleRoot(t), "..", flakeLockHandler, flakeLockHandler, flakeLockHandler+".sh")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("%s is neither on PATH nor at %s", flakeLockHandler, src)
	}
	return writeExec(t, filepath.Join(t.TempDir(), flakeLockHandler),
		"#!/bin/sh\nexec bash -euo pipefail '"+src+"' \"$@\"\n")
}

func writeExec(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// gitIn runs git in repo through the fixture's hermetic client (x/gittest) and
// returns its stdout.
func gitIn(t *testing.T, repo *gitfixture.Repo, args ...string) string {
	t.Helper()
	out, err := repo.Client.Run(t.Context(), args...)
	if err != nil {
		t.Fatalf("git %s (in %s): %v", strings.Join(args, " "), repo.Dir, err)
	}
	return string(out)
}

// rig is one scenario's world: a bare origin, the clone that pulls under
// pg-rescue (work), the clone that pushes first (other), the fakes, and the
// pg-rescue config.
type rig struct {
	t         *testing.T
	env       []string // environment for pg-rescue
	work      *gitfixture.Repo
	other     *gitfixture.Repo
	cfg       string
	state     string // XDG_STATE_HOME
	nixLog    string
	claudeDir string
	connector *testenv.FakeConnector
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{
		t:         t,
		cfg:       filepath.Join(root, "config.toml"),
		state:     filepath.Join(root, "state"),
		nixLog:    filepath.Join(root, "nix.log"),
		claudeDir: filepath.Join(root, "claude"),
		connector: testenv.NewFakeConnector(t),
	}
	if err := os.MkdirAll(r.claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// One bin dir holds the real binaries (symlinked, so each is found by its
	// own name) and the fakes. It goes first on PATH.
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pg-rescue", "pg-rescue-claude", "pg-rescue-bead"} {
		if err := os.Symlink(binary(t, name), filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(flakeLockBinary(t), filepath.Join(bin, flakeLockHandler)); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(bin, "nix"), fakeNix)
	writeExec(t, filepath.Join(bin, "claude"), fakeClaude)

	var connectorPath string
	var env []string
	for _, kv := range r.connector.Env() {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			connectorPath = v
			continue
		}
		env = append(env, kv)
	}
	r.env = append(
		env,
		"PATH="+bin+string(os.PathListSeparator)+connectorPath,
		"XDG_STATE_HOME="+r.state,
		"FAKE_NIX_LOG="+r.nixLog,
		"FAKE_CLAUDE_DIR="+r.claudeDir,
	)

	// The config mirrors the home-manager module's default instances and
	// `sync` chain, minus `fix-large` and `notify`, which this test does not
	// reach (notify would post a real macOS notification). The tracker is
	// named explicitly because the scratch repos have no .beads/.
	tracker := filepath.Join(root, "tracker")
	if err := os.MkdirAll(tracker, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `
[handler.flake-lock-conflict]
command = ["pg-rescue-flake-lock-conflict"]
timeout = "2m"
tags = ["deterministic"]

[handler.fix-small]
command = ["pg-rescue-claude", "--model", "haiku", "--time-limit", "2m", "--strict-mcp-config"]
timeout = "3m"
tags = ["agent"]

[handler.p1-later]
command = ["pg-rescue-bead", "--priority", "1", "--dedup-query", "pg-rescue-open", "--tracker-dir", "` + tracker + `"]
timeout = "1m"
tags = ["deferral"]

[chain.sync]
handlers = ["flake-lock-conflict", "fix-small", "p1-later"]
`
	if err := os.WriteFile(r.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	origin := gittest.New(t, gitfixture.RepoOptions{Name: "origin", Bare: true, InitialBranch: "main"})
	seed := gittest.Clone(t, origin, "seed")
	r.commit(seed, "seed", map[string]string{"flake.lock": lockBase, "README.md": "readme\n"})
	gitIn(t, seed, "push", "origin", "HEAD:main")
	r.work = gittest.Clone(t, origin, "work")
	r.other = gittest.Clone(t, origin, "other")
	return r
}

func (r *rig) commit(repo *gitfixture.Repo, msg string, files map[string]string) {
	r.t.Helper()
	if _, err := repo.Commit(r.t.Context(), msg, files); err != nil {
		r.t.Fatal(err)
	}
}

// pull runs `git pull --rebase` in the work clone under the real pg-rescue,
// chain `sync`, and returns the wrapper's exit code and stderr.
func (r *rig) pull() (code int, stderr string) {
	r.t.Helper()
	cmd := exec.Command(binary(r.t, "pg-rescue"), "--config", r.cfg, "--chain", "sync",
		"-C", r.work.Dir, "--context", "integration: rebase onto origin", "--", "git", "pull", "--rebase")
	cmd.Env = append(os.Environ(), r.env...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), errb.String()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return 0, errb.String()
}

func (r *rig) midRebase() bool {
	r.t.Helper()
	p := strings.TrimSpace(gitIn(r.t, r.work, "rev-parse", "--git-path", "rebase-merge"))
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.work.Dir, p)
	}
	_, err := os.Stat(p)
	return err == nil
}

// runLog returns the single run-log entry the scenario wrote.
func (r *rig) runLog() runlog.Entry {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.state, "pg-rescue", runlog.FileName))
	if err != nil {
		r.t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		r.t.Fatalf("run log has %d lines; want 1:\n%s", len(lines), b)
	}
	var e runlog.Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		r.t.Fatalf("run log line is not an Entry: %v\n%s", err, lines[0])
	}
	return e
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func (r *rig) fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// TestLockOnlyConflictIsResolvedByFlakeLockConflict: both sides add a
// different input to flake.lock. The deterministic handler, first in the
// chain, resolves it; no agent runs and nothing is filed.
func TestLockOnlyConflictIsResolvedByFlakeLockConflict(t *testing.T) {
	r := newRig(t)
	r.commit(r.other, "other: add alpha", map[string]string{"flake.lock": lockWithNode("alpha", "a1")})
	gitIn(t, r.other, "push", "origin", "HEAD:main")
	r.commit(r.work, "local: add beta", map[string]string{"flake.lock": lockWithNode("beta", "b1")})

	code, stderr := r.pull()
	if code != 0 {
		t.Fatalf("exit = %d; want 0; stderr:\n%s", code, stderr)
	}
	if r.midRebase() {
		t.Error("the repository is still mid-rebase")
	}
	if st := gitIn(t, r.work, "status", "--porcelain"); st != "" {
		t.Errorf("working tree is not clean:\n%s", st)
	}
	if b, _ := os.ReadFile(filepath.Join(r.work.Dir, "flake.lock")); string(b) != "relocked: alpha beta\n" {
		t.Errorf("flake.lock = %q; want the fake nix's relock of the two conflicted inputs", b)
	}
	if b, _ := os.ReadFile(r.nixLog); string(b) != "flake update alpha beta\n" {
		t.Errorf("nix calls = %q; want one targeted `flake update alpha beta`", b)
	}
	if r.fileExists(r.claudeDir, "stdin") {
		t.Error("claude ran although the deterministic handler resolved the conflict")
	}
	if n := len(r.connector.Calls()); n != 0 {
		t.Errorf("pg-connector was called %d times; want 0", n)
	}

	e := r.runLog()
	if e.Result != "resolved" || str(e.ResolvedBy) != "flake-lock-conflict" || e.FinalExit != 0 {
		t.Errorf("run log: result=%q resolved_by=%s final_exit=%d; want resolved by flake-lock-conflict, exit 0", e.Result, str(e.ResolvedBy), e.FinalExit)
	}
	if len(e.Attempts) != 1 || e.Attempts[0].Handler != "flake-lock-conflict" || e.Attempts[0].Outcome != "resolved" ||
		strings.Join(e.Attempts[0].Tags, ",") != "deterministic" {
		t.Errorf("run log attempts = %+v; want one resolved, deterministic-tagged flake-lock-conflict attempt", e.Attempts)
	}
}

// TestSourceConflictIsDeclinedThenDeferred: both sides edit README.md, so the
// deterministic handler declines (a source file conflicts), the agent
// (fix-small) declines too, and p1-later files a deferral. The run exits 75
// and leaves the repository mid-rebase for whoever picks the item up.
func TestSourceConflictIsDeclinedThenDeferred(t *testing.T) {
	r := newRig(t)
	r.commit(r.other, "other: edit readme", map[string]string{"README.md": "upstream\n"})
	gitIn(t, r.other, "push", "origin", "HEAD:main")
	r.commit(r.work, "local: edit readme", map[string]string{"README.md": "local\n"})

	code, stderr := r.pull()
	if code != 75 {
		t.Fatalf("exit = %d; want 75 (deferred); stderr:\n%s", code, stderr)
	}
	if !r.midRebase() {
		t.Error("a deferral must leave the repository mid-rebase")
	}
	if b, _ := os.ReadFile(filepath.Join(r.work.Dir, "README.md")); !strings.Contains(string(b), "<<<<<<<") {
		t.Errorf("README.md = %q; want the conflict left in place", b)
	}
	if b, _ := os.ReadFile(r.nixLog); len(b) != 0 {
		t.Errorf("nix ran despite the lock not being the conflict: %q", b)
	}

	// The agent ran once, and was shown the conflict.
	prompt, err := os.ReadFile(filepath.Join(r.claudeDir, "stdin"))
	if err != nil {
		t.Fatalf("claude did not run: %v", err)
	}
	if !strings.Contains(string(prompt), "README.md") {
		t.Errorf("the agent's prompt does not mention the conflicted file:\n%s", prompt)
	}

	// The deferral was filed through pg-connector, by the bead handler.
	var created bool
	for _, c := range r.connector.Calls() {
		if len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "create" {
			created = true
		}
	}
	if !created {
		t.Errorf("no `pg-connector issue create` call was recorded: %+v", r.connector.Calls())
	}

	e := r.runLog()
	if e.Result != "deferred" || str(e.DeferredBy) != "p1-later" || e.FinalExit != 75 {
		t.Errorf("run log: result=%q deferred_by=%s final_exit=%d; want deferred by p1-later, exit 75", e.Result, str(e.DeferredBy), e.FinalExit)
	}
	if e.Exit == nil || *e.Exit == 0 {
		t.Errorf("run log exit = %v; want the failed command's own non-zero exit", e.Exit)
	}
	var got []string
	for _, a := range e.Attempts {
		got = append(got, a.Handler+"="+a.Outcome)
	}
	want := "flake-lock-conflict=declined,fix-small=declined,p1-later=deferred"
	if strings.Join(got, ",") != want {
		t.Errorf("run log attempts = %v; want %s", got, want)
	}
}
