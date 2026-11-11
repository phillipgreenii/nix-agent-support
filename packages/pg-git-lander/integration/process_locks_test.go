//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This file holds the process-level lock tests of section 15.3: they need
// real processes (signals, a killed holder, a stopped holder, concurrent
// landings), so they drive the real binary through the harness helpers and
// make every race deterministic with FIFOs and marker files.

// newHarness builds a scenario's repository, hooks, config and FIFOs exactly
// as execScenario does, but hands the harness to the test so it can run its own
// sequence of processes and signals.
func newHarness(t *testing.T, sc Scenario) *harness {
	t.Helper()
	dir, err := os.MkdirTemp(fixtureRoot, "scn-")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	_ = os.Remove(dir) // copyTree needs a destination that does not exist yet
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h := &harness{t: t, sc: sc, dir: dir, shape: shapes[sc.Shape], procs: map[string]*proc{}}
	h.repo = filepath.Join(dir, "repo")
	h.hooks = filepath.Join(dir, "hooks")
	h.marks = filepath.Join(dir, "markers")
	home := filepath.Join(dir, "home")
	h.env = append(gitEnvBase(home, globalConfigPath),
		"PGL_REPO="+h.repo, "PGL_SCN="+dir, "PGL_MARKERS="+h.marks, "PGL_HOOKS="+h.hooks)
	if err := h.setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() {
		for _, p := range h.procs {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Signal(syscall.SIGCONT)
				_ = p.cmd.Process.Kill()
			}
		}
	})
	return h
}

func (h *harness) mustGit(dir string, args ...string) string {
	h.t.Helper()
	out, err := h.gitHere(dir, args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// addBranch creates branch name from main in its own worktree, with one commit
// that adds <name>.txt, and returns the worktree path.
func (h *harness) addBranch(name string) string {
	h.t.Helper()
	wt := filepath.Join(h.dir, "wt-"+name)
	h.mustGit(h.repo, "worktree", "add", "-q", "-b", name, wt, "main")
	if err := os.WriteFile(filepath.Join(wt, name+".txt"), []byte(name+"\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
	h.mustGit(wt, "add", name+".txt")
	h.mustGit(wt, "commit", "-q", "-m", "add "+name)
	return wt
}

// run starts the tool and waits for it.
func (h *harness) run(args ...string) *result {
	h.t.Helper()
	p, err := h.startTool(args)
	if err != nil {
		h.t.Fatal(err)
	}
	r, err := h.finish(p)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// start launches the tool in the background under name.
func (h *harness) start(name string, args ...string) *proc {
	h.t.Helper()
	p, err := h.startTool(args)
	if err != nil {
		h.t.Fatal(err)
	}
	h.procs[name] = p
	return p
}

func (h *harness) wait(name string) *result {
	h.t.Helper()
	r, err := h.finish(h.procs[name])
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) await(fifo string) {
	h.t.Helper()
	if err := h.fifoOp(fifo, false); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) release(fifo string) {
	h.t.Helper()
	if err := h.fifoOp(fifo, true); err != nil {
		h.t.Fatal(err)
	}
}

// expect checks the outcome word, exit code and (when set) the reason.
func expect(t *testing.T, r *result, exit int, outcome, reason string) {
	t.Helper()
	if r.exit != exit || r.json == nil || r.json["outcome"] != outcome || (reason != "" && r.json["reason"] != reason) {
		t.Fatalf("want exit %d outcome %s reason %q, got exit %d stdout %q stderr %q", exit, outcome, reason, r.exit, r.stdout, r.stderr)
	}
}

func (h *harness) noProblems() {
	h.t.Helper()
	if p := h.postCondition(); len(p) != 0 {
		h.t.Fatalf("post-condition: %v", p)
	}
}

func (h *harness) lockRefs() string {
	return h.mustGit(h.repo, "for-each-ref", "--format=%(refname)", "refs/pg-git-lander/")
}

// readPid reads a pid file a hook wrote into the markers directory.
func (h *harness) readPid(name string) int {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.marks, name))
	if err != nil {
		h.t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		h.t.Fatalf("pid file %s: %v", name, err)
	}
	return pid
}

// groupOf returns the process group of the process whose pid a hook wrote down.
func (h *harness) groupOf(pidFile string) int {
	h.t.Helper()
	pgid, err := syscall.Getpgid(h.readPid(pidFile))
	if err != nil {
		h.t.Fatal(err)
	}
	return pgid
}

// groupGone waits (bounded) until no process of the group remains.
func groupGone(pgid int) bool {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// gone waits (bounded) until the process no longer exists.
func gone(pid int) bool {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func strs(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, e := range l {
			out = append(out, fmt.Sprint(e))
		}
	}
	return out
}

// blockingHook is a hook body that, only the first time it runs, writes its
// pid, tells the test it is there and blocks until the test releases it.
const blockingHook = `
if [ ! -e "$PGL_MARKERS/first" ]; then
  : > "$PGL_MARKERS/first"
  echo $$ > "$PGL_MARKERS/hook.pid"
  echo up > "$PGL_MARKERS/up.fifo"
  cat "$PGL_MARKERS/go.fifo" > /dev/null
fi
exit 0
`

// TestEightProcessesLandIntoOneTarget: eight real processes land eight
// branches into one TARGET at once, retrying busy.
func TestEightProcessesLandIntoOneTarget(t *testing.T) {
	h := newHarness(t, Scenario{Name: "eight-landings", Shape: ShapeAhead})
	const n = 8
	for i := 1; i <= n; i++ {
		h.addBranch(fmt.Sprintf("b%d", i))
	}
	results := make([]*result, n)
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 20; attempt++ {
				p, err := h.startTool([]string{"land", fmt.Sprintf("b%d", i), "--wait", "60s"})
				if err != nil {
					t.Error(err)
					return
				}
				r, err := h.finish(p)
				if err != nil {
					t.Error(err)
					return
				}
				results[i-1] = r
				if r.exit != 3 { // busy is always retryable
					return
				}
			}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r == nil {
			t.Fatalf("b%d produced no result", i+1)
		}
		expect(t, r, 0, "landed", "")
	}
	if merges := h.mustGit(h.repo, "rev-list", "--merges", "main"); merges != "" {
		t.Fatalf("history of main is not linear, merges: %s", merges)
	}
	if got := h.mustGit(h.repo, "rev-list", "--count", "main"); got != strconv.Itoa(n+1) { // the base plus the eight
		t.Fatalf("main has %s commits, want %d", got, n+1)
	}
	subjects := h.mustGit(h.repo, "log", "--format=%s", "main")
	files := h.mustGit(h.repo, "ls-tree", "-r", "--name-only", "main")
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("b%d", i)
		if !strings.Contains(subjects, "add "+name+"\n") && !strings.HasSuffix(subjects, "add "+name) {
			t.Errorf("commit of %s missing from main: %q", name, subjects)
		}
		if !strings.Contains(files, name+".txt") {
			t.Errorf("file of %s missing from main's tree", name)
		}
	}
	if refs := h.lockRefs(); refs != "" {
		t.Fatalf("lock refs remain: %s", refs)
	}
	events, err := os.Open(filepath.Join(h.repo, ".git", "pg-git-lander", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	lines := 0
	sc := bufio.NewScanner(events)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines++
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("events.jsonl line %d does not parse: %v: %q", lines, err, sc.Text())
		}
	}
	if lines == 0 {
		t.Fatal("events.jsonl is empty")
	}
	h.noProblems()
}

// plantSlowDriver plants a merge driver that blocks mid-rebase: git, not the
// tool, runs it, so the test has a real half-done rebase to crash in.
func (h *harness) plantSlowDriver() {
	h.t.Helper()
	script := filepath.Join(h.hooks, "slow-driver")
	body := "#!/bin/sh\n" + blockingHook + "\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		h.t.Fatal(err)
	}
	h.mustGit(h.repo, "config", "--local", "merge.slow.driver", script+" %O %A %B")
	attr := filepath.Join(h.repo, ".git", "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(attr), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(attr, []byte("flake.lock merge=slow\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// TestSigkillHolderMidRebase: SIGKILL the holder in the middle of a rebase and
// reap it; the next invocation reports holder-died and --abort restores the
// start state.
func TestSigkillHolderMidRebase(t *testing.T) {
	h := newHarness(t, Scenario{Name: "sigkill-mid-rebase", Shape: ShapeLockfile, Fifos: []string{"up", "go"}, Layout: []Worktree{{Name: "src", Branch: "feature/x"}}})
	h.plantSlowDriver()
	startMain := h.mustGit(h.repo, "rev-parse", "main")
	startFeat := h.mustGit(h.repo, "rev-parse", "feature/x")

	p := h.start("holder", "land", "feature/x")
	h.await("up") // the merge driver is running inside git's rebase
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	p.killed = true
	<-p.done // reaped
	// git survives its parent: let the driver finish, so that git stops at its
	// conflict and drops its index lock, then wait for the whole group.
	pgid := h.groupOf("hook.pid")
	h.release("go")
	if !groupGone(pgid) {
		t.Fatal("git did not finish after the holder was killed")
	}

	r := h.run("land", "feature/x", "--wait", "10s")
	expect(t, r, 5, "refused", "holder-died")

	r = h.run("land", "feature/x", "--abort")
	expect(t, r, 0, "aborted", "")
	if got := h.mustGit(h.repo, "rev-parse", "main"); got != startMain {
		t.Errorf("main = %s, want the start state %s", got, startMain)
	}
	if got := h.mustGit(h.repo, "rev-parse", "feature/x"); got != startFeat {
		t.Errorf("feature/x = %s, want the start state %s", got, startFeat)
	}
	h.noProblems()
}

// blockingConfig names the first-run-blocking hook at the given land point, and
// a one-second lease so that a stopped holder lapses quickly.
func blockingConfig(point string) string {
	return `
[defaults.lock]
lease_ttl = "1s"
wait = "30s"

[[repo]]
root = "{repo}"

[repo.hooks.land]
` + point + ` = [{ run = ["{hooks}/blocker"], timeout = "60s" }]
`
}

// TestSigstopHolderPastItsLease: SIGSTOP the holder for longer than its lease;
// a waiter converts its record, and after SIGCONT the old holder ends with
// lock-lost and moves nothing.
func TestSigstopHolderPastItsLease(t *testing.T) {
	h := newHarness(t, Scenario{
		Name: "sigstop-past-lease", Shape: ShapeAhead, Fifos: []string{"up", "go"},
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: blockingConfig("before_move"),
		Hooks:  map[string]string{"blocker": blockingHook},
	})
	h.addBranch("y")
	startMain := h.mustGit(h.repo, "rev-parse", "main")

	holder := h.start("holder", "land", "feature/x")
	h.await("up") // the holder is inside before_move with every lock held
	if err := holder.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// The stopped holder renews nothing; the waiter watches the record stay
	// unchanged for a lease, then converts it.
	r := h.run("land", "y", "--wait", "30s")
	expect(t, r, 5, "refused", "holder-died")
	if err := holder.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	h.release("go") // the hook ends; the holder must then find its records gone
	r = h.wait("holder")
	expect(t, r, 4, "stopped", "lock-lost")
	if got := h.mustGit(h.repo, "rev-parse", "main"); got != startMain {
		t.Fatalf("main moved to %s from %s", got, startMain)
	}
	expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
	h.noProblems()
}

// TestSigtermDuringLongHook: SIGTERM during a long before_move hook; the
// hook's process group dies, the record is stopped with interrupted, TARGET's
// lock is released, and --continue then lands.
func TestSigtermDuringLongHook(t *testing.T) {
	h := newHarness(t, Scenario{
		Name: "sigterm-long-hook", Shape: ShapeAhead, Fifos: []string{"up", "go"},
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: blockingConfig("before_move"),
		Hooks:  map[string]string{"blocker": blockingHook},
	})
	holder := h.start("holder", "land", "feature/x")
	h.await("up")
	hookPid := h.readPid("hook.pid")
	if err := holder.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	r := h.wait("holder")
	expect(t, r, 4, "stopped", "interrupted")
	if !gone(hookPid) {
		t.Fatalf("the hook (pid %d) survived SIGTERM of the tool", hookPid)
	}
	refs := h.lockRefs()
	if strings.Contains(refs, "locks/heads/main") {
		t.Errorf("TARGET's lock was not released: %s", refs)
	}
	if !strings.Contains(refs, "locks/heads/feature/x") {
		t.Errorf("BRANCH's record is gone, it should be stopped: %s", refs)
	}
	if st := h.run("status"); st.json == nil || len(strs(st.json["stopped"])) != 1 {
		t.Errorf("status should list the one stopped operation: %s", st.stdout)
	}
	expect(t, h.run("land", "feature/x", "--continue"), 0, "landed", "")
	h.noProblems()
}

// TestGrandchildPastHookTimeout: a hook that spawns a grandchild and runs past
// its timeout; the whole process group is killed.
func TestGrandchildPastHookTimeout(t *testing.T) {
	h := newHarness(t, Scenario{
		Name: "grandchild-timeout", Shape: ShapeAhead,
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/spawn"], timeout = "1s" }]
`,
		Hooks: map[string]string{"spawn": `
echo $$ > "$PGL_MARKERS/hook.pid"
sleep 300 &
echo $! > "$PGL_MARKERS/grandchild.pid"
wait
`},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 4, "stopped", "timeout")
	for _, f := range []string{"hook.pid", "grandchild.pid"} {
		if pid := h.readPid(f); !gone(pid) {
			t.Errorf("%s (pid %d) survived the hook timeout", f, pid)
		}
	}
	expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
	h.noProblems()
}

// TestMissingAndNonExecutableCommandsMatchSkipExitCodes: a configured command
// that does not exist exits as 127 and one without execute permission as 126,
// so skip_exit_codes matches them.
func TestMissingAndNonExecutableCommandsMatchSkipExitCodes(t *testing.T) {
	h := newHarness(t, Scenario{
		Name: "exit-126-127", Shape: ShapeAhead,
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [
  { run = ["{hooks}/missing"], skip_exit_codes = [127] },
  { run = ["{hooks}/not-executable"], skip_exit_codes = [126] },
  { run = ["no-such-command-pgl-integration"], skip_exit_codes = [127] },
]
`,
	})
	if err := os.WriteFile(filepath.Join(h.hooks, "not-executable"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.run("land", "feature/x")
	expect(t, r, 0, "landed", "")
	var exits []int
	for _, e := range r.json["skipped_hooks"].([]any) {
		exits = append(exits, int(e.(map[string]any)["exit"].(float64)))
	}
	if fmt.Sprint(exits) != "[127 126 127]" {
		t.Fatalf("skipped hook exits = %v, want [127 126 127]; outcome %s", exits, r.stdout)
	}
	h.noProblems()
}

// The same commands without skip_exit_codes fail as hook-failed, so the codes
// are what the skip matched (not a silent pass).
func TestMissingCommandWithoutSkipFails(t *testing.T) {
	h := newHarness(t, Scenario{
		Name: "exit-127-fails", Shape: ShapeAhead,
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/missing"] }]
`,
	})
	expect(t, h.run("land", "feature/x"), 4, "stopped", "hook-failed")
	expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
	h.noProblems()
}

// recordEnv is a script body that writes its environment to a marker file
// named after its first argument.
const recordEnv = `env | sort > "$PGL_MARKERS/env-$1"` + "\n"

// TestRealEnvironmentReachesHooksResolversAndGitChildren: a planted commit
// hook, merge driver, configured hook and conflict resolver record what they
// see; the tool's own environment reaches all of them, minus the variables
// that would point git at another repository.
func TestRealEnvironmentReachesHooksResolversAndGitChildren(t *testing.T) {
	h := newHarness(t, Scenario{
		Name: "real-environment", Shape: ShapeLockfile,
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/record", "hook"] }]

[[repo.conflict]]
glob = "flake.lock"
resolve = ["{hooks}/resolver"]
`,
		Hooks: map[string]string{
			"record":     recordEnv,
			"pre-commit": recordEnv + "exit 0\n",
			"resolver":   recordEnv + `cat "$PG_GIT_LANDER_BRANCH_FILE" > "$PG_GIT_LANDER_PATH"` + "\n",
			"driver":     `env | sort > "$PGL_MARKERS/env-driver"` + "\nexit 1\n",
		},
	})
	// "pre-commit" and the resolver take their label from $1, which git and
	// the tool do not pass; give them a fixed one.
	for name, label := range map[string]string{"pre-commit": "commit-hook", "resolver": "resolver"} {
		body, _ := os.ReadFile(filepath.Join(h.hooks, name))
		body = []byte(strings.Replace(string(body), `"$PGL_MARKERS/env-$1"`, `"$PGL_MARKERS/env-`+label+`"`, 1))
		if err := os.WriteFile(filepath.Join(h.hooks, name), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.mustGit(h.repo, "config", "--local", "merge.rec.driver", filepath.Join(h.hooks, "driver")+" %O %A %B")
	if err := os.WriteFile(filepath.Join(h.repo, ".git", "info", "attributes"), []byte("flake.lock merge=rec\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The tool's environment carries variables a hermetic hook must still
	// see, and two that would redirect git to another repository.
	clean := h.env
	h.env = append(append([]string{}, clean...),
		"PGL_PROBE=sentinel-value", "GNUPGHOME="+filepath.Join(h.dir, "gnupg"),
		"GIT_DIR=/nonexistent-pgl-gitdir", "GIT_WORK_TREE=/nonexistent-pgl-worktree")
	r := h.run("land", "feature/x")
	h.env = clean
	expect(t, r, 0, "landed", "")

	for _, who := range []string{"hook", "resolver", "commit-hook", "driver"} {
		b, err := os.ReadFile(filepath.Join(h.marks, "env-"+who))
		if err != nil {
			t.Fatalf("%s did not run: %v", who, err)
		}
		env := string(b)
		for _, want := range []string{
			"PGL_PROBE=sentinel-value", "XDG_CONFIG_HOME=" + filepath.Join(h.dir, "home", ".config"),
			"TMPDIR=" + filepath.Join(h.dir, "home", "tmp"), "GIT_CONFIG_GLOBAL=" + globalConfigPath,
			"GNUPGHOME=" + filepath.Join(h.dir, "gnupg"), "HOME=" + filepath.Join(h.dir, "home"),
		} {
			if !hasLine(env, want) {
				t.Errorf("%s did not see %s; its environment:\n%s", who, want, env)
			}
		}
		if strings.Contains(env, "nonexistent-pgl") {
			t.Errorf("%s inherited a variable that points git at another repository:\n%s", who, env)
		}
	}
	for _, who := range []string{"hook", "resolver"} {
		b, _ := os.ReadFile(filepath.Join(h.marks, "env-"+who))
		for _, want := range []string{"PG_GIT_LANDER_OPERATION=land", "PG_GIT_LANDER_BRANCH=feature/x", "PG_GIT_LANDER_TARGET=main"} {
			if !hasLine(string(b), want) {
				t.Errorf("%s did not see %s", who, want)
			}
		}
	}
	h.noProblems()
}

func hasLine(env, line string) bool {
	for _, l := range strings.Split(env, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// resumeRaceHook fails the first run (so the operation stops), blocks the
// second until the test releases it (when blocking is set) and passes after.
func resumeRaceHook(block bool) string {
	second := ":"
	if block {
		second = `echo $$ > "$PGL_MARKERS/hook.pid"; echo up > "$PGL_MARKERS/up.fifo"; cat "$PGL_MARKERS/go.fifo" > /dev/null`
	}
	return `
n=$(cat "$PGL_MARKERS/count" 2>/dev/null || echo 0)
n=$((n+1))
echo $n > "$PGL_MARKERS/count"
case $n in
  1) exit 1 ;;
  2) ` + second + ` ;;
esac
exit 0
`
}

func resumeRaceScenario(name string, block bool) Scenario {
	return Scenario{
		Name: name, Shape: ShapeAhead, Fifos: []string{"up", "go"},
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}},
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/flaky"], timeout = "60s" }]
`,
		Hooks: map[string]string{"flaky": resumeRaceHook(block)},
	}
}

// TestContinueAndAbortRaceOnOneStoppedOperation: --continue has taken the
// stopped record back (and waits in its hook) when --abort arrives; the
// second gets lost-to-concurrent and only the winner acts.
func TestContinueAndAbortRaceOnOneStoppedOperation(t *testing.T) {
	h := newHarness(t, resumeRaceScenario("continue-vs-abort", true))
	expect(t, h.run("land", "feature/x"), 4, "stopped", "hook-failed")
	h.start("continue", "land", "feature/x", "--continue")
	h.await("up") // --continue holds the record as running, inside the hook
	expect(t, h.run("land", "feature/x", "--abort"), 5, "refused", "lost-to-concurrent")
	h.release("go")
	expect(t, h.wait("continue"), 0, "landed", "")
	h.noProblems()
}

// TestContinueAndAbortStartedTogether: both flags start at the same moment on
// one stopped operation. Whichever takes the record first wins; the other is
// refused (lost-to-concurrent, or nothing-stopped when the winner had already
// finished), and the repository ends consistent either way.
func TestContinueAndAbortStartedTogether(t *testing.T) {
	h := newHarness(t, resumeRaceScenario("continue-with-abort", false))
	startMain := h.mustGit(h.repo, "rev-parse", "main")
	expect(t, h.run("land", "feature/x"), 4, "stopped", "hook-failed")
	h.start("continue", "land", "feature/x", "--continue")
	h.start("abort", "land", "feature/x", "--abort")
	c, a := h.wait("continue"), h.wait("abort")
	losers := func(r *result) bool {
		return r.exit == 5 && (r.json["reason"] == "lost-to-concurrent" || r.json["reason"] == "nothing-stopped")
	}
	switch {
	case c.exit == 0 && c.json["outcome"] == "landed" && losers(a):
		if got := h.mustGit(h.repo, "rev-parse", "main"); got == startMain {
			t.Error("--continue won but main did not move")
		}
	case a.exit == 0 && a.json["outcome"] == "aborted" && losers(c):
		if got := h.mustGit(h.repo, "rev-parse", "main"); got != startMain {
			t.Error("--abort won but main moved")
		}
	default:
		t.Fatalf("exactly one flag must win and the other lose:\ncontinue: exit %d %s\nabort: exit %d %s", c.exit, c.stdout, a.exit, a.stdout)
	}
	h.noProblems()
}

// TestParallelLandingsIntoDifferentTargets: landings into two TARGETs and a
// rebase of an unrelated branch overlap in time, shown by rendezvous hooks that
// each wait for the others' marker files rather than by timestamps.
func TestParallelLandingsIntoDifferentTargets(t *testing.T) {
	const hook = `
: > "$PGL_MARKERS/in-$PG_GIT_LANDER_BRANCH"
i=0
while [ ! -e "$PGL_MARKERS/in-a" ] || [ ! -e "$PGL_MARKERS/in-b" ] || [ ! -e "$PGL_MARKERS/in-c" ]; do
  i=$((i+1))
  [ $i -gt 1200 ] && exit 1
  sleep 0.05
done
exit 0
`
	h := newHarness(t, Scenario{
		Name: "parallel-targets", Shape: ShapeAhead,
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before = [{ run = ["{hooks}/rendezvous"], timeout = "60s" }]

[repo.hooks.rebase]
before = [{ run = ["{hooks}/rendezvous"], timeout = "60s" }]
`,
		Hooks: map[string]string{"rendezvous": hook},
	})
	h.addBranch("c") // to be rebased onto a main that has since moved
	if err := os.WriteFile(filepath.Join(h.repo, "m.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustGit(h.repo, "add", "m.txt")
	h.mustGit(h.repo, "commit", "-q", "-m", "main moves")
	h.mustGit(h.repo, "branch", "release", "main")
	h.mustGit(h.repo, "worktree", "add", "-q", filepath.Join(h.dir, "wt-release"), "release")
	h.addBranch("a")
	h.addBranch("b")

	h.start("a", "land", "a")
	h.start("b", "land", "b", "--into", "release")
	h.start("c", "rebase", "c", "--onto", "main")
	expect(t, h.wait("a"), 0, "landed", "")
	expect(t, h.wait("b"), 0, "landed", "")
	expect(t, h.wait("c"), 0, "rebased", "")
	for _, who := range []string{"a", "b", "c"} {
		if _, err := os.Stat(filepath.Join(h.marks, "in-"+who)); err != nil {
			t.Errorf("operation %s never reached its hook: %v", who, err)
		}
	}
	if got := h.mustGit(h.repo, "log", "--format=%s", "-1", "main"); got != "add a" {
		t.Errorf("main's tip is %q, want the landed a", got)
	}
	if got := h.mustGit(h.repo, "log", "--format=%s", "-1", "release"); got != "add b" {
		t.Errorf("release's tip is %q, want the landed b", got)
	}
	h.noProblems()
}
