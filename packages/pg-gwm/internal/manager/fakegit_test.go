package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/x/cmdrun"
)

// fakeGit is an in-process stand-in for the repository: it answers the git
// questions the manager asks (a cmdrun.Runner behind a gitrun.Git) and is the
// provision.Provisioner that changes it. It holds only what state and ordering
// tests read: branches, worktrees, their heads and locks, and each worktree
// directory's files. A test that asserts what real git does with commits,
// remotes or the index MUST use newEnv instead.
type fakeGit struct {
	mu     sync.Mutex
	main   string
	common string
	seq    int
	heads  map[string]string // branch -> commit
	wts    []*fakeWT

	// tracked is the committed content of the main branch's files (path
	// relative to a worktree -> content): a new or cleaned worktree holds
	// it, and a missing or changed tracked file is unsaved work.
	tracked map[string]string
	// ignored names the files Unsaved does not report, as a .gitignore does.
	ignored map[string]bool
	// orphans are the unreachable commits of a worktree, by real path.
	orphans map[string][]string
	// preserved is the file list each Preserve wrote, by branch.
	preserved map[string][]string
	// timeoutStatus makes every Unsaved and Preserve call time out, as a
	// loaded host does; statusCalls counts those calls.
	timeoutStatus atomic.Bool
	statusCalls   atomic.Int32
	// switches records the runHook flag of every Switch.
	switches []bool
}

type fakeWT struct {
	path, branch, head string
	locked             bool
	reason             string
}

const fakeMainBranch = "main"

func (f *fakeGit) newSHA() string {
	f.seq++
	return fmt.Sprintf("%040x", f.seq)
}

func (f *fakeGit) wt(path string) *fakeWT {
	for _, w := range f.wts {
		if realPath(w.path) == realPath(path) {
			return w
		}
	}
	return nil
}

// addBranch makes a local branch at the main branch's commit.
func (f *fakeGit) addBranch(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[name] = f.heads[fakeMainBranch]
}

func gitFail(code int, msg string) cmdrun.Result {
	return cmdrun.Result{Code: code, Stderr: []byte(msg + "\n")}
}

func okOut(s string) cmdrun.Result { return cmdrun.Result{Stdout: []byte(s)} }

// run is the cmdrun.Runner: it answers the exact git commands the manager and
// repo.Resolve issue and fails the call on any other.
func (f *fakeGit) run(_ context.Context, c cmdrun.Call) cmdrun.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(c.Args) < 3 || c.Args[0] != "-C" {
		return gitFail(1, "fake git: unexpected call "+strings.Join(c.Args, " "))
	}
	dir, args := c.Args[1], c.Args[2:]
	switch args[0] {
	case "rev-parse":
		return f.revParse(dir, args[1:])
	case "worktree":
		if len(args) == 3 && args[1] == "list" {
			return okOut(f.worktreeList())
		}
	case "remote":
		return okOut("")
	case "check-ref-format":
		if f.validRef(strings.TrimPrefix(args[1], "refs/heads/")) {
			return okOut("")
		}
		return gitFail(1, "")
	case "for-each-ref":
		return f.forEachRef(args[1:])
	case "branch":
		if len(args) == 3 && args[1] == "-D" {
			if _, ok := f.heads[args[2]]; !ok {
				return gitFail(1, "error: branch '"+args[2]+"' not found.")
			}
			delete(f.heads, args[2])
			return okOut("")
		}
	}
	return gitFail(1, "fake git: unsupported "+strings.Join(args, " "))
}

func (f *fakeGit) revParse(dir string, args []string) cmdrun.Result {
	switch {
	case len(args) == 2 && args[1] == "--git-common-dir":
		return okOut(f.common + "\n")
	case len(args) == 1 && args[0] == "HEAD":
		if w := f.wt(dir); w != nil {
			return okOut(w.head + "\n")
		}
		return okOut(f.heads[fakeMainBranch] + "\n")
	case len(args) == 2 && args[0] == "--verify":
		ref := strings.TrimSuffix(args[1], "^{commit}")
		if ref == "HEAD" {
			return okOut(f.heads[fakeMainBranch] + "\n")
		}
		if sha, ok := f.heads[ref]; ok {
			return okOut(sha + "\n")
		}
		for _, sha := range f.heads {
			if sha == ref {
				return okOut(sha + "\n")
			}
		}
		for _, w := range f.wts {
			if w.head == ref {
				return okOut(ref + "\n")
			}
		}
		return gitFail(128, "fatal: Needed a single revision")
	}
	return gitFail(1, "fake git: unsupported rev-parse "+strings.Join(args, " "))
}

func (f *fakeGit) worktreeList() string {
	var b strings.Builder
	fmt.Fprintf(&b, "worktree %s\nHEAD %s\nbranch refs/heads/%s\n\n", f.main, f.heads[fakeMainBranch], fakeMainBranch)
	for _, w := range f.wts {
		fmt.Fprintf(&b, "worktree %s\nHEAD %s\n", w.path, w.head)
		if w.branch == "" {
			b.WriteString("detached\n")
		} else {
			fmt.Fprintf(&b, "branch refs/heads/%s\n", w.branch)
		}
		if w.locked {
			b.WriteString("locked " + w.reason + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (f *fakeGit) validRef(b string) bool {
	if b == "" || strings.HasPrefix(b, "-") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.Contains(b, "//") || strings.Contains(b, "@{") {
		return false
	}
	return !strings.ContainsAny(b, " ~^:?*[\\") && !strings.Contains(b, "..")
}

func (f *fakeGit) sortedHeads() []string {
	var names []string
	for h := range f.heads {
		names = append(names, h)
	}
	sort.Strings(names)
	return names
}

func (f *fakeGit) forEachRef(args []string) cmdrun.Result {
	var out strings.Builder
	switch {
	case len(args) > 0 && args[0] == "--contains":
		sha := f.heads[strings.TrimPrefix(args[1], "refs/heads/")]
		for _, h := range f.sortedHeads() {
			if f.heads[h] == sha {
				out.WriteString("refs/heads/" + h + "\n")
			}
		}
	case len(args) == 3 && args[1] == "refs/heads" && args[2] == "refs/remotes":
		for _, h := range f.sortedHeads() {
			out.WriteString("refs/heads/" + h + "\n")
		}
	case len(args) == 2:
		prefix := strings.TrimSuffix(strings.TrimPrefix(args[1], "refs/heads/"), "*")
		for _, h := range f.sortedHeads() {
			if strings.HasPrefix(h, prefix) {
				out.WriteString("refs/heads/" + h + "\t0\n")
			}
		}
	default:
		return gitFail(1, "fake git: unsupported for-each-ref "+strings.Join(args, " "))
	}
	return okOut(out.String())
}

// unsavedFiles lists the work in dir: tracked files that are missing or
// changed, and every file that is neither tracked, ignored nor the .git marker.
func (f *fakeGit) unsavedFiles(dir string) []string {
	var names []string
	for rel, want := range f.tracked {
		if got, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || string(got) != want {
			names = append(names, rel)
		}
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if _, ok := f.tracked[e.Name()]; !ok && e.Name() != ".git" && !f.ignored[e.Name()] {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// restore writes every tracked file back to its committed content.
func (f *fakeGit) restore(dir string) error {
	for rel, content := range f.tracked {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// commitFile commits rel with content on the main branch: worktrees created or
// cleaned afterwards hold it.
func (f *fakeGit) commitFile(rel, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tracked[rel] = content
}

// ignore marks the file names as ignored.
func (f *fakeGit) ignore(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range names {
		f.ignored[n] = true
	}
}

// addOrphan gives the worktree at dir a commit no branch reaches.
func (f *fakeGit) addOrphan(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := realPath(dir)
	f.orphans[k] = append(f.orphans[k], f.newSHA())
}

// addBranchAt makes a local branch at a commit of its own (no other branch
// contains it) and returns the commit.
func (f *fakeGit) addBranchAt(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := f.newSHA()
	f.heads[name] = sha
	return sha
}

// addWorktree registers a worktree git knows that no lease made (a hand-made
// one), checked out on branch.
func (f *fakeGit) addWorktree(dir, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: fake\n"), 0o644); err != nil {
		return err
	}
	f.wts = append(f.wts, &fakeWT{path: dir, branch: branch, head: f.heads[branch]})
	return nil
}

// forget drops the worktree at dir from git's knowledge, as removing its
// administrative directory does; its files stay.
func (f *fakeGit) forget(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.wts {
		if realPath(x.path) == realPath(dir) {
			f.wts = append(f.wts[:i], f.wts[i+1:]...)
			return
		}
	}
}

// branchesWithPrefix lists the local branches under prefix.
func (f *fakeGit) branchesWithPrefix(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, h := range f.sortedHeads() {
		if strings.HasPrefix(h, prefix) {
			out = append(out, h)
		}
	}
	return out
}

func (f *fakeGit) apply(w *fakeWT, b provision.BranchSpec) {
	switch {
	case b.Detach:
		w.branch, w.head = "", b.Commit
	case b.Create || b.Reset:
		sha := b.Commit
		if sha == "" {
			sha = f.heads[fakeMainBranch]
		}
		f.heads[b.Branch] = sha
		w.branch, w.head = b.Branch, sha
	default:
		w.branch, w.head = b.Branch, f.heads[b.Branch]
	}
}

func (f *fakeGit) Create(_ context.Context, dir string, b provision.BranchSpec, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: fake\n"), 0o644); err != nil {
		return err
	}
	if err := f.restore(dir); err != nil {
		return err
	}
	w := &fakeWT{path: dir, locked: true, reason: reason}
	f.apply(w, b)
	f.wts = append(f.wts, w)
	return nil
}

func (f *fakeGit) Switch(_ context.Context, dir string, b provision.BranchSpec, runHook bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switches = append(f.switches, runHook)
	w := f.wt(dir)
	if w == nil {
		return &gitrun.GitError{Args: []string{"switch"}, Code: 128, Stderr: "fatal: not a git repository"}
	}
	f.apply(w, b)
	return nil
}

func (f *fakeGit) Clean(_ context.Context, dir string, keep []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := map[string]bool{".git": true}
	for _, k := range keep {
		kept[k] = true
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !kept[e.Name()] {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return f.restore(dir)
}

// statusTimedOut counts a status call and reports the timeout error when the
// test injected one.
func (f *fakeGit) statusTimedOut() error {
	f.statusCalls.Add(1)
	if f.timeoutStatus.Load() {
		return fmt.Errorf("git status --porcelain=v2 -z: %w after 1m0s", gitrun.ErrTimedOut)
	}
	return nil
}

func (f *fakeGit) Unsaved(_ context.Context, dir string) (provision.Unsaved, error) {
	if err := f.statusTimedOut(); err != nil {
		return provision.Unsaved{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return provision.Unsaved{Files: f.unsavedFiles(dir), Unreachable: f.orphans[realPath(dir)]}, nil
}

// Preserve records the work as a branch and leaves the worktree clean, as the
// commit on the new branch does.
func (f *fakeGit) Preserve(_ context.Context, dir, branch, _ string) error {
	if err := f.statusTimedOut(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[branch] = f.newSHA()
	f.preserved[branch] = f.unsavedFiles(dir)
	for _, n := range f.unsavedFiles(dir) {
		if _, tracked := f.tracked[n]; tracked {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, n)); err != nil {
			return err
		}
	}
	return f.restore(dir)
}

func (f *fakeGit) Unlock(_ context.Context, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.wt(dir)
	if w == nil || !w.locked {
		return &gitrun.GitError{Args: []string{"worktree", "unlock"}, Code: 128, Stderr: "fatal: '" + dir + "' is not locked"}
	}
	w.locked = false
	return nil
}

func (f *fakeGit) Remove(_ context.Context, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.wt(dir)
	if w != nil && w.locked {
		return &gitrun.GitError{Args: []string{"worktree", "remove"}, Code: 128, Stderr: "fatal: cannot remove a locked working tree"}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for i, x := range f.wts {
		if x == w {
			f.wts = append(f.wts[:i], f.wts[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeGit) Prune(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.wts[:0]
	for _, w := range f.wts {
		if _, err := os.Lstat(w.path); err == nil {
			kept = append(kept, w)
		}
	}
	f.wts = kept
	return nil
}

func (f *fakeGit) StopFSMonitor(context.Context, string) error { return nil }

func (f *fakeGit) RemoveStaleIndexLock(string, time.Duration) (bool, error) { return false, nil }

var _ provision.Provisioner = (*fakeGit)(nil)

// newFakeRepo is one fake repository under a fresh temporary directory: its
// main checkout holds only the .git directory.
func newFakeRepo(t *testing.T) *fakeGit {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "repo")
	common := filepath.Join(main, ".git")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeGit{
		main: main, common: common, heads: map[string]string{}, tracked: map[string]string{},
		ignored: map[string]bool{}, orphans: map[string][]string{}, preserved: map[string][]string{},
	}
	f.heads[fakeMainBranch] = f.newSHA()
	return f
}

// newFakeEnv is newEnv over fakeGit: no git process runs and no repository is
// built. Use it for tests that assert state transitions, ordering and
// refusals; e.git, e.commitFile and e.repo are unavailable (use e.fake).
func newFakeEnv(t *testing.T, tune ...func(*config.RepoConfig)) *env {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newFakeRepo(t)
	e := &env{t: t, mainDir: f.main, fake: f, g: gitrun.Git{Run: f.run}, now: epoch}
	e.m = e.manager(tune...)
	return e
}
