package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/x/gitclient"
)

// fakeCC is a minimal ccpool.Runner test double — this module's own local
// reimplementation of packages/pg-router's (now-deleted) dtest.FakeCC shape
// (ListSeq/Closed/ClosedPurge), since that package is unreachable from here
// (Go's internal-package visibility rule). listErr (reconcile_test.go's own
// addition, pg2-hrppg) injects a `ccpool list` failure; unset (nil), List
// behaves exactly as before.
type fakeCC struct {
	mu          sync.Mutex
	ListSeq     [][]ccpool.Session
	listIdx     int
	listErr     error
	Closed      []string
	ClosedPurge []bool

	// callLog, when non-nil, receives one entry per SetMeta/Close call, in order;
	// share it with fakeWorktreeOpener.callLog to assert the cross-seam ordering
	// of a two-phase teardown (pg2-kqegi).
	callLog *callLog
	// MetaSets records every SetMeta call as "id key=value".
	MetaSets []string
	// setMetaErr, when set, fails every SetMeta (after recording it).
	setMetaErr error
	// closeErrNonPurge / closeErrPurge fail the corresponding Close calls.
	closeErrNonPurge, closeErrPurge error
	// onClose, when set, runs inside every Close (after recording it).
	onClose func(externalID string, purge bool)
}

// callLog is a goroutine-safe ordered log shared between the fakes.
type callLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *callLog) add(e string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func (f *fakeCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}
func (f *fakeCC) Send(context.Context, string, string, ccpool.SendMode) error { return nil }
func (f *fakeCC) Cancel(context.Context, string) error                        { return nil }

func (f *fakeCC) Close(_ context.Context, externalID string, purge bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Closed = append(f.Closed, externalID)
	f.ClosedPurge = append(f.ClosedPurge, purge)
	f.callLog.add("Close:" + externalID + ":purge=" + strconv.FormatBool(purge))
	if f.onClose != nil {
		f.onClose(externalID, purge)
	}
	if purge {
		return f.closeErrPurge
	}
	return f.closeErrNonPurge
}

func (f *fakeCC) List(context.Context) ([]ccpool.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.ListSeq) == 0 {
		return nil, nil
	}
	i := f.listIdx
	if i >= len(f.ListSeq) {
		i = len(f.ListSeq) - 1
	}
	f.listIdx++
	return f.ListSeq[i], nil
}

func (f *fakeCC) SetMeta(_ context.Context, externalID, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MetaSets = append(f.MetaSets, externalID+" "+key+"="+value)
	f.callLog.add("SetMeta:" + externalID + ":" + key + "=" + value)
	return f.setMetaErr
}

func (f *fakeCC) Capacity(context.Context) (ccpool.Capacity, error) {
	return ccpool.Capacity{Free: 1}, nil
}

var _ ccpool.Runner = (*fakeCC)(nil)

// fakeWorktreeOpener is a worktree.Opener test double supporting per-path
// error injection on BOTH failure shapes the removed shutdown sweep's fail-soft
// worktree removal must tolerate: Open itself failing (a cwd that is not
// inside any git repository at all — the "path"/"workforest" isolation
// case) and Open succeeding but the returned manager's RemoveWorktree
// failing (a cwd that IS inside a repository but is not a registered
// linked worktree of it — the "none" isolation case, where cwd is the
// repo's own main working tree). This file's own local double (mirrors
// fakeCC's own local-reimplementation rationale above); internal/dtest's
// shared NoopGitOpener/NoopWorktreeManager offer no error injection on
// RemoveWorktree itself, only on Open.
type fakeWorktreeOpener struct {
	mu            sync.Mutex
	OpenErrAt     map[string]bool // dir -> Open fails (not inside any git repository)
	RemoveErrAt   map[string]bool // dir -> the opened manager's RemoveWorktree fails (not a linked worktree)
	Removed       []string        // every path RemoveWorktree was actually invoked with
	Opens         []string        // every dir Open was invoked with, in order (pg2-tpa18)
	BranchDeletes []branchDelete  // every DeleteBranch call recorded (pg2-tpa18)
	Pruned        int             // PruneWorktrees calls (pg2-kqegi)
	callLog       *callLog        // shared ordered log (pg2-kqegi); nil disables
	// removeHook runs inside RemoveWorktree (after recording it) and returns the
	// error to report, letting a test simulate a removal that deletes the directory
	// (or fails) with real filesystem effects.
	removeHook func(path string) error
}

// branchDelete records one DeleteBranch(branch, force) call — pg2-tpa18's
// own test double addition, mirroring internal/dtest.NoopWorktreeManager's
// DeleteBranch recording added by bead pg2-ci75j at the other two call
// sites.
type branchDelete struct {
	Branch string
	Force  bool
}

func (o *fakeWorktreeOpener) Open(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
	o.mu.Lock()
	o.Opens = append(o.Opens, dir)
	o.mu.Unlock()
	if o.OpenErrAt[dir] {
		return nil, gitclient.ErrNotARepository
	}
	return &fakeWorktreeManager{owner: o}, nil
}

// fakeWorktreeManager is both a gitclient.WorktreeManager AND a
// gitclient.BranchManager — like the real *gitclient.Client — so
// deleteAnchorBranch's own `root.(gitclient.BranchManager)` type assertion
// (preshutdown.go) succeeds against this fake too (bead pg2-ci75j's pattern,
// applied at this third call site by pg2-tpa18).
type fakeWorktreeManager struct{ owner *fakeWorktreeOpener }

var (
	_ gitclient.WorktreeManager = (*fakeWorktreeManager)(nil)
	_ gitclient.BranchManager   = (*fakeWorktreeManager)(nil)
)

func (m *fakeWorktreeManager) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) (*gitclient.Handle, error) {
	return nil, nil // unused by preShutdown's teardown sweep
}

func (m *fakeWorktreeManager) RemoveWorktree(_ context.Context, path string, _ bool) error {
	m.owner.mu.Lock()
	defer m.owner.mu.Unlock()
	m.owner.Removed = append(m.owner.Removed, path)
	m.owner.callLog.add("RemoveWorktree:" + path)
	if m.owner.removeHook != nil {
		return m.owner.removeHook(path)
	}
	if m.owner.RemoveErrAt[path] {
		return errors.New("git: not a linked worktree")
	}
	return nil
}

func (m *fakeWorktreeManager) PruneWorktrees(context.Context) error {
	m.owner.mu.Lock()
	defer m.owner.mu.Unlock()
	m.owner.Pruned++
	m.owner.callLog.add("Prune")
	return nil
}

func (m *fakeWorktreeManager) DeleteBranch(_ context.Context, branch string, force bool) error {
	m.owner.mu.Lock()
	defer m.owner.mu.Unlock()
	m.owner.BranchDeletes = append(m.owner.BranchDeletes, branchDelete{Branch: branch, Force: force})
	m.owner.callLog.add("DeleteBranch:" + branch)
	return nil
}

func contains(a []string, x string) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}

// TestCloseSessionAndWorktree_deletesAnchorBranchAfterSuccessfulRemoval is
// pg2-tpa18's own regression: bead pg2-ci75j fixed the orphaned-branch bug
// at internal/executor.cleanupWorktree and the nix mkWorktreeSweepScript,
// but explicitly left THIS preShutdown/reconcile call site
// (closeSessionAndWorktree) out of scope. Once RemoveWorktree succeeds,
// closeSessionAndWorktree must reopen the gitclient at repoRoot (NOT the
// now-removed worktree dir — proven here by asserting the exact Open order)
// and delete the session's own pg-router/<beadID> anchor branch, force=true.
func TestCloseSessionAndWorktree_deletesAnchorBranchAfterSuccessfulRemoval(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{}
	s := ccpool.Session{
		ExternalID: "pg-router-worker-zr-w",
		State:      ccpool.StateIdle,
		CWD:        "/wt/zr-w",
		Meta:       map[string]string{ccpool.MetaKeyBead: "zr-w"},
	}
	if !closeSessionAndWorktree(context.Background(), cc, open.Open, "/repo/root", "", s) {
		t.Fatal("closeSessionAndWorktree = false, want true")
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/wt/zr-w" {
		t.Fatalf("RemoveWorktree calls = %v, want exactly one call for /wt/zr-w", open.Removed)
	}
	if len(open.BranchDeletes) != 1 || open.BranchDeletes[0] != (branchDelete{Branch: "pg-router/zr-w", Force: true}) {
		t.Fatalf("DeleteBranch calls = %v, want exactly one [pg-router/zr-w force=true]", open.BranchDeletes)
	}
	if len(open.Opens) != 2 || open.Opens[0] != "/wt/zr-w" || open.Opens[1] != "/repo/root" {
		t.Errorf("expected Open(cwd) then Open(repoRoot), got %v", open.Opens)
	}
}

// TestCloseSessionAndWorktree_removeFailureNeverDeletesBranch proves the
// branch-delete step never runs at all (no second Open) when RemoveWorktree
// itself fails — the worktree is presumably still there (e.g. dirty), so its
// anchor branch must not be deleted out from under it either.
func TestCloseSessionAndWorktree_removeFailureNeverDeletesBranch(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{RemoveErrAt: map[string]bool{"/wt/zr-w": true}}
	s := ccpool.Session{
		ExternalID: "pg-router-worker-zr-w",
		State:      ccpool.StateIdle,
		CWD:        "/wt/zr-w",
		Meta:       map[string]string{ccpool.MetaKeyBead: "zr-w"},
	}
	if !closeSessionAndWorktree(context.Background(), cc, open.Open, "/repo/root", "", s) {
		t.Fatal("closeSessionAndWorktree = false, want true: a RemoveWorktree failure must not be treated as a close failure")
	}
	if len(open.Opens) != 1 || open.Opens[0] != "/wt/zr-w" {
		t.Errorf("a failed RemoveWorktree must short-circuit before any branch-delete Open; opens=%v", open.Opens)
	}
	if len(open.BranchDeletes) != 0 {
		t.Errorf("branch must not be deleted when RemoveWorktree failed; deletes=%v", open.BranchDeletes)
	}
}

// TestCloseSessionAndWorktree_openFailureNeverDeletesBranch is the other
// fail-soft shape's own analogue: when Open(s.CWD) itself fails (cwd not
// inside any git repository at all), the branch-delete step must never run
// either — there was never a confirmed worktree removal to follow up on.
func TestCloseSessionAndWorktree_openFailureNeverDeletesBranch(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{OpenErrAt: map[string]bool{"/scratch/not-a-repo": true}}
	s := ccpool.Session{
		ExternalID: "pg-router-worker-zr-w",
		State:      ccpool.StateIdle,
		CWD:        "/scratch/not-a-repo",
		Meta:       map[string]string{ccpool.MetaKeyBead: "zr-w"},
	}
	if !closeSessionAndWorktree(context.Background(), cc, open.Open, "/repo/root", "", s) {
		t.Fatal("closeSessionAndWorktree = false, want true: an Open failure must not be treated as a close failure")
	}
	if len(open.Opens) != 1 {
		t.Errorf("an Open(cwd) failure must short-circuit before any branch-delete Open; opens=%v", open.Opens)
	}
	if len(open.BranchDeletes) != 0 {
		t.Errorf("branch must not be deleted when Open(cwd) failed; deletes=%v", open.BranchDeletes)
	}
}

// TestCloseSessionAndWorktree_noBeadMetaSkipsBranchDelete covers an older
// session dispatched before pg2-5sirm's meta round-trip (or a
// non-"worktree"-isolation stray this sweep still matched by prefix alone):
// with no pgrouter.bead tag at all, deleteAnchorBranch must no-op entirely —
// "pg-router/" alone is not a real branch, and there is nothing safe to
// delete — even though RemoveWorktree itself succeeded.
func TestCloseSessionAndWorktree_noBeadMetaSkipsBranchDelete(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{}
	s := ccpool.Session{ExternalID: "pg-router-worker-zr-old", State: ccpool.StateIdle, CWD: "/wt/zr-old"}
	if !closeSessionAndWorktree(context.Background(), cc, open.Open, "/repo/root", "", s) {
		t.Fatal("closeSessionAndWorktree = false, want true")
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/wt/zr-old" {
		t.Fatalf("RemoveWorktree calls = %v, want exactly one call for /wt/zr-old", open.Removed)
	}
	if len(open.Opens) != 1 || open.Opens[0] != "/wt/zr-old" {
		t.Errorf("a missing bead tag must short-circuit before any branch-delete Open; opens=%v", open.Opens)
	}
	if len(open.BranchDeletes) != 0 {
		t.Errorf("branch must not be deleted without a bead id; deletes=%v", open.BranchDeletes)
	}
}

// leaseMeta is a session meta map carrying a supervision lease that expires at
// until (INV-CCH-18), plus the bead tag the sweep reads.
func leaseMeta(until time.Time, bead string) map[string]string {
	return map[string]string{ccpool.MetaKeyLeaseUntil: ccpool.FormatMetaTime(until), ccpool.MetaKeyBead: bead}
}

// TestCloseSinglePhase_worktreeRemoveWarnOnlyForRealFailures pins pg2-tp8rx:
// a session whose cwd is not inside any git repository (the non-git
// workspace root /Users/phillipg/phillipg_mbp that drain-pg2/drain-zr
// sessions run in) has no worktree to remove, so its teardown must not emit
// the "worktree remove failed" WARN -- the noise would mask genuine teardown
// failures. Open's real failure wraps gitclient.ErrNotARepository, so the
// fake wraps it the same way. A genuine failure (Open failing for any other
// reason, or RemoveWorktree failing for a repo-backed cwd) must still warn.
// In every shape the session still counts as purged.
func TestCloseSinglePhase_worktreeRemoveWarnOnlyForRealFailures(t *testing.T) {
	const cwd = "/Users/phillipg/phillipg_mbp"
	tests := []struct {
		name     string
		open     func(context.Context, string) (gitclient.WorktreeManager, error)
		wantWarn bool
	}{
		{
			name: "cwd not inside a git repository does not warn",
			open: func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
				return nil, fmt.Errorf("%w: %s: git rev-parse --path-format=absolute --git-common-dir: exit 128", gitclient.ErrNotARepository, dir)
			},
		},
		{
			name: "open failing for another reason still warns",
			open: func(context.Context, string) (gitclient.WorktreeManager, error) {
				return nil, errors.New("git: permission denied")
			},
			wantWarn: true,
		},
		{
			name:     "remove failing for a repo-backed cwd still warns",
			open:     (&fakeWorktreeOpener{RemoveErrAt: map[string]bool{cwd: true}}).Open,
			wantWarn: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			cc := &fakeCC{}
			s := ccpool.Session{ExternalID: "drain-pg2-pg2-x", State: ccpool.StateIdle, CWD: cwd}
			if !closeSinglePhase(context.Background(), cc, tc.open, "/repo/root", s, false) {
				t.Fatal("closeSinglePhase = false, want true: the row was purged")
			}
			if len(cc.Closed) != 1 {
				t.Errorf("session must be closed; closed=%v", cc.Closed)
			}
			got := strings.Contains(buf.String(), "level=WARN") && strings.Contains(buf.String(), "worktree remove failed")
			if got != tc.wantWarn {
				t.Errorf("worktree-remove WARN emitted = %v, want %v; log:\n%s", got, tc.wantWarn, buf.String())
			}
		})
	}
}
