package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router/conformance"
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
// error injection on BOTH failure shapes closeUnlessNeedsInput's fail-soft
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

func (m *fakeWorktreeManager) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) error {
	return nil // unused by preShutdown's teardown sweep
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

// TestTeardownAllSessions_purges ports packages/pg-router's own (now-deleted)
// Orchestrator.teardownAll test of the same name: it closes
// prefix-matching sessions with purge=true.
func TestTeardownAllSessions_purges(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-x", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Fatalf("teardown must close only the pg-router session; closed=%v", cc.Closed)
	}
	if len(cc.ClosedPurge) != 1 || !cc.ClosedPurge[0] {
		t.Errorf("teardown must purge; closedPurge=%v", cc.ClosedPurge)
	}
}

// TestTeardownAllSessions_returnsClosedCount locks the count fed into the
// "preShutdown: teardown" log line — only prefix-matching sessions count.
func TestTeardownAllSessions_returnsClosedCount(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-a", Live: true},
		{ExternalID: "pg-router-feedback-zr-b", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	n := teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if n != 2 {
		t.Errorf("teardownAllSessions closed count = %d, want 2 (pg-router- sessions only); closed=%v", n, cc.Closed)
	}
}

// TestTeardownAllSessions_preservesNeedsInput: a pg-router session in
// needs_input with NO bead metadata (so beadAlreadyClosed fails soft — same
// as an older session dispatched before pg2-5sirm) is left alive (NOT
// closed) so the operator can still attach after the pass; other pg-router
// sessions are still reaped, and the returned count excludes the preserved
// one.
func TestTeardownAllSessions_preservesNeedsInput(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-need", Live: true, State: ccpool.StateNeedsInput},
		{ExternalID: "pg-router-worker-zr-done", Live: true, State: ccpool.StateIdle},
		{ExternalID: "cc-unrelated", Live: true, State: ccpool.StateWorking},
	}}}
	n := teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if n != 1 {
		t.Errorf("teardownAllSessions closed count = %d, want 1 (needs_input preserved, stray excluded); closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-done" {
		t.Fatalf("teardown must close the idle pg-router session only; closed=%v", cc.Closed)
	}
	if contains(cc.Closed, "pg-router-worker-zr-need") {
		t.Errorf("teardown must NOT close a needs_input session; closed=%v", cc.Closed)
	}
}

// TestTeardownAllSessions_reconcilesNeedsInputWhenBeadClosed is the sweep-level
// regression for pg2-5sirm's own zr-50s7h.2: a needs_input session tagged with
// a bead that is ALREADY closed must be reaped in the very same sweep that
// still preserves a needs_input session whose bead remains open.
func TestTeardownAllSessions_reconcilesNeedsInputWhenBeadClosed(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-review-zr-50s7h.2-20260919T025624.285144000",
			Live:       true, State: ccpool.StateNeedsInput,
			Meta: map[string]string{ccpool.MetaKeyBead: "zr-50s7h.2"},
		},
		{
			ExternalID: "pg-router-worker-zr-0t0z7.3-20260919T033857.961022000",
			Live:       true, State: ccpool.StateNeedsInput,
			Meta: map[string]string{ccpool.MetaKeyBead: "zr-0t0z7.3"},
		},
	}}}
	br := fakeBR{out: map[string]string{
		"show zr-50s7h.2 --json": `{"status":"closed"}`,
		"show zr-0t0z7.3 --json": `{"status":"open"}`,
	}}
	n := teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", "")
	if n != 1 {
		t.Fatalf("teardownAllSessions closed count = %d, want 1 (only the closed-bead session reconciled); closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-review-zr-50s7h.2-20260919T025624.285144000" {
		t.Errorf("must reconcile the closed-bead session only; closed=%v", cc.Closed)
	}
}

// TestTeardownAllSessions_removesWorktreeOfClosedSessionOnly (pg2-a8h6c
// happy path): seeding one needs_input session and one closable session,
// each with its own distinct CWD, only the closable session's CWD must be
// passed to RemoveWorktree — the needs_input session's worktree is left
// alone exactly as its ccpool session is.
func TestTeardownAllSessions_removesWorktreeOfClosedSessionOnly(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-need", Live: true, State: ccpool.StateNeedsInput, CWD: "/wt/need"},
		{ExternalID: "pg-router-worker-zr-done", Live: true, State: ccpool.StateIdle, CWD: "/wt/done"},
	}}}
	open := &fakeWorktreeOpener{}
	n := teardownAllSessions(context.Background(), cc, open.Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if n != 1 {
		t.Fatalf("teardownAllSessions closed count = %d, want 1; closed=%v", n, cc.Closed)
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/wt/done" {
		t.Errorf("RemoveWorktree calls = %v, want exactly one call for the closable session's cwd /wt/done", open.Removed)
	}
	if contains(open.Removed, "/wt/need") {
		t.Errorf("RemoveWorktree must NOT be called for the needs_input session's cwd; calls=%v", open.Removed)
	}
}

// TestCloseUnlessNeedsInput_worktreeOpenFailsSoft covers the
// non-worktree-isolation-type fail-soft path where the session's cwd is
// not inside any git repository at all (isolation type "path"/"workforest"
// — Open itself fails). The session must still count as closed, and the
// failure must not be raised as an error from closeUnlessNeedsInput.
func TestCloseUnlessNeedsInput_worktreeOpenFailsSoft(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{OpenErrAt: map[string]bool{"/scratch/not-a-repo": true}}
	s := ccpool.Session{ExternalID: "pg-router-worker-zr-x", State: ccpool.StateIdle, CWD: "/scratch/not-a-repo"}
	if !closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
		t.Fatal("closeUnlessNeedsInput = false, want true: a worktree-open failure must not be treated as a close failure")
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Errorf("session must still be closed; closed=%v", cc.Closed)
	}
	if len(open.Removed) != 0 {
		t.Errorf("RemoveWorktree must not be invoked when Open itself failed; calls=%v", open.Removed)
	}
}

// TestCloseUnlessNeedsInput_removeWorktreeFailsSoft covers the other
// fail-soft shape: cwd IS inside a git repository but is not a registered
// linked worktree of it (isolation type "none", where cwd is the repo's
// own main working tree — Open succeeds, RemoveWorktree itself fails). The
// session must still count as closed.
func TestCloseUnlessNeedsInput_removeWorktreeFailsSoft(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{RemoveErrAt: map[string]bool{"/repo/root": true}}
	s := ccpool.Session{ExternalID: "pg-router-worker-zr-x", State: ccpool.StateIdle, CWD: "/repo/root"}
	if !closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
		t.Fatal("closeUnlessNeedsInput = false, want true: a RemoveWorktree failure must not be treated as a close failure")
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Errorf("session must still be closed; closed=%v", cc.Closed)
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/repo/root" {
		t.Errorf("RemoveWorktree must still be attempted; calls=%v", open.Removed)
	}
}

// TestCloseUnlessNeedsInput_reconcilesNeedsInputWhenBeadClosed is pg2-5sirm's
// core regression: zr-50s7h.2 closed 2026-09-19 (review fully posted) while
// its own ccpool session sat in needs_input, live, for ~2.5 days because
// nothing ever revisited it. A needs_input session whose pgrouter.bead is
// already closed must now be reconciled (closed) instead of preserved
// forever.
func TestCloseUnlessNeedsInput_reconcilesNeedsInputWhenBeadClosed(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{}
	br := fakeBR{out: map[string]string{"show zr-50s7h.2 --json": `{"status":"closed"}`}}
	s := ccpool.Session{
		ExternalID: "pg-router-review-zr-50s7h.2-20260919T025624.285144000",
		State:      ccpool.StateNeedsInput,
		Meta:       map[string]string{ccpool.MetaKeyBead: "zr-50s7h.2"},
	}
	if !closeUnlessNeedsInput(context.Background(), cc, open.Open, br, "/repo/root", "", s, false) {
		t.Fatal("closeUnlessNeedsInput = false, want true: a needs_input session whose bead is already closed must be reconciled")
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != s.ExternalID {
		t.Errorf("session must be closed; closed=%v", cc.Closed)
	}
}

// TestCloseUnlessNeedsInput_preservesNeedsInputWhenBeadOpen proves the sibling
// zr-0t0z7.3 case (a genuinely open bead awaiting a real reviewer answer)
// stays preserved — reconciliation must not fire on an open bead.
func TestCloseUnlessNeedsInput_preservesNeedsInputWhenBeadOpen(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{}
	br := fakeBR{out: map[string]string{"show zr-0t0z7.3 --json": `{"status":"open"}`}}
	s := ccpool.Session{
		ExternalID: "pg-router-worker-zr-0t0z7.3-20260919T033857.961022000",
		State:      ccpool.StateNeedsInput,
		Meta:       map[string]string{ccpool.MetaKeyBead: "zr-0t0z7.3"},
	}
	if closeUnlessNeedsInput(context.Background(), cc, open.Open, br, "/repo/root", "", s, false) {
		t.Fatal("closeUnlessNeedsInput = true, want false: a needs_input session whose bead is still open must be preserved")
	}
	if len(cc.Closed) != 0 {
		t.Errorf("session must NOT be closed; closed=%v", cc.Closed)
	}
}

// TestCloseUnlessNeedsInput_preservesNeedsInputWithoutBeadMeta covers an older
// session dispatched before pg2-5sirm's meta round-trip (or a non-"worktree"
// stray this sweep still matches by prefix alone): with no pgrouter.bead tag
// at all, beadAlreadyClosed must fail soft (preserve) rather than guess.
func TestCloseUnlessNeedsInput_preservesNeedsInputWithoutBeadMeta(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{}
	s := ccpool.Session{ExternalID: "pg-router-worker-zr-old", State: ccpool.StateNeedsInput}
	if closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
		t.Fatal("closeUnlessNeedsInput = true, want false: a needs_input session with no bead metadata must be preserved")
	}
	if len(cc.Closed) != 0 {
		t.Errorf("session must NOT be closed; closed=%v", cc.Closed)
	}
}

// TestCloseUnlessNeedsInput_preservesNeedsInputOnBeadLookupError proves a bd
// outage fails soft (preserve) rather than risk purging a session an
// operator still needs to attach to just because bd could not be reached.
func TestCloseUnlessNeedsInput_preservesNeedsInputOnBeadLookupError(t *testing.T) {
	cc := &fakeCC{}
	open := &fakeWorktreeOpener{}
	br := fakeBR{err: errors.New("bd down")}
	s := ccpool.Session{
		ExternalID: "pg-router-worker-zr-x",
		State:      ccpool.StateNeedsInput,
		Meta:       map[string]string{ccpool.MetaKeyBead: "zr-x"},
	}
	if closeUnlessNeedsInput(context.Background(), cc, open.Open, br, "/repo/root", "", s, false) {
		t.Fatal("closeUnlessNeedsInput = true, want false: a bd lookup failure must fail soft and preserve the session")
	}
	if len(cc.Closed) != 0 {
		t.Errorf("session must NOT be closed; closed=%v", cc.Closed)
	}
}

// TestTeardownAllSessions_worktreeFailureDoesNotAbortSweep proves a
// worktree-removal failure on one session never stops the sweep from
// closing/removing subsequent sessions.
func TestTeardownAllSessions_worktreeFailureDoesNotAbortSweep(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-a", Live: true, State: ccpool.StateIdle, CWD: "/repo/root"},
		{ExternalID: "pg-router-worker-zr-b", Live: true, State: ccpool.StateIdle, CWD: "/wt/b"},
	}}}
	open := &fakeWorktreeOpener{RemoveErrAt: map[string]bool{"/repo/root": true}}
	n := teardownAllSessions(context.Background(), cc, open.Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if n != 2 {
		t.Fatalf("teardownAllSessions closed count = %d, want 2 (both sessions closed despite the first's worktree removal failing); closed=%v", n, cc.Closed)
	}
	if !contains(open.Removed, "/repo/root") || !contains(open.Removed, "/wt/b") {
		t.Errorf("RemoveWorktree must have been attempted for both sessions; calls=%v", open.Removed)
	}
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

// TestTeardownAllSessions_sparesActiveSessions is pg2-hwt7v's core
// regression (operator ruling, Phillip, 2026-09-30): at daemon shutdown a
// session that is actively working (starting / ready / working) MUST be
// spared ENTIRELY -- no cc.Close, no worktree removal, no anchor-branch
// delete -- even when its bead has already closed, while a closable session
// (idle) in the same sweep is still purged with its worktree and branch.
func TestTeardownAllSessions_sparesActiveSessions(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking, CWD: "/wt/zr-w", Meta: map[string]string{ccpool.MetaKeyBead: "zr-w"}},
		{ExternalID: "pg-router-worker-zr-s", Live: true, State: ccpool.StateStarting, CWD: "/wt/zr-s", Meta: map[string]string{ccpool.MetaKeyBead: "zr-s"}},
		{ExternalID: "pg-router-worker-zr-r", Live: true, State: ccpool.StateReady, CWD: "/wt/zr-r", Meta: map[string]string{ccpool.MetaKeyBead: "zr-r"}},
		{ExternalID: "pg-router-worker-zr-i", Live: true, State: ccpool.StateIdle, CWD: "/wt/zr-i", Meta: map[string]string{ccpool.MetaKeyBead: "zr-i"}},
	}}}
	// Every bead is closed: a working session must still be spared (its
	// bead closing mid-turn does not make it closable at shutdown).
	br := fakeBR{out: map[string]string{
		"show zr-w --json": `{"status":"closed"}`,
		"show zr-s --json": `{"status":"closed"}`,
		"show zr-r --json": `{"status":"closed"}`,
		"show zr-i --json": `{"status":"closed"}`,
	}}
	open := &fakeWorktreeOpener{}
	n := teardownAllSessions(context.Background(), cc, open.Open, br, "pg-router-", "/repo/root", "")
	if n != 1 {
		t.Errorf("teardownAllSessions closed count = %d, want 1 (only the idle session); closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-i" {
		t.Fatalf("only the idle session may be closed; closed=%v", cc.Closed)
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/wt/zr-i" {
		t.Errorf("RemoveWorktree must run for the idle session's cwd ONLY; calls=%v", open.Removed)
	}
	if len(open.BranchDeletes) != 1 || open.BranchDeletes[0].Branch != "pg-router/zr-i" {
		t.Errorf("only the purged session's anchor branch may be deleted; deletes=%v", open.BranchDeletes)
	}
}

// TestCloseUnlessNeedsInput_sparesActiveStates covers each actively-working
// state individually through the per-session decision: spared means returns
// false with no close and no worktree open at all.
func TestCloseUnlessNeedsInput_sparesActiveStates(t *testing.T) {
	for _, st := range []ccpool.SessionState{ccpool.StateStarting, ccpool.StateReady, ccpool.StateWorking} {
		t.Run(string(st), func(t *testing.T) {
			cc := &fakeCC{}
			open := &fakeWorktreeOpener{}
			s := ccpool.Session{ExternalID: "pg-router-worker-zr-x", State: st, CWD: "/wt/zr-x", Meta: map[string]string{ccpool.MetaKeyBead: "zr-x"}}
			if closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
				t.Fatalf("closeUnlessNeedsInput = true, want false: a %s session must be spared at shutdown", st)
			}
			if len(cc.Closed) != 0 {
				t.Errorf("session must NOT be closed; closed=%v", cc.Closed)
			}
			if len(open.Opens) != 0 || len(open.Removed) != 0 || len(open.BranchDeletes) != 0 {
				t.Errorf("worktree must be untouched; opens=%v removed=%v deletes=%v", open.Opens, open.Removed, open.BranchDeletes)
			}
		})
	}
}

// TestCloseUnlessNeedsInput_stillPurgesErroredAndIdle pins the other side of
// the ruling: only ACTIVE sessions are spared. errored and idle sessions
// (turn ended) remain closable at shutdown, worktree included.
func TestCloseUnlessNeedsInput_stillPurgesErroredAndIdle(t *testing.T) {
	for _, st := range []ccpool.SessionState{ccpool.StateIdle, ccpool.StateErrored} {
		t.Run(string(st), func(t *testing.T) {
			cc := &fakeCC{}
			open := &fakeWorktreeOpener{}
			s := ccpool.Session{ExternalID: "pg-router-worker-zr-x", State: st, CWD: "/wt/zr-x"}
			if !closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
				t.Fatalf("closeUnlessNeedsInput = false, want true: a %s session is closable at shutdown", st)
			}
			if len(open.Removed) != 1 || open.Removed[0] != "/wt/zr-x" {
				t.Errorf("worktree must be removed; calls=%v", open.Removed)
			}
		})
	}
}

// TestTeardownAllSessions_keepsWorktreeSharedWithSparedPeer: a per-bead
// worktree is keyed by bead id alone, so a purged idle review session can
// share its cwd with a spared working feedback session of the same bead.
// The idle session is still purged, but the shared worktree and anchor
// branch MUST stay (pg2-u3t04 / pg2-aqpqx semantics, applied at shutdown).
func TestTeardownAllSessions_keepsWorktreeSharedWithSparedPeer(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-review-zr-x", Live: true, State: ccpool.StateIdle, CWD: "/wt/zr-x", Meta: map[string]string{ccpool.MetaKeyBead: "zr-x"}},
		{ExternalID: "pg-router-feedback-zr-x", Live: true, State: ccpool.StateWorking, CWD: "/wt/zr-x", Meta: map[string]string{ccpool.MetaKeyBead: "zr-x"}},
	}}}
	open := &fakeWorktreeOpener{}
	n := teardownAllSessions(context.Background(), cc, open.Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if n != 1 || len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-review-zr-x" {
		t.Fatalf("only the idle session may be closed; n=%d closed=%v", n, cc.Closed)
	}
	if len(open.Removed) != 0 || len(open.BranchDeletes) != 0 {
		t.Errorf("a worktree shared with a spared session must be kept; removed=%v deletes=%v", open.Removed, open.BranchDeletes)
	}
}

// TestServePreShutdown_success proves the wire contract end to end against
// a fake ccpool.Runner: a schema-legal request gets a
// handler.preShutdown-reply back, exit 0, and the sweep actually ran.
func TestServePreShutdown_success(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-x", Live: true},
	}}}
	var stdout bytes.Buffer
	code := servePreShutdown(cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", "", strings.NewReader(`{"schemaVersion":"1","id":"hs-1"}`), &stdout)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitOK)
	}
	var reply map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not JSON: %v; got %s", err, stdout.String())
	}
	if reply["id"] != "hs-1" || reply["outcome"] != "ok" {
		t.Errorf("reply = %+v, want id=hs-1 outcome=ok", reply)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Errorf("servePreShutdown must run the sweep; closed=%v", cc.Closed)
	}
}

// TestServePreShutdown_rejectsMalformedRequest proves the schema check runs
// BEFORE the sweep — a malformed request must not touch ccpool at all.
func TestServePreShutdown_rejectsMalformedRequest(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-x", Live: true}}}}
	var stdout bytes.Buffer
	code := servePreShutdown(cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", "", strings.NewReader(`{"schemaVersion":"1"}`), &stdout) // missing id
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d on a schema-invalid request", code, conformance.ExitError)
	}
	if len(cc.Closed) != 0 {
		t.Errorf("a malformed request must not run the sweep; closed=%v", cc.Closed)
	}
}

// leaseMeta is a session meta map carrying a supervision lease that expires at
// until (INV-CCH-18), plus the bead tag the sweep reads.
func leaseMeta(until time.Time, bead string) map[string]string {
	return map[string]string{ccpool.MetaKeyLeaseUntil: ccpool.FormatMetaTime(until), ccpool.MetaKeyBead: bead}
}

// A ccpool-backed dispatch outlives the daemon that started it (ADR 0085), so
// an OPEN session whose supervision lease has not expired is in the hands of a
// running handler. That holds whatever its state: an idle session whose handler
// is still in its settle step must not be closed out from under it.
func TestCloseUnlessNeedsInput_sparesSessionSupervisedByLiveHandler(t *testing.T) {
	for _, st := range []ccpool.SessionState{ccpool.StateIdle, ccpool.StateErrored} {
		t.Run(string(st), func(t *testing.T) {
			cc := &fakeCC{}
			open := &fakeWorktreeOpener{}
			s := ccpool.Session{
				ExternalID: "pg-router-review-zr-x", Live: true, State: st, CWD: "/wt/zr-x",
				Meta: leaseMeta(time.Now().Add(time.Minute), "zr-x"),
			}
			if closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
				t.Fatalf("closeUnlessNeedsInput = true, want false: a %s session with a live lease is supervised", st)
			}
			if len(cc.Closed) != 0 {
				t.Errorf("session must NOT be closed; closed=%v", cc.Closed)
			}
			if len(open.Opens) != 0 || len(open.Removed) != 0 || len(open.BranchDeletes) != 0 {
				t.Errorf("worktree must be untouched; opens=%v removed=%v deletes=%v", open.Opens, open.Removed, open.BranchDeletes)
			}
		})
	}
}

// The other side: a lease that has expired (its handler is gone), no lease at
// all (an older build), and a row that is already closed are each NOT spared by
// the lease rule, so an idle one is still purged with its worktree.
func TestCloseUnlessNeedsInput_leaseRuleDoesNotSpareUnsupervisedOrClosedRows(t *testing.T) {
	cases := map[string]ccpool.Session{
		"expired lease": {
			ExternalID: "pg-router-review-zr-x", State: ccpool.StateIdle, CWD: "/wt/zr-x",
			Meta: leaseMeta(time.Now().Add(-time.Minute), "zr-x"),
		},
		"no lease": {
			ExternalID: "pg-router-review-zr-x", State: ccpool.StateIdle, CWD: "/wt/zr-x",
			Meta: map[string]string{ccpool.MetaKeyBead: "zr-x"},
		},
		"closed row with an unexpired lease": {
			ExternalID: "pg-router-review-zr-x", State: ccpool.StateIdle, CWD: "/wt/zr-x",
			CloseReason: "handler", Meta: leaseMeta(time.Now().Add(time.Minute), "zr-x"),
		},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			cc := &fakeCC{}
			open := &fakeWorktreeOpener{}
			if !closeUnlessNeedsInput(context.Background(), cc, open.Open, fakeBR{}, "/repo/root", "", s, false) {
				t.Fatalf("closeUnlessNeedsInput = false, want true: %s must not be spared", name)
			}
			if len(open.Removed) != 1 || open.Removed[0] != "/wt/zr-x" {
				t.Errorf("worktree must be removed; calls=%v", open.Removed)
			}
		})
	}
}

// A per-bead worktree is shared by every role's session for the bead, so a
// lease-supervised idle peer protects the worktree of a session that is purged,
// exactly as a working peer does.
func TestTeardownAllSessions_keepsWorktreeSharedWithLeaseSupervisedPeer(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-review-zr-x", State: ccpool.StateIdle, CWD: "/wt/zr-x", Meta: map[string]string{ccpool.MetaKeyBead: "zr-x"}},
		{ExternalID: "pg-router-feedback-zr-x", Live: true, State: ccpool.StateIdle, CWD: "/wt/zr-x", Meta: leaseMeta(time.Now().Add(time.Minute), "zr-x")},
	}}}
	open := &fakeWorktreeOpener{}
	n := teardownAllSessions(context.Background(), cc, open.Open, fakeBR{}, "pg-router-", "/repo/root", "")
	if n != 1 || len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-review-zr-x" {
		t.Fatalf("only the unsupervised idle session may be closed; n=%d closed=%v", n, cc.Closed)
	}
	if len(open.Removed) != 0 || len(open.BranchDeletes) != 0 {
		t.Errorf("a worktree shared with a supervised session must be kept; removed=%v deletes=%v", open.Removed, open.BranchDeletes)
	}
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
