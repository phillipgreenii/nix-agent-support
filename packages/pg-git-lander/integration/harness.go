//go:build integration

// Package integration is the integration scenario harness of pg-git-lander:
// it starts the real binary and real git in a hermetic temporary environment,
// builds the repository fixtures once per test process, runs declarative
// scenario tables, compares the outcome JSON subset and a repository
// snapshot, enforces the post-condition after every scenario, and makes races
// and crashes deterministic with hook scripts, marker files and FIFOs rather
// than sleeps. Every file carries the integration build tag, so the unit tier
// never compiles it.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Worktree is one extra worktree of a scenario's layout, added with
// git worktree add <scenario>/wt-<Name> <Branch> after the fixture is copied.
// The main worktree is always called "main" and sits at <scenario>/repo.
type Worktree struct {
	Name   string
	Branch string // checked out; empty with Detach means detached at main's tip
	Detach bool
	// Dirty writes an untracked file of this name into the worktree.
	Dirty string
}

// Step is one action of a scenario; exactly one action field is set.
type Step struct {
	// Run starts the tool with these arguments and waits for it; the result
	// becomes the scenario's current result.
	Run []string
	// Start launches the tool in the background under Name.
	Start []string
	// StartGit launches git (in the main worktree) in the background under
	// Name, for example a commit whose hook rendezvouses with the test.
	StartGit []string
	// Name is the handle of Start, StartGit, Wait and Kill.
	Name string
	// Wait waits for the background process Name and makes its result current.
	Wait bool
	// Kill sends SIGKILL to the background process Name (a crash).
	Kill bool
	// Await blocks until a hook has written to the FIFO of this name: the
	// hook has reached its rendezvous point.
	Await string
	// Release unblocks a hook waiting on the FIFO of this name.
	Release string
	// Git runs git in the main worktree (for example, to advance TARGET).
	Git []string
	// AdvanceRemote commits a file to the bare origin.git (ShapeOrigin only)
	// as "<file>=<content>" and the test then fetches.
	AdvanceRemote string
}

// Snapshot is a partial expectation of the repository state; unset fields are
// not compared. Values starting with "@" name a fixture fact: "@feature/x" is
// the original tip of feature/x, "@tree:feature/x" its tree.
type Snapshot struct {
	// Refs maps full ref names to expected ids; "" means the ref is absent.
	Refs map[string]string
	// Worktrees maps a layout name ("main" for the main worktree) to the
	// expected HEAD ("branch:<name>" or "detached:<id>") and status --porcelain.
	Worktrees map[string]WorktreeState
	// TargetTree is the expected tree hash of main (not its commit id, so a
	// rebase does not change the expectation).
	TargetTree string
}

// WorktreeState is the expectation of one worktree.
type WorktreeState struct {
	Head   string // "" means not compared
	Status string // compared when CheckStatus is set (empty means clean)
	// CheckStatus enables the status comparison.
	CheckStatus bool
}

// Expected is the expected end state of a scenario.
type Expected struct {
	// Outcome is a subset of the final result's outcome JSON. Maps are
	// compared as subsets; every other value must be equal. String values may
	// use {repo}, {scn} and {wt:NAME}.
	Outcome map[string]any
	// Snapshot is the repository state.
	Snapshot Snapshot
	// Stopped relaxes the post-condition for a scenario that ends with a
	// stopped operation: lock refs, pg-git-lander/* branches and dirty
	// worktrees are then expected and are not violations. fsck must still pass.
	Stopped bool
}

// Scenario is one declarative table entry.
type Scenario struct {
	Name   string
	Shape  Shape
	Layout []Worktree
	// Config is the user config.toml written to the XDG config directory;
	// {hooks}, {markers}, {repo} and {scn} are replaced.
	Config string
	// Hooks maps a git hook name (pre-commit, pre-rebase, ...) or a script
	// name used from Config to its body; they live in {hooks} and git's
	// core.hooksPath names that directory.
	Hooks map[string]string
	// Fifos names the rendezvous FIFOs created in {markers}. A hook blocks
	// with `echo > "$PGL_MARKERS/up.fifo"; cat "$PGL_MARKERS/go.fifo"`, and a
	// step Awaits "up" and Releases "go" (names without the .fifo suffix).
	Fifos []string
	Steps []Step
	// Expected is compared after the steps; the post-condition always is.
	Expected Expected
}

// harness holds the per-scenario state.
type harness struct {
	t      testing.TB
	sc     Scenario
	dir    string
	repo   string
	hooks  string
	marks  string
	env    []string
	shape  *builtShape
	procs  map[string]*proc
	result *result
}

type result struct {
	exit   int
	stdout string
	stderr string
	json   map[string]any
}

type proc struct {
	cmd    *exec.Cmd
	out    *bytes.Buffer
	errb   *bytes.Buffer
	done   chan struct{}
	killed bool
}

const stepTimeout = 60 * time.Second

func (h *harness) wtPath(name string) string {
	if name == "main" {
		return h.repo
	}
	return filepath.Join(h.dir, "wt-"+name)
}

func (h *harness) expand(s string) string {
	pairs := []string{"{repo}", h.repo, "{scn}", h.dir, "{hooks}", h.hooks, "{markers}", h.marks, "{origin}", filepath.Join(h.dir, "origin.git")}
	for _, w := range h.sc.Layout {
		pairs = append(pairs, "{wt:"+w.Name+"}", h.wtPath(w.Name))
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

func (h *harness) expandAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = h.expand(s)
	}
	return out
}

func (h *harness) gitHere(dir string, args ...string) (string, error) {
	return runGit(h.env, dir, nil, args...)
}

// setup copies the shape, adds the layout, plants hooks, config and FIFOs.
func (h *harness) setup() error {
	if err := copyTree(h.shape.dir, h.dir); err != nil {
		return err
	}
	for _, d := range []string{h.hooks, h.marks, filepath.Join(h.dir, "home", "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if h.sc.Shape == ShapeOrigin {
		// The copy's remote still names the build-time path.
		if _, err := h.gitHere(h.repo, "remote", "set-url", "origin", filepath.Join(h.dir, "origin.git")); err != nil {
			return err
		}
	}
	for _, w := range h.sc.Layout {
		args := []string{"worktree", "add", "-q"}
		switch {
		case w.Detach:
			args = append(args, "--detach", h.wtPath(w.Name), "main")
		default:
			args = append(args, h.wtPath(w.Name), w.Branch)
		}
		if _, err := h.gitHere(h.repo, args...); err != nil {
			return err
		}
		if w.Dirty != "" {
			if err := os.WriteFile(filepath.Join(h.wtPath(w.Name), w.Dirty), []byte("dirty\n"), 0o644); err != nil {
				return err
			}
		}
	}
	if len(h.sc.Hooks) > 0 {
		for name, body := range h.sc.Hooks {
			if !strings.HasPrefix(body, "#!") {
				body = "#!/bin/sh\n" + body
			}
			if err := os.WriteFile(filepath.Join(h.hooks, name), []byte(h.expand(body)), 0o755); err != nil {
				return err
			}
		}
		if _, err := h.gitHere(h.repo, "config", "--local", "core.hooksPath", h.hooks); err != nil {
			return err
		}
	}
	for _, f := range h.sc.Fifos {
		if err := syscall.Mkfifo(filepath.Join(h.marks, f+".fifo"), 0o600); err != nil {
			return err
		}
	}
	if h.sc.Config != "" {
		cfgDir := filepath.Join(h.dir, "home", ".config", "pg-git-lander")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(h.expand(h.sc.Config)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func toolArgs(h *harness, args []string) []string {
	args = h.expandAll(args)
	has := func(f string) bool {
		for _, a := range args {
			if a == f {
				return true
			}
		}
		return false
	}
	if len(args) > 0 && args[0] != "log" && args[0] != "--version" && args[0] != "--help" && !has("--json") {
		args = append(args, "--json")
	}
	if !has("--repo") {
		args = append(args, "--repo", h.repo)
	}
	return args
}

func (h *harness) startTool(args []string) (*proc, error) {
	return h.startCmd(exec.Command(toolBinary, toolArgs(h, args)...))
}

func (h *harness) startCmd(cmd *exec.Cmd) (*proc, error) {
	cmd.Dir = h.repo
	cmd.Env = h.env
	p := &proc{cmd: cmd, out: &bytes.Buffer{}, errb: &bytes.Buffer{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (h *harness) finish(p *proc) (*result, error) {
	select {
	case <-p.done:
	case <-time.After(stepTimeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		return nil, fmt.Errorf("tool did not finish within %s; stderr: %s", stepTimeout, p.errb.String())
	}
	r := &result{exit: p.cmd.ProcessState.ExitCode(), stdout: p.out.String(), stderr: p.errb.String()}
	if p.killed {
		r.exit = -1
	}
	var m map[string]any
	if json.Unmarshal(p.out.Bytes(), &m) == nil {
		r.json = m
	}
	return r, nil
}

// fifoOpen opens a FIFO with a bounded wait, so a hook that never reaches its
// rendezvous point fails the scenario instead of hanging it.
func (h *harness) fifoOp(name string, write bool) error {
	path := filepath.Join(h.marks, name+".fifo")
	errc := make(chan error, 1)
	go func() {
		flag := os.O_RDONLY
		if write {
			flag = os.O_WRONLY
		}
		f, err := os.OpenFile(path, flag, 0)
		if err != nil {
			errc <- err
			return
		}
		if !write {
			buf := make([]byte, 16)
			_, _ = f.Read(buf)
		}
		errc <- f.Close()
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(stepTimeout):
		// Unblock the stuck opener from the other side, then give up.
		other := os.O_WRONLY | syscall.O_NONBLOCK
		if write {
			other = os.O_RDONLY | syscall.O_NONBLOCK
		}
		if f, err := os.OpenFile(path, other, 0); err == nil {
			_ = f.Close()
		}
		return fmt.Errorf("fifo %s: nobody on the other side within %s", name, stepTimeout)
	}
}

func (h *harness) runSteps() error {
	for i, st := range h.sc.Steps {
		var err error
		switch {
		case st.Run != nil:
			var p *proc
			if p, err = h.startTool(st.Run); err == nil {
				h.result, err = h.finish(p)
			}
		case st.Start != nil:
			var p *proc
			if p, err = h.startTool(st.Start); err == nil {
				h.procs[st.Name] = p
			}
		case st.StartGit != nil:
			var p *proc
			if p, err = h.startCmd(exec.Command("git", h.expandAll(st.StartGit)...)); err == nil {
				h.procs[st.Name] = p
			}
		case st.Wait:
			p := h.procs[st.Name]
			if p == nil {
				err = fmt.Errorf("no background process %q", st.Name)
			} else {
				h.result, err = h.finish(p)
			}
		case st.Kill:
			p := h.procs[st.Name]
			if p == nil {
				err = fmt.Errorf("no background process %q", st.Name)
			} else {
				p.killed = true
				err = p.cmd.Process.Signal(syscall.SIGKILL)
				<-p.done
			}
		case st.Await != "":
			err = h.fifoOp(st.Await, false)
		case st.Release != "":
			err = h.fifoOp(st.Release, true)
		case st.Git != nil:
			_, err = h.gitHere(h.repo, h.expandAll(st.Git)...)
		case st.AdvanceRemote != "":
			err = h.advanceRemote(st.AdvanceRemote)
		default:
			err = fmt.Errorf("empty step")
		}
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

func (h *harness) advanceRemote(spec string) error {
	name, content, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("AdvanceRemote %q is not file=content", spec)
	}
	clone := filepath.Join(h.dir, "remote-work")
	origin := filepath.Join(h.dir, "origin.git")
	if _, err := runGit(h.env, h.dir, nil, "clone", "-q", origin, clone); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(clone, name), []byte(content+"\n"), 0o644); err != nil {
		return err
	}
	for _, a := range [][]string{{"add", name}, {"commit", "-q", "-m", "advance remote"}, {"push", "-q", "origin", "main"}} {
		if _, err := runGit(h.env, clone, nil, a...); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(clone); err != nil {
		return err
	}
	_, err := h.gitHere(h.repo, "fetch", "-q", "origin")
	return err
}

// execScenario runs the scenario and returns every problem found; an empty
// result means the scenario passed. runScenario wraps it for tests, and the
// harness's own tests use it to see a broken scenario fail.
func execScenario(t testing.TB, sc Scenario) []string {
	t.Helper()
	shape := shapes[sc.Shape]
	if shape == nil {
		return []string{fmt.Sprintf("unknown shape %q", sc.Shape)}
	}
	dir, err := os.MkdirTemp(fixtureRoot, "scn-")
	if err != nil {
		return []string{err.Error()}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	// copyTree needs a destination that does not exist yet.
	_ = os.Remove(dir)
	h := &harness{t: t, sc: sc, dir: dir, shape: shape, procs: map[string]*proc{}}
	h.repo = filepath.Join(dir, "repo")
	h.hooks = filepath.Join(dir, "hooks")
	h.marks = filepath.Join(dir, "markers")
	home := filepath.Join(dir, "home")
	h.env = append(gitEnvBase(home, globalConfigPath),
		"PGL_REPO="+h.repo, "PGL_SCN="+dir, "PGL_MARKERS="+h.marks, "PGL_HOOKS="+h.hooks)
	if err := h.setup(); err != nil {
		return []string{"setup: " + err.Error()}
	}
	defer func() {
		for _, p := range h.procs {
			if p.cmd.ProcessState == nil {
				_ = p.cmd.Process.Kill()
			}
		}
	}()
	var problems []string
	if err := h.runSteps(); err != nil {
		problems = append(problems, err.Error())
	}
	problems = append(problems, h.compareOutcome()...)
	problems = append(problems, h.compareSnapshot()...)
	problems = append(problems, h.postCondition()...)
	return problems
}

// runScenario runs a scenario as a subtest and fails it on any problem.
func runScenario(t *testing.T, sc Scenario) {
	t.Run(sc.Name, func(t *testing.T) {
		t.Parallel()
		for _, p := range execScenario(t, sc) {
			t.Error(p)
		}
	})
}

// compareOutcome checks the current result against Expected.Outcome.
func (h *harness) compareOutcome() []string {
	want := h.sc.Expected.Outcome
	if want == nil {
		return nil
	}
	r := h.result
	if r == nil {
		return []string{"expected an outcome but no step produced a result"}
	}
	if r.json == nil {
		return []string{fmt.Sprintf("stdout is not outcome JSON: %q (stderr %q)", r.stdout, r.stderr)}
	}
	var problems []string
	if code, ok := r.json["exit"].(float64); ok && int(code) != r.exit {
		problems = append(problems, fmt.Sprintf("process exit %d disagrees with outcome exit %v", r.exit, code))
	}
	norm, err := normalize(h.expandValue(want))
	if err != nil {
		return append(problems, err.Error())
	}
	return append(problems, subsetDiff("outcome", norm, r.json)...)
}

func (h *harness) expandValue(v any) any {
	switch x := v.(type) {
	case string:
		return h.expand(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = h.expandValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = h.expandValue(e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = h.expand(e)
		}
		return out
	}
	return v
}

// normalize round-trips through JSON so Go ints compare equal to decoded floats.
func normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}

// subsetDiff reports where want is not a subset of got: maps by key, every
// other value (arrays included) by equality.
func subsetDiff(path string, want, got any) []string {
	wm, ok := want.(map[string]any)
	if !ok {
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if !bytes.Equal(wb, gb) {
			return []string{fmt.Sprintf("%s = %s, want %s", path, gb, wb)}
		}
		return nil
	}
	gm, ok := got.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s is not an object", path)}
	}
	keys := make([]string, 0, len(wm))
	for k := range wm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		g, present := gm[k]
		if !present {
			out = append(out, fmt.Sprintf("%s.%s is missing", path, k))
			continue
		}
		out = append(out, subsetDiff(path+"."+k, wm[k], g)...)
	}
	return out
}

func (h *harness) fact(v string) string {
	if !strings.HasPrefix(v, "@") {
		return v
	}
	v = v[1:]
	if r, ok := strings.CutPrefix(v, "tree:"); ok {
		return h.shape.facts.Trees[r]
	}
	return h.shape.facts.Tips[v]
}

// worktreeNames returns layout name -> path for every worktree git lists.
func (h *harness) listWorktrees() (map[string]string, error) {
	out, err := h.gitHere(h.repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	byPath := map[string]string{h.repo: "main"}
	for _, w := range h.sc.Layout {
		byPath[h.wtPath(w.Name)] = w.Name
	}
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		name := byPath[p]
		if name == "" {
			name = "path:" + p
		}
		res[name] = p
	}
	return res, nil
}

func (h *harness) head(dir string) string {
	if b, err := h.gitHere(dir, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		return "branch:" + strings.TrimSpace(b)
	}
	id, _ := h.gitHere(dir, "rev-parse", "HEAD")
	return "detached:" + strings.TrimSpace(id)
}

func (h *harness) compareSnapshot() []string {
	s := h.sc.Expected.Snapshot
	var problems []string
	for ref, want := range s.Refs {
		got, err := h.gitHere(h.repo, "rev-parse", "--verify", "--quiet", ref)
		got = strings.TrimSpace(got)
		if err != nil {
			got = ""
		}
		if want = h.fact(want); got != want {
			problems = append(problems, fmt.Sprintf("ref %s = %q, want %q", ref, got, want))
		}
	}
	if len(s.Worktrees) > 0 {
		wts, err := h.listWorktrees()
		if err != nil {
			return append(problems, err.Error())
		}
		for name, want := range s.Worktrees {
			dir, ok := wts[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("worktree %s does not exist", name))
				continue
			}
			if want.Head != "" {
				if got := h.head(dir); got != h.fact(want.Head) && got != "branch:"+h.fact(want.Head) {
					problems = append(problems, fmt.Sprintf("worktree %s HEAD = %s, want %s", name, got, want.Head))
				}
			}
			if want.CheckStatus {
				got, _ := h.gitHere(dir, "status", "--porcelain")
				if strings.TrimSpace(got) != strings.TrimSpace(want.Status) {
					problems = append(problems, fmt.Sprintf("worktree %s status = %q, want %q", name, got, want.Status))
				}
			}
		}
	}
	if s.TargetTree != "" {
		got, _ := h.gitHere(h.repo, "rev-parse", "refs/heads/main^{tree}")
		if want := h.fact(s.TargetTree); strings.TrimSpace(got) != want {
			problems = append(problems, fmt.Sprintf("tree of main = %s, want %s", strings.TrimSpace(got), want))
		}
	}
	return problems
}

// postCondition is enforced after EVERY scenario: no lock refs or records, no
// pg-git-lander/* branches, every worktree clean, no stale ref .lock files and
// git fsck --no-dangling exiting 0 with empty output. A scenario that ends
// with a stopped operation (Expected.Stopped) may leave the records, the work
// branch and a dirty worktree; fsck must still pass.
func (h *harness) postCondition() []string {
	var problems []string
	stopped := h.sc.Expected.Stopped
	if !stopped {
		if out, _ := h.gitHere(h.repo, "for-each-ref", "--format=%(refname)", "refs/pg-git-lander/"); strings.TrimSpace(out) != "" {
			problems = append(problems, "lock refs or records left behind: "+strings.Join(strings.Fields(out), " "))
		}
		if out, _ := h.gitHere(h.repo, "for-each-ref", "--format=%(refname)", "refs/heads/pg-git-lander/"); strings.TrimSpace(out) != "" {
			problems = append(problems, "pg-git-lander/* branches left behind: "+strings.Join(strings.Fields(out), " "))
		}
		if wts, err := h.listWorktrees(); err != nil {
			problems = append(problems, err.Error())
		} else {
			for name, dir := range wts {
				if out, _ := h.gitHere(dir, "status", "--porcelain"); strings.TrimSpace(out) != "" {
					problems = append(problems, fmt.Sprintf("worktree %s is not clean: %q", name, out))
				}
			}
		}
	}
	common := filepath.Join(h.repo, ".git")
	_ = filepath.Walk(filepath.Join(common, "refs"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".lock") {
			problems = append(problems, "stale ref lock file "+p)
		}
		return nil
	})
	cmd := exec.Command("git", "fsck", "--no-dangling")
	cmd.Dir, cmd.Env = h.repo, h.env
	out, err := cmd.CombinedOutput()
	if err != nil || len(bytes.TrimSpace(out)) != 0 {
		problems = append(problems, fmt.Sprintf("git fsck --no-dangling: %v: %q", err, out))
	}
	return problems
}
