package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
)

// guardWM is a git client that, unlike fakeWorktreeManager, can read status,
// refs and the current branch, so the stale-session guard has something to
// read. One instance serves both the worktree and the canonical clone.
type guardWM struct {
	*fakeWorktreeManager
	dirty      []gitclient.StatusEntry
	statusErr  error
	hasUp      bool
	hasUpErr   error
	aheadUp    int // CommitsAhead("@{u}", "HEAD")
	aheadRoot  int // CommitsAhead("HEAD", <branch>)
	aheadErr   error
	branch     string
	branchErr  error
	aheadCalls [][2]string
}

var (
	_ gitclient.StatusReader = (*guardWM)(nil)
	_ gitclient.RefReader    = (*guardWM)(nil)
	_ gitclient.Locator      = (*guardWM)(nil)
)

func (g *guardWM) Status(context.Context) ([]gitclient.StatusEntry, error) {
	return g.dirty, g.statusErr
}
func (g *guardWM) IsTracked(context.Context, string) (bool, error) { return true, nil }
func (g *guardWM) RefExists(context.Context, string) (bool, error) { return true, nil }
func (g *guardWM) HasUpstream(context.Context) (bool, error)       { return g.hasUp, g.hasUpErr }
func (g *guardWM) CommitsAhead(_ context.Context, base, tip string) (int, error) {
	g.aheadCalls = append(g.aheadCalls, [2]string{base, tip})
	if g.aheadErr != nil {
		return 0, g.aheadErr
	}
	if base == "@{u}" {
		return g.aheadUp, nil
	}
	return g.aheadRoot, nil
}
func (g *guardWM) Toplevel(context.Context) (string, error)  { return "", nil }
func (g *guardWM) CommonDir(context.Context) (string, error) { return "", nil }
func (g *guardWM) CurrentBranch(context.Context) (string, error) {
	return g.branch, g.branchErr
}
func (g *guardWM) RemoteURL(context.Context, string) (string, error) { return "", nil }

// guardOpener opens guardWM for every directory except those in notARepo
// (gitclient.ErrNotARepository) and openErr (a generic failure).
type guardOpener struct {
	fakeWorktreeOpener
	wm        *guardWM
	notARepo  map[string]bool
	openErrAt map[string]error
}

func newGuardOpener() *guardOpener {
	o := &guardOpener{}
	o.wm = &guardWM{fakeWorktreeManager: &fakeWorktreeManager{owner: &o.fakeWorktreeOpener}, branch: "pg-router/zr-done"}
	return o
}

func (o *guardOpener) Open(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
	if err := o.openErrAt[dir]; err != nil {
		return nil, err
	}
	if o.notARepo[dir] {
		return nil, gitclient.ErrNotARepository
	}
	o.mu.Lock()
	o.Opens = append(o.Opens, dir)
	o.mu.Unlock()
	return o.wm, nil
}

const (
	staleNow    = int64(2_000_000_000)
	staleRepo   = "/repo/root"
	stalePrefix = "pg-router-"
)

var staleSessionID = "pg-router-worker-zr-done-20260919T033857.961022000"

// staleHarness is one reconcile pass over a single default-pool session.
type staleHarness struct {
	t       *testing.T
	cc      *fakeCC
	open    *guardOpener
	br      fakeBR
	sess    ccpool.Session
	latest  func(string) (time.Time, bool)
	wtDir   string // handler cfg.WorktreeDir ("" = single-phase teardown)
	idleFor time.Duration
}

// newStaleHarness: a not-live needs_input session, bead closed, idle 10 days,
// working directory a real existing directory.
func newStaleHarness(t *testing.T) *staleHarness {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &staleHarness{
		t:    t,
		open: newGuardOpener(),
		br:   fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}},
		sess: ccpool.Session{
			ExternalID:     staleSessionID,
			State:          ccpool.StateNeedsInput,
			Live:           false,
			CWD:            cwd,
			LastActivityAt: staleNow - int64((10 * 24 * time.Hour).Seconds()),
			Meta:           map[string]string{ccpool.MetaKeyBead: "zr-done"},
		},
		latest: func(string) (time.Time, bool) { return time.Time{}, false },
	}
	return h
}

// run executes the guarded default-pool reconcile and returns the closed count.
func (h *staleHarness) run() int {
	h.t.Helper()
	h.cc = &fakeCC{ListSeq: [][]ccpool.Session{{h.sess}}}
	guard := newStaleSessionGuard(h.open.Open, staleRepo, staleSessionMinIdle,
		func() time.Time { return time.Unix(staleNow, 0) }, h.latest)
	return reconcileStaleDefaultPoolSessions(context.Background(), h.cc, h.open.Open, h.br,
		stalePrefix, staleRepo, h.wtDir, nil, guard)
}

func (h *staleHarness) wantClosed() {
	h.t.Helper()
	if n := h.run(); n != 1 {
		h.t.Fatalf("session must be closed; n=%d closed=%v", n, h.cc.Closed)
	}
	if len(h.cc.Closed) == 0 || h.cc.Closed[len(h.cc.Closed)-1] != staleSessionID {
		h.t.Fatalf("closed=%v, want %s", h.cc.Closed, staleSessionID)
	}
	if len(h.cc.ClosedPurge) == 0 || !h.cc.ClosedPurge[len(h.cc.ClosedPurge)-1] {
		h.t.Errorf("the row must be purged (the transcript is a separate file, never removed); purge=%v", h.cc.ClosedPurge)
	}
}

func (h *staleHarness) wantPreserved() {
	h.t.Helper()
	if n := h.run(); n != 0 {
		h.t.Fatalf("session must be preserved; n=%d closed=%v", n, h.cc.Closed)
	}
	if len(h.cc.Closed) != 0 || len(h.cc.MetaSets) != 0 {
		h.t.Errorf("a preserved session must not be touched; closed=%v meta=%v", h.cc.Closed, h.cc.MetaSets)
	}
	if len(h.open.Removed) != 0 || len(h.open.BranchDeletes) != 0 {
		h.t.Errorf("a preserved session's worktree/branch must not be touched; removed=%v branches=%v",
			h.open.Removed, h.open.BranchDeletes)
	}
}

// ---- close branches ----

// The incident shape: not-live needs_input, bead closed, idle >7d, worktree
// absent. Two-phase teardown (the CWD is under the handler worktree dir) must
// end in a purge.
func TestStaleDefaultPool_closesAbsentWorktree_twoPhase(t *testing.T) {
	h := newStaleHarness(t)
	root := t.TempDir()
	h.wtDir = filepath.Join(root, "worktrees")
	h.sess.CWD = filepath.Join(h.wtDir, "zr-done") // never created
	h.wantClosed()
	if len(h.cc.MetaSets) != 1 {
		t.Errorf("two-phase teardown must mark purge_pending first; meta=%v", h.cc.MetaSets)
	}
	if len(h.cc.ClosedPurge) != 2 || h.cc.ClosedPurge[0] || !h.cc.ClosedPurge[1] {
		t.Errorf("want non-purge close then purge; got %v", h.cc.ClosedPurge)
	}
}

func TestStaleDefaultPool_closesEmptyCWD(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.CWD = ""
	h.wantClosed()
}

func TestStaleDefaultPool_closesCleanPushedWorktree(t *testing.T) {
	h := newStaleHarness(t)
	h.open.wm.hasUp = true
	h.open.wm.aheadUp = 0
	h.wantClosed()
	if len(h.open.Removed) != 1 {
		t.Errorf("the clean pushed worktree is removed with the session; removed=%v", h.open.Removed)
	}
}

func TestStaleDefaultPool_closesCleanWorktreeWithNoUpstreamAndNoUniqueCommits(t *testing.T) {
	h := newStaleHarness(t)
	h.open.wm.hasUp = false
	h.open.wm.aheadRoot = 0
	h.wantClosed()
	// The no-upstream comparison is against the canonical clone's HEAD.
	if got := h.open.wm.aheadCalls; len(got) != 1 || got[0] != [2]string{"HEAD", "pg-router/zr-done"} {
		t.Errorf("no-upstream branch must be compared against the canonical HEAD; calls=%v", got)
	}
}

func TestStaleDefaultPool_closesWorktreeOutsideAnyRepo(t *testing.T) {
	h := newStaleHarness(t)
	h.open.notARepo = map[string]bool{h.sess.CWD: true}
	h.wantClosed()
}

func TestStaleDefaultPool_closesSessionRunningInTheCanonicalClone(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.CWD = staleRepo // "none" isolation; absent on this machine, and git never removes a main tree
	h.wantClosed()
}

func TestStaleDefaultPool_closesIdleSessionToo(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.State = ccpool.StateIdle
	h.wantClosed()
}

// ---- preserve branches: worktree ----

func TestStaleDefaultPool_preservesDirtyWorktree(t *testing.T) {
	h := newStaleHarness(t)
	h.open.wm.dirty = []gitclient.StatusEntry{{Path: "notes.md"}}
	h.wantPreserved()
}

func TestStaleDefaultPool_preservesWorktreeWithCommitsNotOnUpstream(t *testing.T) {
	h := newStaleHarness(t)
	h.open.wm.hasUp = true
	h.open.wm.aheadUp = 2
	h.wantPreserved()
}

func TestStaleDefaultPool_preservesNoUpstreamBranchWithUniqueCommits(t *testing.T) {
	h := newStaleHarness(t)
	h.open.wm.hasUp = false
	h.open.wm.aheadRoot = 3
	h.wantPreserved()
}

func TestStaleDefaultPool_preservesWhenGitStateUnreadable(t *testing.T) {
	boom := errors.New("git exploded")
	cases := map[string]func(h *staleHarness){
		"status error":        func(h *staleHarness) { h.open.wm.statusErr = boom },
		"upstream error":      func(h *staleHarness) { h.open.wm.hasUpErr = boom },
		"count error (up)":    func(h *staleHarness) { h.open.wm.hasUp = true; h.open.wm.aheadErr = boom },
		"count error (root)":  func(h *staleHarness) { h.open.wm.aheadErr = boom },
		"detached HEAD":       func(h *staleHarness) { h.open.wm.branchErr = gitclient.ErrDetachedHEAD },
		"open failure":        func(h *staleHarness) { h.open.openErrAt = map[string]error{h.sess.CWD: boom} },
		"repo root unopenble": func(h *staleHarness) { h.open.openErrAt = map[string]error{staleRepo: boom} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newStaleHarness(t)
			mutate(h)
			h.wantPreserved()
		})
	}
}

func TestStaleDefaultPool_preservesWhenWorktreeStatFails(t *testing.T) {
	h := newStaleHarness(t)
	// A path component that is a regular file makes Lstat fail with ENOTDIR,
	// which is neither "absent" nor readable.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.sess.CWD = filepath.Join(file, "wt")
	h.wantPreserved()
}

// ---- preserve branches: bead ----

func TestStaleDefaultPool_preservesOpenBead(t *testing.T) {
	for _, status := range []string{"open", "in_progress"} {
		h := newStaleHarness(t)
		h.br = fakeBR{out: map[string]string{"show zr-done --json": `{"status":"` + status + `"}`}}
		h.wantPreserved()
	}
}

func TestStaleDefaultPool_preservesOnBeadLookupError(t *testing.T) {
	h := newStaleHarness(t)
	h.br = fakeBR{err: errors.New("bd down")}
	h.wantPreserved()
}

func TestStaleDefaultPool_preservesBeadThatCannotBeFound(t *testing.T) {
	// bd reports an unknown id as an error, indistinguishable from an outage:
	// failing closed is the only safe reading.
	h := newStaleHarness(t)
	h.br = fakeBR{err: errors.New("bd show: issue not found")}
	h.wantPreserved()
}

func TestStaleDefaultPool_preservesSessionWithNoBeadTag(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.Meta = nil
	h.wantPreserved()
}

// ---- preserve branches: state and idle age ----

func TestStaleDefaultPool_preservesWorkingSession(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.State = ccpool.StateWorking
	h.wantPreserved()
}

func TestStaleDefaultPool_preservesSessionIdleUnderThreshold(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.LastActivityAt = staleNow - int64((6*24*time.Hour + 23*time.Hour).Seconds())
	h.wantPreserved()
}

func TestStaleDefaultPool_closesAtExactlyTheThreshold(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.LastActivityAt = staleNow - int64(staleSessionMinIdle.Seconds())
	h.wantClosed()
}

func TestStaleDefaultPool_preservesWhenIdleAgeUnknown(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.LastActivityAt = 0 // an older ccpool that does not report it
	h.sess.TranscriptPath = ""
	h.wantPreserved()
}

func TestStaleDefaultPool_recentTranscriptActivityOverridesOldLastActivity(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.TranscriptPath = "/t/x.jsonl"
	h.latest = func(string) (time.Time, bool) { return time.Unix(staleNow, 0).Add(-time.Hour), true }
	h.wantPreserved()
}

func TestStaleDefaultPool_oldTranscriptAloneSuffices(t *testing.T) {
	// No last_activity_at (older ccpool) but a transcript untouched for 10 days.
	h := newStaleHarness(t)
	h.sess.LastActivityAt = 0
	h.sess.TranscriptPath = "/t/x.jsonl"
	h.latest = func(string) (time.Time, bool) { return time.Unix(staleNow, 0).Add(-10 * 24 * time.Hour), true }
	h.wantClosed()
}

func TestStaleDefaultPool_ignoresOtherPrefixes(t *testing.T) {
	h := newStaleHarness(t)
	h.sess.ExternalID = "cc-someone-elses-session"
	h.wantPreserved()
}

func TestStaleDefaultPool_listFailureIsSoft(t *testing.T) {
	cc := &fakeCC{listErr: errors.New("ccpool down")}
	guard := newStaleSessionGuard((&fakeWorktreeOpener{}).Open, staleRepo, staleSessionMinIdle, nil, nil)
	if n := reconcileStaleDefaultPoolSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, fakeBR{},
		stalePrefix, staleRepo, "", nil, guard); n != 0 {
		t.Fatalf("n=%d, want 0", n)
	}
}

// A mixed pass: only the qualifying row is closed.
func TestStaleDefaultPool_mixedPass(t *testing.T) {
	h := newStaleHarness(t)
	stale := h.sess
	fresh := h.sess
	fresh.ExternalID = "pg-router-worker-zr-fresh"
	fresh.Meta = map[string]string{ccpool.MetaKeyBead: "zr-fresh"}
	fresh.LastActivityAt = staleNow - 60
	open := h.sess
	open.ExternalID = "pg-router-worker-zr-open"
	open.Meta = map[string]string{ccpool.MetaKeyBead: "zr-open"}
	h.br = fakeBR{out: map[string]string{
		"show zr-done --json":  `{"status":"closed"}`,
		"show zr-fresh --json": `{"status":"closed"}`,
		"show zr-open --json":  `{"status":"open"}`,
	}}
	h.cc = &fakeCC{ListSeq: [][]ccpool.Session{{fresh, open, stale}}}
	guard := newStaleSessionGuard(h.open.Open, staleRepo, staleSessionMinIdle,
		func() time.Time { return time.Unix(staleNow, 0) }, h.latest)
	n := reconcileStaleDefaultPoolSessions(context.Background(), h.cc, h.open.Open, h.br, stalePrefix, staleRepo, "", nil, guard)
	if n != 1 || len(h.cc.Closed) != 1 || h.cc.Closed[0] != staleSessionID {
		t.Fatalf("only the stale closed-bead row may close; n=%d closed=%v", n, h.cc.Closed)
	}
}

// The guard must not change the unguarded (role-pool) pass: it still closes a
// freshly-idle closed-bead session with no idle-age or worktree condition.
func TestReconcileClosedBeadSessions_unguardedPassIsUnchanged(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{{
		ExternalID:     "pg-router-worker-zr-done",
		State:          ccpool.StateNeedsInput,
		LastActivityAt: staleNow,
		Meta:           map[string]string{ccpool.MetaKeyBead: "zr-done"},
	}}}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	if n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, stalePrefix, staleRepo, "", nil); n != 1 {
		t.Fatalf("role-pool pass must still close it; n=%d", n)
	}
}

func TestSessionIdleAge(t *testing.T) {
	now := time.Unix(staleNow, 0)
	none := func(string) (time.Time, bool) { return time.Time{}, false }
	if _, known := sessionIdleAge(ccpool.Session{}, now, none); known {
		t.Error("no signals must be unknown")
	}
	if age, known := sessionIdleAge(ccpool.Session{LastActivityAt: staleNow - 100}, now, none); !known || age != 100*time.Second {
		t.Errorf("age=%v known=%v, want 100s", age, known)
	}
	newer := func(string) (time.Time, bool) { return now.Add(-10 * time.Second), true }
	if age, _ := sessionIdleAge(ccpool.Session{LastActivityAt: staleNow - 100, TranscriptPath: "/t"}, now, newer); age != 10*time.Second {
		t.Errorf("age=%v, want the NEWER transcript activity (10s)", age)
	}
	older := func(string) (time.Time, bool) { return now.Add(-1000 * time.Second), true }
	if age, _ := sessionIdleAge(ccpool.Session{LastActivityAt: staleNow - 100, TranscriptPath: "/t"}, now, older); age != 100*time.Second {
		t.Errorf("age=%v, want the newer last_activity_at (100s)", age)
	}
}
