package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

// recorder is a Removal whose pauses are recorded, never slept, and whose
// fsmonitor stop is recorded, never run.
type recorder struct {
	pauses  []time.Duration
	stopped []string
	failAt  int // the Nth Sleep (1-based) returns an error, as a cancelled ctx would
}

func (r *recorder) removal(batch int, pause time.Duration) Removal {
	return Removal{
		BatchSize: batch,
		Pause:     pause,
		Sleep: func(_ context.Context, d time.Duration) error {
			r.pauses = append(r.pauses, d)
			if r.failAt > 0 && len(r.pauses) == r.failAt {
				return context.Canceled
			}
			return nil
		},
		StopFsmonitor: func(_ context.Context, p string) { r.stopped = append(r.stopped, p) },
	}
}

// makeTree builds a throwaway tree of dirs directories (the root excluded)
// each holding files regular files, plus a symlink pointing outside the tree.
// It returns the root and the number of unlink+rmdir calls a drain must issue.
func makeTree(t *testing.T, root string, dirs, files int) int {
	t.Helper()
	ops := 1 // the root rmdir
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, fmt.Sprintf("d%d", d), "sub")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ops += 2 // d<N> and sub
		for f := 0; f < files; f++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", f)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			ops++
		}
	}
	return ops
}

func TestDrainTree_batchesAndPausesAtEveryBatchBoundary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	ops := makeTree(t, root, 10, 24) // 1 + 10*2 + 240 = 261
	var rec recorder
	st, err := rec.removal(50, 250*time.Millisecond).DrainTree(t.Context(), root)
	if err != nil || !st.Done {
		t.Fatalf("drain = %+v, %v; want done", st, err)
	}
	if st.Ops != ops {
		t.Errorf("ops = %d, want %d (one unlink or rmdir per path, nothing skipped)", st.Ops, ops)
	}
	if want := ops / 50; st.Batches != want || len(rec.pauses) != want {
		t.Errorf("batches = %d, pauses = %d, want %d", st.Batches, len(rec.pauses), want)
	}
	for _, p := range rec.pauses {
		if p != 250*time.Millisecond {
			t.Errorf("pause = %v, want 250ms", p)
		}
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root still exists: %v", err)
	}
}

func TestDrainTree_neverFollowsASymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "precious")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "tree")
	makeTree(t, root, 1, 1)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	var rec recorder
	if st, err := rec.removal(1000, 0).DrainTree(t.Context(), root); err != nil || !st.Done {
		t.Fatalf("drain = %+v, %v", st, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a file behind a symlink was deleted: %v", err)
	}
}

// An interrupted drain (the pause is cancelled, as a shutdown does) stops
// without error and leaves a non-empty remainder that a later pass finishes.
func TestDrainTree_interruptedThenResumedFinishes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	ops := makeTree(t, root, 8, 20)
	rec := recorder{failAt: 2}
	st, err := rec.removal(40, time.Second).DrainTree(t.Context(), root)
	if err != nil || st.Done {
		t.Fatalf("interrupted drain = %+v, %v; want no error and not done", st, err)
	}
	if st.Ops != 80 {
		t.Errorf("ops before the interrupt = %d, want 80 (two full batches)", st.Ops)
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("the remainder must stay for the next pass: %v", err)
	}
	var rec2 recorder
	st2, err := rec2.removal(40, time.Second).DrainTree(t.Context(), root)
	if err != nil || !st2.Done {
		t.Fatalf("resume = %+v, %v", st2, err)
	}
	if st.Ops+st2.Ops < ops-1 || st.Ops+st2.Ops > ops+8 {
		t.Errorf("total ops %d vs tree %d: the resume must only finish the remainder", st.Ops+st2.Ops, ops)
	}
}

func TestDrainTree_cancelledContextDeletesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	makeTree(t, root, 2, 2)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var rec recorder
	st, err := rec.removal(10, 0).DrainTree(ctx, root)
	if err != nil || st.Done || st.Ops != 0 {
		t.Errorf("drain = %+v, %v; want nothing done", st, err)
	}
}

func TestDrainTree_concurrentlyVanishedEntriesAreFine(t *testing.T) {
	var rec recorder
	st, err := rec.removal(10, 0).DrainTree(t.Context(), filepath.Join(t.TempDir(), "gone"))
	if err != nil || !st.Done {
		t.Errorf("drain of a missing path = %+v, %v; want done", st, err)
	}
}

func TestMoveToTrash_isOneRenameBesideTheTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bead-1")
	makeTree(t, src, 1, 1)
	trash, err := MoveToTrash(src, "", time.Unix(0, 42))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, DefaultTrashDirName, "bead-1.42"); trash != want {
		t.Errorf("trash = %q, want %q", trash, want)
	}
	if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("source path still exists: %v", err)
	}
}

// ---- real git ----

type realRepo struct {
	repo, wtDir string
	open        Opener
	rec         *recorder
}

func newRealRepo(t *testing.T, batch int) realRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := newCommittedRepo(t)
	home := t.TempDir()
	rec := &recorder{}
	open := ThrottledOpener(
		rec.removal(batch, time.Second),
		gitclient.WithHome(home),
		gitclient.WithoutInherited("SSH_AUTH_SOCK"),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
	)
	return realRepo{repo: repo, wtDir: t.TempDir(), open: open, rec: rec}
}

func (r realRepo) add(t *testing.T, bead string) string {
	t.Helper()
	wm, err := r.open(t.Context(), r.repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.wtDir, bead)
	h, err := wm.CreateWorktree(t.Context(), path, "pg-router/"+bead, gitclient.CreateWorktreeOptions{ResetBranch: true})
	if err == nil && h != nil {
		err = h.Wait()
	}
	if err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	return path
}

func (r realRepo) registered(t *testing.T) string {
	t.Helper()
	out, err := hermeticGit(r.repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func (r realRepo) trashEntries(t *testing.T) []string {
	t.Helper()
	es, _ := os.ReadDir(filepath.Join(r.wtDir, DefaultTrashDirName))
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestRemoveWorktree_realGit_stopsDaemonRenamesPrunesAndDrains(t *testing.T) {
	r := newRealRepo(t, 5)
	wt := r.add(t, "zr-a")
	makeTree(t, filepath.Join(wt, "build"), 3, 4) // untracked output a forced removal discards
	wm, _ := r.open(t.Context(), wt)
	if err := wm.RemoveWorktree(t.Context(), wt, true); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if len(r.rec.stopped) != 1 || r.rec.stopped[0] != wt {
		t.Errorf("fsmonitor stopped for %v, want exactly [%s]", r.rec.stopped, wt)
	}
	if _, err := os.Lstat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree path still exists: %v", err)
	}
	if got := r.trashEntries(t); len(got) != 0 {
		t.Errorf("trash not drained: %v", got)
	}
	if list := r.registered(t); containsPath(list, wt) {
		t.Errorf("git still registers the removed worktree:\n%s", list)
	}
	if len(r.rec.pauses) == 0 {
		t.Error("expected at least one paced pause (tiny batch size)")
	}
	// The branch is free to delete: no registration pins it.
	if out, err := hermeticGit(r.repo, "branch", "-D", "pg-router/zr-a").CombinedOutput(); err != nil {
		t.Errorf("branch -D after removal: %v\n%s", err, out)
	}
}

func containsPath(list, p string) bool {
	resolved, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err == nil {
		p = filepath.Join(resolved, filepath.Base(p))
	}
	for _, cand := range []string{p, "worktree " + p} {
		if len(list) >= len(cand) && indexOf(list, cand) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRemoveWorktree_realGit_nonForceRefusesDirtyTreeAndLeavesItUntouched(t *testing.T) {
	r := newRealRepo(t, 100)
	wt := r.add(t, "zr-b")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	wm, _ := r.open(t.Context(), wt)
	err := wm.RemoveWorktree(t.Context(), wt, false)
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("err = %v, want ErrWorktreeDirty", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "scratch.txt")); err != nil {
		t.Errorf("a refused removal must leave the tree: %v", err)
	}
	if len(r.rec.stopped) != 0 || len(r.trashEntries(t)) != 0 {
		t.Errorf("a refused removal must not stop the daemon (%v) or trash anything (%v)", r.rec.stopped, r.trashEntries(t))
	}
	// forced, the same tree goes.
	if err := wm.RemoveWorktree(t.Context(), wt, true); err != nil {
		t.Fatalf("forced removal: %v", err)
	}
}

func TestRemoveWorktree_realGit_nonForceCleanTreeIsRemoved(t *testing.T) {
	r := newRealRepo(t, 100)
	wt := r.add(t, "zr-c")
	wm, _ := r.open(t.Context(), wt)
	if err := wm.RemoveWorktree(t.Context(), wt, false); err != nil {
		t.Fatalf("clean non-force removal: %v", err)
	}
	if _, err := os.Lstat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree path still exists: %v", err)
	}
}

func TestRemoveWorktree_realGit_refusesLockedAndNonWorktree(t *testing.T) {
	r := newRealRepo(t, 100)
	wt := r.add(t, "zr-d")
	if out, err := hermeticGit(r.repo, "worktree", "lock", wt).CombinedOutput(); err != nil {
		t.Fatalf("lock: %v\n%s", err, out)
	}
	wm, _ := r.open(t.Context(), wt)
	if err := wm.RemoveWorktree(t.Context(), wt, true); !errors.Is(err, ErrWorktreeLocked) {
		t.Errorf("locked: err = %v, want ErrWorktreeLocked", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("a locked worktree must stay: %v", err)
	}
	// The main checkout is not a linked worktree: never moved, never deleted.
	root, _ := r.open(t.Context(), r.repo)
	if err := root.RemoveWorktree(t.Context(), r.repo, true); !errors.Is(err, ErrNotLinkedWorktree) {
		t.Errorf("main checkout: err = %v, want ErrNotLinkedWorktree", err)
	}
	if _, err := os.Stat(filepath.Join(r.repo, ".git")); err != nil {
		t.Errorf("main checkout damaged: %v", err)
	}
}

// A crash between the rename and the delete leaves a trash entry and a pruned
// registration; the removal's outcome is already visible and a later pass
// (the janitor's trash purge, here a direct DrainTree) finishes the delete.
func TestRemoveWorktree_realGit_crashBetweenRenameAndDeleteLeavesDrainableTrash(t *testing.T) {
	r := newRealRepo(t, 1)
	r.rec.failAt = 1 // the first pause is cancelled: the drain stops right after one batch
	wt := r.add(t, "zr-e")
	wm, _ := r.open(t.Context(), wt)
	if err := wm.RemoveWorktree(t.Context(), wt, true); err != nil {
		t.Fatalf("RemoveWorktree: %v (a cut-short drain is not a failure)", err)
	}
	left := r.trashEntries(t)
	if len(left) != 1 {
		t.Fatalf("trash entries = %v, want exactly the half-drained one", left)
	}
	if containsPath(r.registered(t), wt) {
		t.Error("registration must already be pruned")
	}
	var rec recorder
	st, err := rec.removal(1000, 0).DrainTree(t.Context(), filepath.Join(r.wtDir, DefaultTrashDirName, left[0]))
	if err != nil || !st.Done {
		t.Fatalf("finishing the half-drained trash: %+v, %v", st, err)
	}
	if got := r.trashEntries(t); len(got) != 0 {
		t.Errorf("trash after the finishing pass: %v", got)
	}
}

// hermeticGit runs git in dir with every GIT_ variable of the ambient
// environment (a pre-commit hook run exports the repository-redirecting ones)
// removed.
func hermeticGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}

// A drain cut short by its budget is the designed hand-off to the janitor sweep,
// so a normal teardown of a tree bigger than the budget logs at INFO, not WARN.
func TestRemoveTree_budgetCutIsLoggedAtInfoNotWarn(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	root := filepath.Join(t.TempDir(), "wt")
	makeTree(t, root, 3, 4)
	rec := &recorder{failAt: 1} // the first pause ends the drain, as an expired budget does
	if err := rec.removal(2, time.Millisecond).RemoveTree(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "handed to the janitor sweep") {
		t.Errorf("want the INFO hand-off line, got %q", out)
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("a budget cut must not warn, got %q", out)
	}
}
