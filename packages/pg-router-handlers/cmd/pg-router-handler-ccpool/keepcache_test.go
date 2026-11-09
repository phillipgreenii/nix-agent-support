package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
)

// Worktree keep cache, examined cap and sweep deadline (bead pg2-fx5x0l.2,
// INV-CCH-19), against REAL git through the sweepHarness fixture.

// countOpens wraps the harness opener and counts opens of a directory other than
// the canonical repo: one per worktree examined with git (the repo root is opened
// only after the status guard passed).
func (h *sweepHarness) countOpens() *atomic.Int64 {
	var n atomic.Int64
	inner := h.env.open
	h.env.open = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		if dir != h.repo {
			n.Add(1)
		}
		return inner(ctx, dir)
	}
	return &n
}

func (h *sweepHarness) makeDirty(path string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("work"), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *sweepHarness) keepFiles() []string {
	h.t.Helper()
	m, err := filepath.Glob(filepath.Join(h.lockDir, keepFilePrefix+"*"+keepFileSuffix))
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func TestKeepCache_dirtyKeptOnce_skippedInsideTTL_reexaminedAfter(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.makeDirty(path)
	opens := h.countOpens()

	if got := h.sweep(); got != 0 {
		t.Fatalf("pass 1 removed %d, want 0 (dirty)", got)
	}
	if opens.Load() != 1 {
		t.Fatalf("pass 1 examined the worktree %d times, want 1", opens.Load())
	}
	if len(h.keepFiles()) != 1 {
		t.Fatalf("pass 1 must leave one keep record, got %v", h.keepFiles())
	}

	h.nowFixed = h.nowFixed.Add(h.deps.Cfg.WorktreeKeepRecheck - time.Second)
	if got := h.sweep(); got != 0 || opens.Load() != 1 {
		t.Fatalf("inside the TTL: removed=%d examinations=%d, want 0 and still 1", got, opens.Load())
	}
	h.wantKept(path, "zr-k")

	h.nowFixed = h.nowFixed.Add(2 * time.Second) // now past the TTL
	if got := h.sweep(); got != 0 || opens.Load() != 2 {
		t.Fatalf("after the TTL: removed=%d examinations=%d, want 0 and 2", got, opens.Load())
	}
	h.wantKept(path, "zr-k")
}

func TestKeepCache_cleanedWorktreeAfterTTLIsReclaimed(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.makeDirty(path)
	if got := h.sweep(); got != 0 {
		t.Fatalf("removed %d, want 0", got)
	}
	if err := os.Remove(filepath.Join(path, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	h.nowFixed = h.nowFixed.Add(h.deps.Cfg.WorktreeKeepRecheck + time.Second)
	if got := h.sweep(); got != 1 || h.exists(path) {
		t.Fatalf("removed=%d exists=%v, want the now-clean worktree reclaimed after the TTL", got, h.exists(path))
	}
}

func TestKeepCache_invalidatedWhenHEADChanges(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.makeDirty(path)
	// Pin the branch ref's mtime in the past so a later commit certainly differs
	// (file timestamps can be coarse).
	ref := filepath.Join(h.repo, ".git", "refs", "heads", "pg-router", "zr-k")
	past := h.nowFixed.Add(-3 * time.Hour)
	if err := os.Chtimes(ref, past, past); err != nil {
		t.Fatal(err)
	}
	opens := h.countOpens()
	h.sweep()
	h.sweep()
	if opens.Load() != 1 {
		t.Fatalf("the unchanged worktree was examined %d times inside the TTL, want 1", opens.Load())
	}

	h.git(path, "commit", "-q", "--allow-empty", "-m", "worker commit")
	if got := h.sweep(); got != 0 {
		t.Fatalf("removed %d, want 0", got)
	}
	if opens.Load() != 2 {
		t.Fatalf("a worktree that gained a commit must be re-examined at once inside the TTL; examinations=%d, want 2", opens.Load())
	}
	h.wantKept(path, "zr-k")
}

func TestKeepCache_invalidatedWhenIndexChanges(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.makeDirty(path)
	opens := h.countOpens()
	h.sweep()
	h.sweep()
	if opens.Load() != 1 {
		t.Fatalf("examinations=%d, want 1", opens.Load())
	}
	admin, ok := worktreeAdminDir(path)
	if !ok {
		t.Fatal("no admin dir")
	}
	later := h.nowFixed.Add(time.Hour)
	if err := os.Chtimes(filepath.Join(admin, "HEAD"), later, later); err != nil {
		t.Fatal(err)
	}
	h.sweep()
	if opens.Load() != 2 {
		t.Fatalf("a changed HEAD file must force a re-examination; examinations=%d, want 2", opens.Load())
	}
}

func TestKeepCache_disabledWhenRecheckIsZero(t *testing.T) {
	h := newSweepHarness(t)
	h.deps.Cfg.WorktreeKeepRecheck = 0
	path := h.leak("zr-k")
	h.makeDirty(path)
	opens := h.countOpens()
	h.sweep()
	h.sweep()
	if opens.Load() != 2 || len(h.keepFiles()) != 0 {
		t.Fatalf("with the cache disabled: examinations=%d records=%v, want 2 and none", opens.Load(), h.keepFiles())
	}
}

func TestKeepCache_tornFileIsTreatedAsAbsent(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.makeDirty(path)
	torn := keepFile(h.lockDir, "zr-k")
	if err := os.WriteFile(torn, []byte(`{"path":"`+path+`","sig":"1|2`), 0o644); err != nil {
		t.Fatal(err)
	}
	opens := h.countOpens()
	if got := h.sweep(); got != 0 || opens.Load() != 1 {
		t.Fatalf("torn record: removed=%d examinations=%d, want 0 and 1 (examined as if absent)", got, opens.Load())
	}
	if e, ok := readKeep(torn); !ok || e.Path != path {
		t.Errorf("the torn record must be replaced by a whole one, got %+v ok=%v", e, ok)
	}
	h.sweep()
	if opens.Load() != 1 {
		t.Errorf("the rewritten record must now be honored; examinations=%d, want 1", opens.Load())
	}
}

func TestKeepCache_unpushedCommitsAreCachedToo(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.git(path, "commit", "-q", "--allow-empty", "-m", "worker commit")
	opens := h.countOpens()
	h.sweep()
	h.sweep()
	if opens.Load() != 1 || len(h.keepFiles()) != 1 {
		t.Fatalf("examinations=%d records=%d, want 1 and 1", opens.Load(), len(h.keepFiles()))
	}
}

func TestKeepCache_gcRemovesExpiredTornAndOrphanedRecords(t *testing.T) {
	h := newSweepHarness(t)
	now := h.nowFixed
	live := filepath.Join(h.wtDir, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(bead string, e keepEntry) string {
		t.Helper()
		keepRemember(h.lockDir, leakedWorktree{bead: bead, path: e.Path}, e.Sig, time.Unix(0, e.Until).Add(-time.Minute), time.Minute)
		return keepFile(h.lockDir, bead)
	}
	fresh := write("fresh", keepEntry{Path: live, Sig: "s", Until: now.Add(time.Minute).UnixNano()})
	expired := write("expired", keepEntry{Path: live, Sig: "s", Until: now.Add(-time.Minute).UnixNano()})
	gone := write("gone", keepEntry{Path: filepath.Join(h.wtDir, "nope"), Sig: "s", Until: now.Add(time.Minute).UnixNano()})
	torn := keepFile(h.lockDir, "torn")
	if err := os.WriteFile(torn, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTmp := filepath.Join(h.lockDir, keepFilePrefix+"x"+keepFileSuffix+keepTmpMarker+"1")
	if err := os.WriteFile(oldTmp, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(oldTmp, old, old); err != nil {
		t.Fatal(err)
	}
	youngTmp := filepath.Join(h.lockDir, keepFilePrefix+"y"+keepFileSuffix+keepTmpMarker+"1")
	if err := os.WriteFile(youngTmp, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(youngTmp, now, now); err != nil {
		t.Fatal(err)
	}

	if n := sweepStaleLocks(withLockDir(h.deps, h.lockDir)); n != 4 {
		t.Fatalf("sweepStaleLocks removed %d, want 4 (expired, gone, torn, old temp)", n)
	}
	for _, p := range []string{expired, gone, torn, oldTmp} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed (err=%v)", p, err)
		}
	}
	for _, p := range []string{fresh, youngTmp} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s should have been kept: %v", p, err)
		}
	}
}

func TestSweep_examinedCapBoundsGitWorkPerPass(t *testing.T) {
	h := newSweepHarness(t)
	total := worktreeSweepMaxExamined + 2
	for i := 0; i < total; i++ {
		h.makeDirty(h.leak(fmt.Sprintf("zr-%02d", i)))
	}
	opens := h.countOpens()
	if got := h.sweep(); got != 0 {
		t.Fatalf("removed %d, want 0 (all dirty)", got)
	}
	if opens.Load() != worktreeSweepMaxExamined {
		t.Fatalf("pass 1 examined %d candidates, want the cap %d", opens.Load(), worktreeSweepMaxExamined)
	}
	// The kept ones are cached, so they do not consume the cap again: pass 2
	// reaches the remaining two.
	opens.Store(0)
	h.sweep()
	if opens.Load() != 2 {
		t.Fatalf("pass 2 examined %d candidates, want the remaining 2", opens.Load())
	}
}

// The deadline's expiry is driven by cancelling the context at a known point
// rather than by wall-clock time, so a loaded host cannot change the outcome.
// The wiring of Cfg.SweepDeadline itself is covered by the already-expired test
// below.
func TestSweep_deadlineExpiryReturnsPartialResultWithoutError(t *testing.T) {
	h := newSweepHarness(t)
	h.deps.Cfg.SweepDeadline = time.Hour
	p1 := h.leak("zr-1")
	p2 := h.leak("zr-2")
	inner := h.env.open
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	h.env.open = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		// Calls 1-2 open zr-1 and the repo root, 3 is zr-1's anchor-branch delete:
		// the deadline passes there, so zr-2 must not be examined.
		if calls.Add(1) == 3 {
			cancel()
		}
		return inner(context.Background(), dir)
	}
	if got := sweepLeakedWorktrees(ctx, h.role, h.deps, h.env); got != 1 {
		t.Fatalf("removed %d, want 1 (the first worktree, before the deadline)", got)
	}
	if h.exists(p1) || !h.exists(p2) {
		t.Errorf("zr-1 exists=%v zr-2 exists=%v, want zr-1 reclaimed and zr-2 left for a later pass", h.exists(p1), h.exists(p2))
	}
}

func TestSweep_alreadyExpiredDeadlineDoesNothing(t *testing.T) {
	h := newSweepHarness(t)
	h.deps.Cfg.SweepDeadline = time.Nanosecond
	path := h.leak("zr-k")
	if got := h.sweep(); got != 0 {
		t.Fatalf("removed %d, want 0 under an expired deadline", got)
	}
	h.wantKept(path, "zr-k")
}

func withLockDir(d executor.Deps, dir string) executor.Deps {
	d.LockDir = dir
	return d
}

// Design D5: kept candidates that sort first by name, more than the examination
// cap, must not starve a later removable one. Real git over a throwaway repo;
// the keep cache is off so every kept worktree costs the cap again each run.
func TestSweep_oldestExaminedFirstReachesLaterRemovable(t *testing.T) {
	h := newSweepHarness(t)
	h.deps.Cfg.WorktreeKeepRecheck = 0
	total := worktreeSweepMaxExamined + 2
	for i := 0; i < total; i++ {
		h.makeDirty(h.leak(fmt.Sprintf("a-%02d", i)))
	}
	clean := h.leak("z-removable")

	const bound = 3
	for run := 1; run <= bound && h.exists(clean); run++ {
		h.sweep()
	}
	if h.exists(clean) {
		t.Fatalf("z-removable still present after %d runs; kept candidates starved it", bound)
	}
	if got := len(h.examFiles()); got != total {
		t.Errorf("last-examined records = %d, want one per kept worktree (%d)", got, total)
	}
}

func (h *sweepHarness) examFiles() []string {
	m, _ := filepath.Glob(filepath.Join(h.lockDir, examFilePrefix+"*"+examFileSuffix))
	return m
}

// Worktrees whose checked-out branch is not the anchor branch are kept, and that
// decision is cached like a dirty keep. With more of them than one run may
// examine, every candidate is examined within two runs and the pass reports none
// unexamined from then on, so a long-running janitor reaches steady state
// instead of re-paying for the same kept worktrees each run. Real git.
func TestKeepCache_otherBranchKeepIsCachedSoEveryCandidateIsReached(t *testing.T) {
	h := newSweepHarness(t)
	h.env.cwds = func(context.Context) ([]string, error) { return nil, nil }
	total := worktreeSweepMaxExamined*2 + 3
	paths := make([]string, 0, total)
	for i := 0; i < total; i++ {
		p := h.leak(fmt.Sprintf("zr-%02d", i))
		h.git(p, "checkout", "-q", "-b", fmt.Sprintf("feature-%02d", i))
		paths = append(paths, p)
	}

	const bound = 4
	var c *passCounts
	for run := 1; run <= bound; run++ {
		_, c = h.sweepCounted()
		if c.Unexamined == 0 {
			break
		}
	}
	if c.Unexamined != 0 {
		t.Fatalf("still %d unexamined after %d runs: %+v", c.Unexamined, bound, c)
	}
	// Steady state: nothing is examined with git again while the keeps are valid.
	opens := h.countOpens()
	_, c = h.sweepCounted()
	if opens.Load() != 0 || c.Examined != 0 || c.Cached != total || c.Unexamined != 0 {
		t.Fatalf("steady-state run opened %d worktrees, counters %+v; want 0 opens, all %d cached", opens.Load(), c, total)
	}
	for i, p := range paths {
		h.wantKept(p, fmt.Sprintf("zr-%02d", i))
	}
}
