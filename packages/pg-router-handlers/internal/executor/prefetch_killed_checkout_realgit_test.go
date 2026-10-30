package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// Real-git coverage for bead pg2-lpctwk: a review checkout killed at the
// pre-fetch timeout used to leave <gitdir>/index.lock and a half-updated tree
// that every later dispatch reused. The scratch repo is a throwaway temp dir;
// nothing here touches a live ledger, daemon or worktree.

type killedCheckoutFixture struct {
	ctx    context.Context
	repo   *gitfixture.Repo
	wt     string
	gitDir string
	oldSHA string
	prSHA  string
}

func newKilledCheckoutFixture(t *testing.T) *killedCheckoutFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "prefetch-killed"})
	oldSHA, err := repo.Commit(ctx, "base", map[string]string{"a.txt": "one\n"})
	if err != nil {
		t.Fatal(err)
	}
	prSHA, err := repo.Commit(ctx, "pr", map[string]string{"a.txt": "one\ntwo\n", ".gitattributes": "a.txt filter=slow\n"})
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := repo.Client.Run(ctx, "worktree", "add", "--detach", wt, oldSHA); err != nil {
		t.Fatal(err)
	}
	f := &killedCheckoutFixture{ctx: ctx, repo: repo, wt: wt, oldSHA: oldSHA, prSHA: prSHA}
	r := &ccpoolRun{deps: Deps{Cfg: config.Config{}}}
	f.gitDir = r.worktreeGitDir(ctx, wt)
	if f.gitDir == "" {
		t.Fatal("cannot locate the worktree gitdir")
	}
	return f
}

func (f *killedCheckoutFixture) setSmudge(t *testing.T, cmd string) {
	t.Helper()
	for _, kv := range [][]string{{"filter.slow.smudge", cmd}, {"filter.slow.clean", "cat"}} {
		if _, err := f.repo.Client.Run(f.ctx, "config", kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *killedCheckoutFixture) prefetch(r *ccpoolRun, timeout string) (ok bool) {
	pf := roles.PrefetchConfig{Rev: testRev, Timeout: timeout}
	d := DispatchContext{Role: prefetchRole(pf), Item: prefetchItem(f.prSHA)}
	return r.prefetch(f.ctx, pf, d, f.wt).OK
}

func (f *killedCheckoutFixture) wantRecovered(t *testing.T, r *ccpoolRun) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.gitDir, "index.lock")); err == nil {
		t.Error("index.lock still present after recovery")
	}
	if _, err := os.Stat(filepath.Join(f.gitDir, prefetchCheckoutMarker)); err == nil {
		t.Error("checkout marker still present after a successful checkout")
	}
	if head, err := r.deps.gitRunOut(f.ctx, f.wt, "rev-parse", "HEAD"); err != nil || head != f.prSHA {
		t.Errorf("HEAD = %q, %v; want %s", head, err, f.prSHA)
	}
	if st, _ := r.deps.gitRunOut(f.ctx, f.wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree not clean after recovery: %q", st)
	}
}

// A checkout that outlives the pre-fetch timeout is cancelled with SIGTERM,
// so real git releases its own index.lock; the next pre-fetch succeeds.
func TestPrefetchRealGit_timedOutCheckoutReleasesIndexLock(t *testing.T) {
	f := newKilledCheckoutFixture(t)
	f.setSmudge(t, "sleep 3; cat")
	r := &ccpoolRun{deps: Deps{Cfg: config.Config{}}}

	start := time.Now()
	if f.prefetch(r, "500ms") {
		t.Fatal("pre-fetch succeeded; the slow smudge filter should have outlived the timeout")
	}
	if took := time.Since(start); took > 12*time.Second {
		t.Errorf("cancelled pre-fetch took %v; want it to stop shortly after the timeout", took)
	}
	if _, err := os.Stat(filepath.Join(f.gitDir, "index.lock")); err == nil {
		t.Fatal("index.lock left behind by the timed-out checkout")
	}

	f.setSmudge(t, "cat")
	if !f.prefetch(r, "") {
		t.Fatal("pre-fetch after the timeout failed")
	}
	f.wantRecovered(t, r)
}

// SIGKILL simulation: the checkout dies leaving a stale index.lock and a dirty
// tree. The next dispatch recovers the worktree and the checkout succeeds.
func TestPrefetchRealGit_killedCheckoutIsRecoveredOnNextDispatch(t *testing.T) {
	f := newKilledCheckoutFixture(t)
	f.setSmudge(t, "cat")
	killed := func(ctx context.Context, dir string, stdout io.Writer, args ...string) error {
		if len(args) > 0 && args[0] == "checkout" {
			_ = os.WriteFile(filepath.Join(f.gitDir, "index.lock"), nil, 0o644)
			_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("half-written\n"), 0o644)
			return errors.New("signal: killed")
		}
		return osGitRun(ctx, dir, stdout, args...)
	}
	r := &ccpoolRun{deps: Deps{Cfg: config.Config{}, GitRun: killed}}
	if f.prefetch(r, "") {
		t.Fatal("simulated killed checkout reported OK")
	}
	if _, err := os.Stat(filepath.Join(f.gitDir, "index.lock")); err != nil {
		t.Fatalf("fixture bug: no stale lock: %v", err)
	}

	r = &ccpoolRun{deps: Deps{Cfg: config.Config{}}}
	if !f.prefetch(r, "") {
		t.Fatal("redispatch did not recover the poisoned worktree")
	}
	f.wantRecovered(t, r)
}

// Marker alone (no lock: git was killed after releasing it) still forces a
// clean checkout over the half-updated tree.
func TestPrefetchRealGit_markerWithoutLockForcesCleanCheckout(t *testing.T) {
	f := newKilledCheckoutFixture(t)
	f.setSmudge(t, "cat")
	if err := os.WriteFile(filepath.Join(f.gitDir, prefetchCheckoutMarker), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.wt, "a.txt"), []byte("dirty\nlines\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &ccpoolRun{deps: Deps{Cfg: config.Config{}}}
	if !f.prefetch(r, "") {
		t.Fatal("pre-fetch did not recover")
	}
	f.wantRecovered(t, r)
}

// A live session sharing the worktree may own the lock: leave it alone.
func TestPrefetchRealGit_liveSessionKeepsLockAndTree(t *testing.T) {
	f := newKilledCheckoutFixture(t)
	f.setSmudge(t, "cat")
	lock := filepath.Join(f.gitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "peer", CWD: f.wt, Live: true, State: ccpool.StateWorking}}}}
	r := &ccpoolRun{deps: Deps{Cfg: config.Config{}, CC: cc}}
	if f.prefetch(r, "") {
		t.Fatal("pre-fetch succeeded over a live session's index.lock")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("a live session's index.lock was removed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.wt, "a.txt")); strings.Contains(string(b), "two") {
		t.Errorf("worktree was updated despite the live session: %q", b)
	}
}
