// Package provisiontest is the conformance suite every provision.Provisioner
// MUST pass. It is imported only by tests: each implementation's test calls
// Run with a factory for its provisioner.
package provisiontest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

const lockReason = "pg-gwm w-x"

// env is one fixture: a repository with a committed base, plus the hook marker.
type env struct {
	t      *testing.T
	repo   *gitfixture.Repo
	p      provision.Provisioner
	marker string
	base   string
}

func (e *env) path(rel string) string {
	e.t.Helper()
	p, err := e.repo.RootPath(rel)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) hookDir() string { return e.path("hooks") }

func (e *env) write(dir, rel, body string) {
	e.t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// client is a hermetic git client for dir (the main checkout or a linked
// worktree under the fixture root).
func (e *env) client(dir string) *gitclient.Client {
	e.t.Helper()
	return gittest.ClientAt(e.t, e.repo, dir)
}

func (e *env) git(dir string, args ...string) string {
	e.t.Helper()
	out, err := e.client(dir).Run(e.t.Context(), args...)
	if err != nil {
		e.t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// markerRuns counts post-checkout hook runs.
func (e *env) markerRuns() int {
	b, err := os.ReadFile(e.marker)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "ran")
}

func (e *env) hook(name, body string) {
	e.t.Helper()
	p := filepath.Join(e.hookDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // hooks must be executable
		e.t.Fatal(err)
	}
}

func newEnv(t *testing.T, newProv func(*testing.T, *gitfixture.Repo) provision.Provisioner) *env {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "provisiontest", InitialCommit: true})
	e := &env{t: t, repo: repo}
	var err error
	e.base, err = repo.Commit(t.Context(), "base", map[string]string{
		".gitignore": "*.o\n.flox/\n.keepme\n",
		"a.txt":      "one\n",
		"sub/b.txt":  "b\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.marker = e.path("hook.log")
	e.hook("post-checkout", "echo ran >> '"+e.marker+"'\n")
	e.p = newProv(t, repo)
	return e
}

// create makes a locked worktree named name on a new branch and returns its
// directory. The hook has run once for it.
func (e *env) create(name string) string {
	e.t.Helper()
	dir := e.path(name)
	b := provision.BranchSpec{Branch: name, Create: true, Commit: e.base}
	if err := e.p.Create(e.t.Context(), dir, b, lockReason); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

// Run runs the conformance suite against the provisioner newProv builds for
// a fixture repository's main checkout.
func Run(t *testing.T, newProv func(t *testing.T, repo *gitfixture.Repo) provision.Provisioner) {
	cases := []struct {
		name string
		fn   func(*testing.T, *env)
	}{
		{"create", testCreate},
		{"create detached", testCreateDetached},
		{"switch hook policy", testSwitchHookPolicy},
		{"clean", testClean},
		{"unsaved files", testUnsavedFiles},
		{"unsaved unreachable", testUnsavedUnreachable},
		{"unsaved merge", testUnsavedMerge},
		{"unsaved submodule", testUnsavedSubmodule},
		{"preserve", testPreserve},
		{"preserve without identity or with gpgsign and a failing hook", testPreserveHostile},
		{"preserve blocked", testPreserveBlocked},
		{"remove", testRemove},
		{"prune", testPrune},
		{"stop fsmonitor", testStopFSMonitor},
		{"stale index lock", testStaleIndexLock},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newEnv(t, newProv)) })
	}
}

func testCreate(t *testing.T, e *env) {
	dir := e.create("wt1")
	if got := e.git(dir, "branch", "--show-current"); got != "wt1" {
		t.Errorf("branch = %q, want wt1", got)
	}
	if list := e.git(e.repo.Dir, "worktree", "list", "--porcelain"); !strings.Contains(list, "locked "+lockReason) {
		t.Errorf("worktree not locked with %q:\n%s", lockReason, list)
	}
	if n := e.markerRuns(); n != 1 {
		t.Errorf("post-checkout ran %d times, want 1", n)
	}
}

func testCreateDetached(t *testing.T, e *env) {
	dir := e.path("wt1")
	if err := e.p.Create(t.Context(), dir, provision.BranchSpec{Detach: true, Commit: e.base}, lockReason); err != nil {
		t.Fatal(err)
	}
	if got := e.git(dir, "branch", "--show-current"); got != "" {
		t.Errorf("branch = %q, want detached", got)
	}
	if got := e.git(dir, "rev-parse", "HEAD"); got != e.base {
		t.Errorf("HEAD = %s, want %s", got, e.base)
	}
}

func testSwitchHookPolicy(t *testing.T, e *env) {
	dir := e.create("wt1")
	e.git(e.repo.Dir, "branch", "other", e.base)
	repoCfg := e.git(e.repo.Dir, "config", "--local", "--get", "core.hooksPath")
	before := e.markerRuns()

	if err := e.p.Switch(t.Context(), dir, provision.BranchSpec{Branch: "other"}, false); err != nil {
		t.Fatal(err)
	}
	if got := e.markerRuns(); got != before {
		t.Errorf("runHook=false ran the hook (%d -> %d)", before, got)
	}
	if got := e.git(dir, "branch", "--show-current"); got != "other" {
		t.Errorf("branch = %q, want other", got)
	}
	if got := e.git(e.repo.Dir, "config", "--local", "--get", "core.hooksPath"); got != repoCfg {
		t.Errorf("core.hooksPath changed: %q -> %q", repoCfg, got)
	}

	if err := e.p.Switch(t.Context(), dir, provision.BranchSpec{Branch: "wt1"}, true); err != nil {
		t.Fatal(err)
	}
	if got := e.markerRuns(); got != before+1 {
		t.Errorf("runHook=true ran the hook %d times, want 1", got-before)
	}
	if got := e.git(e.repo.Dir, "config", "--local", "--get", "core.hooksPath"); got != repoCfg {
		t.Errorf("core.hooksPath changed after hooked switch: %q -> %q", repoCfg, got)
	}

	if err := e.p.Switch(t.Context(), dir, provision.BranchSpec{Detach: true, Commit: e.base}, false); err != nil {
		t.Fatal(err)
	}
	if got := e.git(dir, "branch", "--show-current"); got != "" {
		t.Errorf("detach left branch %q", got)
	}
	if err := e.p.Switch(t.Context(), dir, provision.BranchSpec{Branch: "made", Create: true, Commit: e.base}, false); err != nil {
		t.Fatal(err)
	}
	if got := e.git(dir, "branch", "--show-current"); got != "made" {
		t.Errorf("create switch branch = %q", got)
	}
}

func testClean(t *testing.T, e *env) {
	dir := e.create("wt1")
	e.write(dir, "a.txt", "dirty\n")
	e.write(dir, "untracked.txt", "u")
	e.write(dir, "x.o", "ignored")
	e.write(dir, "build/y.o", "ignored")
	e.write(dir, ".flox/env.json", "keep")
	if err := e.p.Clean(t.Context(), dir, []string{".flox"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "one\n" {
		t.Errorf("tracked change survived: %q", got)
	}
	for _, gone := range []string{"untracked.txt", "x.o", "build/y.o"} {
		if e.exists(filepath.Join(dir, gone)) {
			t.Errorf("%s survived clean", gone)
		}
	}
	if !e.exists(filepath.Join(dir, ".flox", "env.json")) {
		t.Error("keep path .flox/env.json was removed")
	}
	// Without the keep path the ignored directory goes too.
	if err := e.p.Clean(t.Context(), dir, nil); err != nil {
		t.Fatal(err)
	}
	if e.exists(filepath.Join(dir, ".flox")) {
		t.Error(".flox survived a clean with an empty keep list")
	}
}

func testUnsavedFiles(t *testing.T, e *env) {
	dir := e.create("wt1")
	u, err := e.p.Unsaved(t.Context(), dir)
	if err != nil || u.Any() {
		t.Fatalf("clean tree: %+v, %v", u, err)
	}
	e.write(dir, "x.o", "ignored")
	if u, err = e.p.Unsaved(t.Context(), dir); err != nil || u.Any() {
		t.Fatalf("ignored only: %+v, %v", u, err)
	}
	e.write(dir, "a.txt", "dirty\n")
	e.write(dir, "new.txt", "n")
	u, err = e.p.Unsaved(t.Context(), dir)
	if err != nil || !u.Any() {
		t.Fatalf("dirty tree: %+v, %v", u, err)
	}
	if strings.Join(u.Files, ",") != "a.txt,new.txt" {
		t.Errorf("Files = %v, want a.txt and new.txt", u.Files)
	}
	if u.Blocker != "" || len(u.Unreachable) != 0 {
		t.Errorf("unexpected blocker or unreachable: %+v", u)
	}
}

func testUnsavedUnreachable(t *testing.T, e *env) {
	dir := e.path("wt1")
	if err := e.p.Create(t.Context(), dir, provision.BranchSpec{Detach: true, Commit: e.base}, lockReason); err != nil {
		t.Fatal(err)
	}
	e.git(dir, "commit", "--allow-empty", "-m", "orphan")
	head := e.git(dir, "rev-parse", "HEAD")
	u, err := e.p.Unsaved(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Any() || len(u.Unreachable) != 1 || u.Unreachable[0] != head {
		t.Errorf("Unreachable = %v, want [%s]", u.Unreachable, head)
	}
	if len(u.Files) != 0 {
		t.Errorf("Files = %v, want none", u.Files)
	}
}

// conflict leaves dir on branch name with a merge in progress.
func (e *env) conflict(dir string) {
	e.t.Helper()
	ctx := e.t.Context()
	e.git(dir, "switch", "-c", "feat")
	if _, err := e.repo.Commit(ctx, "main side", map[string]string{"a.txt": "main\n"}); err != nil {
		e.t.Fatal(err)
	}
	e.write(dir, "a.txt", "feat\n")
	e.git(dir, "commit", "-am", "feat side")
	// The merge conflicts, so git exits non-zero by design.
	_, _ = e.client(dir).Run(ctx, "merge", "main")
}

func testUnsavedMerge(t *testing.T, e *env) {
	dir := e.create("wt1")
	e.conflict(dir)
	u, err := e.p.Unsaved(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.Blocker == "" || !u.Any() {
		t.Errorf("merge in progress: %+v", u)
	}
}

// submoduleChange leaves a modified file inside an initialized submodule.
func (e *env) submoduleChange(dir string) {
	e.t.Helper()
	ctx := e.t.Context()
	src := gittest.NewSibling(e.t, e.repo, "subsrc", gitfixture.RepoOptions{InitialCommit: true})
	if _, err := src.Commit(ctx, "s", map[string]string{"s.txt": "s\n"}); err != nil {
		e.t.Fatal(err)
	}
	e.git(e.repo.Dir, "-c", "protocol.file.allow=always", "submodule", "add", src.Dir, "mod")
	e.git(e.repo.Dir, "commit", "-m", "add submodule")
	e.git(dir, "merge", "--ff-only", "main")
	e.git(dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	e.write(filepath.Join(dir, "mod"), "s.txt", "changed\n")
}

func testUnsavedSubmodule(t *testing.T, e *env) {
	dir := e.create("wt1")
	e.submoduleChange(dir)
	u, err := e.p.Unsaved(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.Blocker == "" || !strings.Contains(u.Blocker, "submodule") {
		t.Errorf("submodule change: %+v", u)
	}
}

func testPreserve(t *testing.T, e *env) {
	dir := e.create("wt1")
	old := e.git(dir, "rev-parse", "HEAD")
	e.write(dir, "a.txt", "dirty\n")
	e.write(dir, "new.txt", "n")
	e.write(dir, "x.o", "ignored")
	if err := e.p.Preserve(t.Context(), dir, "pg-gwm/kept", "body text"); err != nil {
		t.Fatal(err)
	}
	if got := e.git(dir, "branch", "--show-current"); got != "pg-gwm/kept" {
		t.Errorf("branch = %q", got)
	}
	if got := e.git(e.repo.Dir, "rev-parse", "wt1"); got != old {
		t.Errorf("lease branch moved: %s, want %s", got, old)
	}
	if got := e.git(e.repo.Dir, "rev-parse", "pg-gwm/kept~1"); got != old {
		t.Errorf("preserved parent = %s, want %s", got, old)
	}
	if got := e.git(e.repo.Dir, "log", "-1", "--format=%s", "pg-gwm/kept"); got != "adding dirty WIP" {
		t.Errorf("subject = %q", got)
	}
	if got := e.git(e.repo.Dir, "log", "-1", "--format=%b", "pg-gwm/kept"); got != "body text" {
		t.Errorf("body = %q", got)
	}
	files := e.git(e.repo.Dir, "show", "--name-only", "--format=", "pg-gwm/kept")
	if files != "a.txt\nnew.txt" {
		t.Errorf("committed files = %q, want a.txt and new.txt only", files)
	}
}

func testPreserveHostile(t *testing.T, e *env) {
	dir := e.create("wt1")
	for _, k := range []string{"user.name", "user.email"} {
		if _, err := e.repo.Client.Run(t.Context(), "config", "--local", "--unset", k); err != nil {
			t.Fatal(err)
		}
	}
	e.git(e.repo.Dir, "config", "--local", "commit.gpgsign", "true")
	e.hook("pre-commit", "exit 1\n")
	e.write(dir, "a.txt", "dirty\n")
	if err := e.p.Preserve(t.Context(), dir, "pg-gwm/kept", "body"); err != nil {
		t.Fatalf("preserve with no identity, gpgsign and a failing hook: %v", err)
	}
	if got := e.git(e.repo.Dir, "log", "-1", "--format=%s", "pg-gwm/kept"); got != "adding dirty WIP" {
		t.Errorf("subject = %q", got)
	}
}

func testPreserveBlocked(t *testing.T, e *env) {
	dir := e.create("wt1")
	e.conflict(dir)
	err := e.p.Preserve(t.Context(), dir, "pg-gwm/kept", "body")
	if err == nil || !errors.Is(err, provision.ErrPreserveBlocked) {
		t.Fatalf("Preserve during a merge = %v, want ErrPreserveBlocked", err)
	}
	if out := e.git(e.repo.Dir, "branch", "--list", "pg-gwm/kept"); out != "" {
		t.Errorf("blocked preserve created a branch: %q", out)
	}
}

func testRemove(t *testing.T, e *env) {
	dir := e.create("wt1")
	if err := e.p.Remove(t.Context(), dir); err == nil {
		t.Fatal("Remove of a locked worktree succeeded")
	}
	if !e.exists(dir) {
		t.Fatal("refused Remove deleted the directory")
	}
	if err := e.p.Unlock(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if err := e.p.Remove(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if e.exists(dir) {
		t.Error("directory survived Remove")
	}
	if got := e.git(e.repo.Dir, "rev-parse", "--verify", "wt1"); got == "" {
		t.Error("branch not kept")
	}
}

func testPrune(t *testing.T, e *env) {
	dir := e.create("wt1")
	if err := e.p.Unlock(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.p.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if list := e.git(e.repo.Dir, "worktree", "list", "--porcelain"); strings.Contains(list, "wt1") {
		t.Errorf("worktree still registered after prune:\n%s", list)
	}
}

func testStopFSMonitor(t *testing.T, e *env) {
	dir := e.create("wt1")
	// No daemon runs in a fresh worktree: "not running" is success.
	if err := e.p.StopFSMonitor(t.Context(), dir); err != nil {
		t.Fatalf("StopFSMonitor with no daemon: %v", err)
	}
}

func testStaleIndexLock(t *testing.T, e *env) {
	dir := e.create("wt1")
	lock := filepath.Join(e.git(dir, "rev-parse", "--absolute-git-dir"), "index.lock")
	old := time.Now().Add(-time.Hour)
	set := func(body string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(lock, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(lock, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	if removed, err := e.p.RemoveStaleIndexLock(dir, 10*time.Minute); err != nil || removed {
		t.Fatalf("no lock: removed=%v err=%v", removed, err)
	}
	set("", time.Now())
	if removed, err := e.p.RemoveStaleIndexLock(dir, 10*time.Minute); err != nil || removed || !e.exists(lock) {
		t.Fatalf("young lock: removed=%v err=%v", removed, err)
	}
	set("pid", old)
	if removed, err := e.p.RemoveStaleIndexLock(dir, 10*time.Minute); err != nil || removed || !e.exists(lock) {
		t.Fatalf("non-empty lock: removed=%v err=%v", removed, err)
	}
	set("", old)
	if removed, err := e.p.RemoveStaleIndexLock(dir, 10*time.Minute); err != nil || !removed || e.exists(lock) {
		t.Fatalf("stale zero-byte lock: removed=%v err=%v", removed, err)
	}
}
