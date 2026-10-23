package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// Two-phase teardown tests (bead pg2-kqegi, INV-CCH-20). The fakes share one
// callLog so the order ACROSS the ccpool and git seams is asserted, not just
// each seam's own order.

const (
	tpRepoRoot = "/repo/root"
	tpBead     = "zr-w"
	tpID       = "pg-router-worker-zr-w"
)

// tpEnv is one two-phase scenario: a real temp worktree directory so the
// existence checks see real filesystem state, and fakes sharing one log.
type tpEnv struct {
	t       *testing.T
	log     *callLog
	cc      *fakeCC
	open    *fakeWorktreeOpener
	wtDir   string
	cwd     string
	session ccpool.Session
}

func newTPEnv(t *testing.T) *tpEnv {
	t.Helper()
	log := &callLog{}
	wtDir := t.TempDir()
	cwd := filepath.Join(wtDir, tpBead)
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &tpEnv{
		t:     t,
		log:   log,
		cc:    &fakeCC{callLog: log},
		open:  &fakeWorktreeOpener{callLog: log},
		wtDir: wtDir,
		cwd:   cwd,
	}
	e.session = ccpool.Session{
		ExternalID: tpID,
		State:      ccpool.StateIdle,
		CWD:        cwd,
		Meta:       map[string]string{ccpool.MetaKeyBead: tpBead},
	}
	return e
}

func (e *tpEnv) markedRow(mut func(*ccpool.Session)) ccpool.Session {
	s := e.session
	s.Meta = map[string]string{ccpool.MetaKeyBead: tpBead, ccpool.MetaKeyPurgePending: "1"}
	s.CloseReason = "handler"
	s.Live = false
	if mut != nil {
		mut(&s)
	}
	return s
}

// removingWorktree makes RemoveWorktree delete the directory, like real git.
func (e *tpEnv) removingWorktree() {
	e.open.removeHook = func(path string) error { return os.RemoveAll(path) }
}

var closedBead = fakeBR{out: map[string]string{"show " + tpBead + " --json": `{"status":"closed"}`}}

func (e *tpEnv) wantLog(want ...string) {
	e.t.Helper()
	if got := e.log.all(); !reflect.DeepEqual(got, want) {
		e.t.Errorf("call log mismatch\n got: %q\nwant: %q", got, want)
	}
}

func (e *tpEnv) twoPhaseLog() []string {
	return []string{
		"SetMeta:" + tpID + ":pgrouter.purge_pending=1",
		"Close:" + tpID + ":purge=false",
		"RemoveWorktree:" + e.cwd,
		"DeleteBranch:pg-router/" + tpBead,
		"Close:" + tpID + ":purge=true",
	}
}

func (e *tpEnv) attemptLog(n string) string {
	return "SetMeta:" + tpID + ":pgrouter.purge_attempts=" + n
}

// The order is exactly SetMeta -> Close(false) -> RemoveWorktree -> branch
// delete -> Close(true), and the removal runs from the REPO ROOT.
func TestCloseSession_twoPhaseOrder(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	if !closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, false) {
		t.Fatal("closeSession = false, want true (purged)")
	}
	e.wantLog(e.twoPhaseLog()...)
	if len(e.open.Opens) == 0 || e.open.Opens[0] != tpRepoRoot {
		t.Errorf("the worktree must be removed from the repo root; Opens=%v", e.open.Opens)
	}
}

// The shutdown sweep takes the same order.
func TestTeardownAllSessions_twoPhaseOrder(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	e.cc.ListSeq = [][]ccpool.Session{{e.session}}
	n := teardownAllSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir)
	if n != 1 {
		t.Fatalf("teardownAllSessions = %d, want 1", n)
	}
	e.wantLog(e.twoPhaseLog()...)
}

// So does the dispatch-time closed-bead reconcile.
func TestReconcileClosedBeadSessions_twoPhaseOrder(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	e.cc.ListSeq = [][]ccpool.Session{{e.session}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, closedBead, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 1", n)
	}
	e.wantLog(e.twoPhaseLog()...)
}

// none isolation (CWD == RepoRoot), a CWD outside WorktreeDir, and an unset
// WorktreeDir keep today's single-phase purge with no marker.
func TestCloseSession_singlePhaseWhenNotAPerBeadWorktree(t *testing.T) {
	cases := []struct {
		name   string
		cwd    func(e *tpEnv) string
		repo   func(e *tpEnv) string
		wtDir  func(e *tpEnv) string
		reason string
	}{
		{
			"none isolation: CWD is the repo root",
			func(e *tpEnv) string { return e.wtDir },
			func(e *tpEnv) string { return e.wtDir },
			func(e *tpEnv) string { return e.wtDir }, "",
		},
		{
			"CWD outside WorktreeDir (path/workforest isolation)",
			func(*tpEnv) string { return "/somewhere/else/zr-w" },
			func(*tpEnv) string { return tpRepoRoot },
			func(e *tpEnv) string { return e.wtDir }, "",
		},
		{
			"WorktreeDir unset",
			func(e *tpEnv) string { return e.cwd },
			func(*tpEnv) string { return tpRepoRoot },
			func(*tpEnv) string { return "" }, "",
		},
		{
			"CWD empty",
			func(*tpEnv) string { return "" },
			func(*tpEnv) string { return tpRepoRoot },
			func(e *tpEnv) string { return e.wtDir }, "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTPEnv(t)
			s := e.session
			s.CWD = c.cwd(e)
			if !closeSession(context.Background(), e.cc, e.open.Open, c.repo(e), c.wtDir(e), s, false) {
				t.Fatal("closeSession = false, want true")
			}
			if len(e.cc.MetaSets) != 0 {
				t.Errorf("single-phase teardown must write no marker; MetaSets=%v", e.cc.MetaSets)
			}
			if !reflect.DeepEqual(e.cc.ClosedPurge, []bool{true}) {
				t.Errorf("want exactly one purge close; ClosedPurge=%v", e.cc.ClosedPurge)
			}
		})
	}
}

// keepWorktree=true (a peer holds the worktree) is a single-phase purge with no
// marker, even for an eligible per-bead worktree.
func TestCloseSession_keepWorktreeIsSinglePhase(t *testing.T) {
	e := newTPEnv(t)
	if !closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, true) {
		t.Fatal("closeSession = false, want true")
	}
	e.wantLog("Close:" + tpID + ":purge=true")
	if len(e.open.Removed) != 0 {
		t.Errorf("a kept worktree must not be removed; Removed=%v", e.open.Removed)
	}
}

// A failed removal leaves the row present, non-purged, carrying the marker.
func TestCloseSession_removeFailureKeepsMarkedRow(t *testing.T) {
	e := newTPEnv(t)
	// A .git DIRECTORY (a standalone clone) must never be deleted as a husk.
	if err := os.MkdirAll(filepath.Join(e.cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.open.RemoveErrAt = map[string]bool{e.cwd: true}
	if closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, false) {
		t.Fatal("closeSession = true, want false (not purged)")
	}
	e.wantLog(
		"SetMeta:"+tpID+":pgrouter.purge_pending=1",
		"Close:"+tpID+":purge=false",
		"RemoveWorktree:"+e.cwd,
	)
	if _, err := os.Stat(e.cwd); err != nil {
		t.Errorf("the directory must be left alone: %v", err)
	}
	if len(e.open.BranchDeletes) != 0 {
		t.Errorf("no branch delete on a failed removal; got %v", e.open.BranchDeletes)
	}
}

// A failed meta write falls back to today's ordering (purge first, then remove).
func TestCloseSession_metaWriteFailureFallsBackToSinglePhase(t *testing.T) {
	e := newTPEnv(t)
	e.cc.setMetaErr = errors.New("meta write failed")
	if !closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, false) {
		t.Fatal("closeSession = false, want true")
	}
	e.wantLog(
		"SetMeta:"+tpID+":pgrouter.purge_pending=1",
		"Close:"+tpID+":purge=true",
		"RemoveWorktree:"+e.cwd,
		"DeleteBranch:pg-router/"+tpBead,
	)
}

// A failed non-purge close leaves the row open and marked, and nothing removed.
func TestCloseSession_nonPurgeCloseFailureLeavesMarkedRow(t *testing.T) {
	e := newTPEnv(t)
	e.cc.closeErrNonPurge = errors.New("close failed")
	if closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, false) {
		t.Fatal("closeSession = true, want false")
	}
	e.wantLog(
		"SetMeta:"+tpID+":pgrouter.purge_pending=1",
		"Close:"+tpID+":purge=false",
	)
}

// A failed final purge keeps the marker for the next retry.
func TestCloseSession_purgeFailureAfterRemovalKeepsMarker(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	e.cc.closeErrPurge = errors.New("purge failed")
	if closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, false) {
		t.Fatal("closeSession = true, want false")
	}
}

// Interrupt simulation: the context dies between the non-purge close and the
// removal; nothing is removed or purged, and the NEXT reconcile completes it.
func TestTwoPhase_interruptBetweenPhasesIsCompletedByNextReconcile(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	ctx, cancel := context.WithCancel(context.Background())
	e.cc.onClose = func(_ string, purge bool) {
		if !purge {
			cancel() // the daemon is going down right after phase (a)
		}
	}
	if closeSession(ctx, e.cc, e.open.Open, tpRepoRoot, e.wtDir, e.session, false) {
		t.Fatal("an interrupted teardown must not report a purge")
	}
	e.wantLog(
		"SetMeta:"+tpID+":pgrouter.purge_pending=1",
		"Close:"+tpID+":purge=false",
	)
	if _, err := os.Stat(e.cwd); err != nil {
		t.Fatalf("the worktree must still exist after the interruption: %v", err)
	}

	// The next dispatch: the row survives, closed and marked; list it and reconcile.
	e.cc.onClose = nil
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcile = %d, want 1 (the interrupted teardown completed)", n)
	}
	if _, err := os.Stat(e.cwd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worktree must be removed by the retry: %v", err)
	}
	if got := e.cc.ClosedPurge; !reflect.DeepEqual(got, []bool{false, true}) {
		t.Errorf("ClosedPurge = %v, want [false true]", got)
	}
}

// Retry of a marked, non-live row whose worktree still exists: removed, then purged.
func TestRetry_existingWorktreeRemovedThenPurged(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	e.wantLog(
		e.attemptLog("1"),
		"RemoveWorktree:"+e.cwd,
		"DeleteBranch:pg-router/"+tpBead,
		"Close:"+tpID+":purge=true",
	)
	if len(e.open.Opens) == 0 || e.open.Opens[0] != tpRepoRoot {
		t.Errorf("retry must remove from the repo root; Opens=%v", e.open.Opens)
	}
}

// The attempt counter continues from the recorded value.
func TestRetry_attemptCountIncrements(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	row := e.markedRow(func(s *ccpool.Session) { s.Meta[ccpool.MetaKeyPurgeAttempts] = "2" })
	e.cc.ListSeq = [][]ccpool.Session{{row}}
	reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if got := e.log.all(); len(got) == 0 || got[0] != e.attemptLog("3") {
		t.Errorf("first call = %v, want %q", got, e.attemptLog("3"))
	}
}

// A missing directory counts as gone: pruned, branch deleted, row purged.
func TestRetry_missingDirectoryIsPurged(t *testing.T) {
	e := newTPEnv(t)
	if err := os.RemoveAll(e.cwd); err != nil {
		t.Fatal(err)
	}
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	e.wantLog(
		e.attemptLog("1"),
		"Prune",
		"DeleteBranch:pg-router/"+tpBead,
		"Close:"+tpID+":purge=true",
	)
	if len(e.open.Removed) != 0 {
		t.Errorf("nothing to remove for a missing directory; Removed=%v", e.open.Removed)
	}
}

// A directory under WorktreeDir with no .git (git no longer knows it) is
// deleted, the registrations pruned, and the row purged.
func TestRetry_unregisteredDirectoryIsDeletedAndPurged(t *testing.T) {
	e := newTPEnv(t)
	if err := os.WriteFile(filepath.Join(e.cwd, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.open.RemoveErrAt = map[string]bool{e.cwd: true}
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	if _, err := os.Stat(e.cwd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unregistered husk must be deleted: %v", err)
	}
	e.wantLog(
		e.attemptLog("1"),
		"RemoveWorktree:"+e.cwd,
		"Prune",
		"DeleteBranch:pg-router/"+tpBead,
		"Close:"+tpID+":purge=true",
	)
}

// A .git file pointing at a registration git no longer has is also a husk.
func TestRetry_danglingGitFileIsAHusk(t *testing.T) {
	e := newTPEnv(t)
	gone := filepath.Join(t.TempDir(), "worktrees", "zr-w")
	if err := os.WriteFile(filepath.Join(e.cwd, ".git"), []byte("gitdir: "+gone+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.open.RemoveErrAt = map[string]bool{e.cwd: true}
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil); n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	if _, err := os.Stat(e.cwd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the husk must be deleted: %v", err)
	}
}

// A husk outside WorktreeDir is never deleted by os.RemoveAll.
func TestRetry_neverDeletesOutsideWorktreeDir(t *testing.T) {
	e := newTPEnv(t)
	other := t.TempDir()
	elsewhere := filepath.Join(other, "zr-w")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	// Not eligible for two-phase at all (outside WorktreeDir), but a marked row
	// naming it must still never be RemoveAll'd.
	e.open.RemoveErrAt = map[string]bool{elsewhere: true}
	row := e.markedRow(func(s *ccpool.Session) { s.CWD = elsewhere })
	e.cc.ListSeq = [][]ccpool.Session{{row}}
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil); n != 0 {
		t.Fatalf("reconcile = %d, want 0", n)
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Errorf("a directory outside WorktreeDir must never be deleted: %v", err)
	}
}

// ANY Live peer on the same CWD, in ANY state, keeps the worktree: the row is
// purged without removing anything (INV-CCH-15).
func TestRetry_livePeerKeepsWorktree(t *testing.T) {
	for _, st := range []ccpool.SessionState{
		ccpool.StateIdle, ccpool.StateNeedsInput, ccpool.StateWorking, ccpool.StateStarting, ccpool.StateReady, ccpool.StateErrored,
	} {
		t.Run(string(st), func(t *testing.T) {
			e := newTPEnv(t)
			peer := ccpool.Session{ExternalID: "pg-router-review-zr-w", State: st, Live: true, CWD: e.cwd}
			e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil), peer}}
			n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
			if n != 1 {
				t.Fatalf("reconcile = %d, want 1", n)
			}
			e.wantLog(e.attemptLog("1"), "Close:"+tpID+":purge=true")
			if len(e.open.Removed) != 0 {
				t.Errorf("a live peer's worktree must not be removed; Removed=%v", e.open.Removed)
			}
			if _, err := os.Stat(e.cwd); err != nil {
				t.Errorf("worktree must remain: %v", err)
			}
		})
	}
}

// A DEAD peer on the same CWD does not protect the worktree.
func TestRetry_deadPeerDoesNotKeepWorktree(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	peer := ccpool.Session{ExternalID: "pg-router-review-zr-w", State: ccpool.StateIdle, Live: false, CloseReason: "idle_ttl", CWD: e.cwd}
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil), peer}}
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil); n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	if len(e.open.Removed) != 1 {
		t.Errorf("Removed = %v, want one removal", e.open.Removed)
	}
}

// Only ONE marked row is retried per pass.
func TestRetry_boundedToOneRowPerPass(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	cwd2 := filepath.Join(e.wtDir, "zr-x")
	if err := os.MkdirAll(cwd2, 0o755); err != nil {
		t.Fatal(err)
	}
	a := e.markedRow(nil)
	b := e.markedRow(func(s *ccpool.Session) {
		s.ExternalID, s.CWD = "pg-router-worker-zr-x", cwd2
		s.Meta[ccpool.MetaKeyBead] = "zr-x"
	})
	e.cc.ListSeq = [][]ccpool.Session{{a, b}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcile = %d, want 1 (one retry per pass)", n)
	}
	if !reflect.DeepEqual(e.cc.Closed, []string{tpID}) {
		t.Errorf("only the first marked row may be handled; Closed=%v", e.cc.Closed)
	}
	if _, err := os.Stat(cwd2); err != nil {
		t.Errorf("the second row's worktree must be untouched this pass: %v", err)
	}
}

// A row skipped by a guard is not an attempt: the next marked row gets the slot.
func TestRetry_midTurnRowIsSkippedNotCounted(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	cwd2 := filepath.Join(e.wtDir, "zr-x")
	if err := os.MkdirAll(cwd2, 0o755); err != nil {
		t.Fatal(err)
	}
	working := e.markedRow(func(s *ccpool.Session) {
		s.ExternalID, s.CWD = "pg-router-worker-zr-y", filepath.Join(e.wtDir, "zr-y")
		s.State, s.Live, s.CloseReason = ccpool.StateWorking, true, ""
	})
	next := e.markedRow(func(s *ccpool.Session) {
		s.ExternalID, s.CWD = "pg-router-worker-zr-x", cwd2
		s.Meta[ccpool.MetaKeyBead] = "zr-x"
	})
	e.cc.ListSeq = [][]ccpool.Session{{working, next}}
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil); n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	for _, id := range e.cc.Closed {
		if id == "pg-router-worker-zr-y" {
			t.Errorf("a mid-turn row must never be closed; Closed=%v", e.cc.Closed)
		}
	}
}

// A still-live marked row (its non-purge close failed) is re-closed non-purge
// first, then removed and purged.
func TestRetry_liveMarkedRowIsReclosedFirst(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	row := e.markedRow(func(s *ccpool.Session) { s.Live, s.CloseReason = true, "" })
	e.cc.ListSeq = [][]ccpool.Session{{row}}
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil); n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	e.wantLog(
		e.attemptLog("1"),
		"Close:"+tpID+":purge=false",
		"RemoveWorktree:"+e.cwd,
		"DeleteBranch:pg-router/"+tpBead,
		"Close:"+tpID+":purge=true",
	)
}

// A still-live marked row whose transcript is not quiet is deferred.
func TestRetry_liveMarkedRowWithActiveTranscriptIsDeferred(t *testing.T) {
	e := newTPEnv(t)
	row := e.markedRow(func(s *ccpool.Session) { s.Live, s.CloseReason = true, "" })
	e.cc.ListSeq = [][]ccpool.Session{{row}}
	notQuiet := func(ccpool.Session) bool { return false }
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, notQuiet); n != 0 {
		t.Fatalf("reconcile = %d, want 0", n)
	}
	e.wantLog()
}

// A failed retry keeps the row and the marker; the count is still recorded.
func TestRetry_failedRetryKeepsRow(t *testing.T) {
	e := newTPEnv(t)
	if err := os.MkdirAll(filepath.Join(e.cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.open.RemoveErrAt = map[string]bool{e.cwd: true}
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir, nil); n != 0 {
		t.Fatalf("reconcile = %d, want 0", n)
	}
	e.wantLog(e.attemptLog("1"), "RemoveWorktree:"+e.cwd)
}

// One pass: a marked row is handled by exactly one branch. With a closed bead
// and a reconcilable state, the closed-bead branch must NOT also close it.
func TestReconcile_markedRowIsHandledOncePerPass(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	idleMarked := e.markedRow(func(s *ccpool.Session) { s.State = ccpool.StateIdle })
	e.cc.ListSeq = [][]ccpool.Session{{idleMarked}}
	n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, closedBead, "pg-router-", tpRepoRoot, e.wtDir, nil)
	if n != 1 {
		t.Fatalf("reconcile = %d, want 1", n)
	}
	if !reflect.DeepEqual(e.cc.ClosedPurge, []bool{true}) {
		t.Errorf("a marked row must be purged exactly once, never re-two-phased; ClosedPurge=%v", e.cc.ClosedPurge)
	}
}

// A marked row whose retry is skipped (mid-turn) is still not picked up by the
// closed-bead branch in the same pass.
func TestReconcile_skippedMarkedRowIsNotHandledByClosedBeadBranch(t *testing.T) {
	e := newTPEnv(t)
	// needs_input would be reconcilable (closed bead), but it is marked; and it is
	// still live with an active transcript, so the retry defers it.
	row := e.markedRow(func(s *ccpool.Session) { s.State, s.Live, s.CloseReason = ccpool.StateNeedsInput, true, "" })
	e.cc.ListSeq = [][]ccpool.Session{{row}}
	notQuiet := func(ccpool.Session) bool { return false }
	if n := reconcileClosedBeadSessions(context.Background(), e.cc, e.open.Open, closedBead, "pg-router-", tpRepoRoot, e.wtDir, notQuiet); n != 0 {
		t.Fatalf("reconcile = %d, want 0", n)
	}
	e.wantLog()
}

// The shutdown sweep finishes a marked row instead of re-deciding it.
func TestTeardownAllSessions_retriesMarkedRow(t *testing.T) {
	e := newTPEnv(t)
	e.removingWorktree()
	e.cc.ListSeq = [][]ccpool.Session{{e.markedRow(nil)}}
	n := teardownAllSessions(context.Background(), e.cc, e.open.Open, fakeBR{}, "pg-router-", tpRepoRoot, e.wtDir)
	if n != 1 {
		t.Fatalf("teardownAllSessions = %d, want 1", n)
	}
	e.wantLog(
		e.attemptLog("1"),
		"RemoveWorktree:"+e.cwd,
		"DeleteBranch:pg-router/"+tpBead,
		"Close:"+tpID+":purge=true",
	)
}

// The orphan pass skips a marked row even when its lease has expired.
func TestIsOrphanOf_skipsPurgePendingRow(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	role := roles.Role{Name: "worker"}
	row := ccpool.Session{
		ExternalID: "pg-router-worker-zr-w",
		State:      ccpool.StateIdle,
		Meta: map[string]string{
			ccpool.MetaKeyPool:       ccpool.PoolName,
			ccpool.MetaKeyRole:       "worker",
			ccpool.MetaKeyLeaseUntil: ccpool.FormatMetaTime(now.Add(-time.Hour)),
		},
	}
	if !isOrphanOf(row, role, "pg-router-", now) {
		t.Fatal("control: an unmarked expired-lease row must be an orphan")
	}
	row.Meta[ccpool.MetaKeyPurgePending] = "1"
	if isOrphanOf(row, role, "pg-router-", now) {
		t.Error("a purge_pending row must not be an orphan (INV-CCH-20 exclusivity)")
	}
}

func TestPathStrictlyUnder(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", false},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/a/b/c/d", "/a/b", true},
		{"/a/b/../x", "/a/b", false},
	}
	for _, c := range cases {
		if got := pathStrictlyUnder(c.path, c.dir); got != c.want {
			t.Errorf("pathStrictlyUnder(%q, %q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}
