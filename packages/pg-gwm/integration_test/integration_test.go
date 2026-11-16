//go:build integration

// Package integration_test runs the built pg-gwm binary across processes: a
// lease killed mid-create, a reap that outruns its deadline, and a git that
// ignores SIGTERM. It sits behind the integration tag and has its own flake
// check (pg-gwm-integration-tests); the unit gate never compiles it.
package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// binPath is the pg-gwm binary built once by TestMain.
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pg-gwm-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "pg-gwm")
	build := exec.Command("go", "build", "-o", binPath, "../cmd/pg-gwm")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	code := 1
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building pg-gwm:", err)
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// world is one hermetic repository, configuration and state, and the
// environment every pg-gwm child runs in.
type world struct {
	t      *testing.T
	repo   *gitfixture.Repo
	hooks  string // the repository's hooks directory
	binDir string // first on the child's PATH
	env    []string
}

// newWorld builds a repository and writes a configuration naming only it;
// extra is appended to its table.
func newWorld(t *testing.T, extra string) *world {
	t.Helper()
	state, cfg, home := t.TempDir(), t.TempDir(), t.TempDir()
	r := gittest.New(t, gitfixture.RepoOptions{Suite: "integration", InitialCommit: true})
	real, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := r.RootPath("hooks")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg, "pg-gwm"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[repos.%q]\nworktree-root = %q\n%s", real, filepath.Join(t.TempDir(), "wt"), extra)
	if err := os.WriteFile(filepath.Join(cfg, "pg-gwm", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	return &world{
		t: t, repo: r, hooks: hooks, binDir: binDir,
		env: []string{
			"HOME=" + home,
			"XDG_STATE_HOME=" + state,
			"XDG_CONFIG_HOME=" + cfg,
			"GIT_CONFIG_NOSYSTEM=1",
			"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		},
	}
}

func (w *world) writeScript(path, body string) {
	w.t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) cmd(args ...string) *exec.Cmd {
	c := exec.Command(binPath, append(args, "--repo", w.repo.Dir)...)
	c.Env = w.env
	return c
}

// run runs pg-gwm to completion and returns stdout and the exit code.
func (w *world) run(args ...string) (string, int) {
	w.t.Helper()
	c := w.cmd(args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	w.t.Logf("pg-gwm %v: exit %d\nstdout: %s\nstderr: %s", args, code, out.String(), errb.String())
	return out.String(), code
}

type reapResult struct {
	Removed []string `json:"removed"`
	Broken  []string `json:"broken"`
	Created []string `json:"created"`
	Outcome string   `json:"outcome"`
}

func (w *world) reap(args ...string) reapResult {
	w.t.Helper()
	out, code := w.run(append([]string{"reap", "--format=json"}, args...)...)
	if code != 0 {
		w.t.Fatalf("reap exited %d", code)
	}
	var parsed struct {
		Results []reapResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || len(parsed.Results) != 1 {
		w.t.Fatalf("reap output %q: %v", out, err)
	}
	return parsed.Results[0]
}

type record struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (w *world) list() []record {
	w.t.Helper()
	out, code := w.run("list", "--format=json")
	if code != 0 {
		w.t.Fatalf("list exited %d", code)
	}
	var recs []record
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		w.t.Fatalf("list output %q: %v", out, err)
	}
	return recs
}

func waitFor(t *testing.T, what string, limit time.Duration, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A lease killed with SIGKILL while its post-checkout hook blocks drops its
// operation hold; the next reap marks the worktree broken and removes it in
// the same run, so list never shows it.
func TestKilledLeaseIsMarkedBrokenAndRemoved(t *testing.T) {
	w := newWorld(t, "")
	marker, release := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "release")
	w.writeScript(filepath.Join(w.hooks, "post-checkout"), fmt.Sprintf(
		`touch %q
n=0
while [ ! -e %q ] && [ "$n" -lt 1200 ]; do sleep 0.1; n=$((n+1)); done
`, marker, release,
	))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644) })

	lease := w.cmd("lease", "--detach", "--format=json")
	if err := lease.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the hook to start", 30*time.Second, func() bool { return exists(marker) })
	if err := lease.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = lease.Wait()

	res := w.reap()
	if len(res.Broken) != 1 || !slices.Equal(res.Broken, res.Removed) {
		t.Fatalf("want one worktree both broken and removed, got broken=%v removed=%v", res.Broken, res.Removed)
	}
	for _, r := range w.list() {
		if r.ID == res.Broken[0] {
			t.Fatalf("list still shows %s (%s)", r.ID, r.Status)
		}
	}
}

// A reap with a 1-second deadline and a slow post-checkout hook ends with
// the outcome deadline, and still exits 0.
func TestReapDeadlineWithSlowHook(t *testing.T) {
	w := newWorld(t, "min = 2\nmax = 3\n")
	w.writeScript(filepath.Join(w.hooks, "post-checkout"), "sleep 2\n")

	res := w.reap("--deadline", "1s")
	if res.Outcome != "deadline" {
		t.Fatalf("outcome %q, want deadline (created %v)", res.Outcome, res.Created)
	}
}

// A git that ignores SIGTERM during worktree add is killed once the 30-second
// grace passes, and the worktree it was creating is left broken.
func TestGitIgnoringSIGTERMIsKilledAfterGrace(t *testing.T) {
	w := newWorld(t, "create-timeout = \"1s\"\n")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	w.writeScript(filepath.Join(w.binDir, "git"), fmt.Sprintf(
		`case "$*" in
*"worktree add"*) trap '' TERM; sleep 300 ;;
*) exec %q "$@" ;;
esac
`, realGit,
	))

	start := time.Now()
	out, code := w.run("lease", "--detach", "--format=json")
	if code == 0 {
		t.Fatalf("lease succeeded with a hung git: %s", out)
	}
	if took := time.Since(start); took < 25*time.Second || took > 2*time.Minute {
		t.Fatalf("lease returned after %s, want about the 30-second grace", took)
	}
	var broken []string
	for _, r := range w.list() {
		if r.Status == "broken" {
			broken = append(broken, r.ID)
		}
	}
	if len(broken) != 1 {
		t.Fatalf("want one broken worktree, got %v (list: %+v)", broken, w.list())
	}
	if info, code := w.run("info", broken[0], "--format=json"); code != 0 || !strings.Contains(info, `"broken"`) {
		t.Fatalf("info %s: exit %d: %s", broken[0], code, info)
	}
}
