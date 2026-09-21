package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/x/gitclient"
)

// fakeCC is a minimal ccpool.Runner test double — this module's own local
// reimplementation of packages/pg-router's (now-deleted) dtest.FakeCC shape
// (ListSeq/Closed/ClosedPurge), since that package is unreachable from here
// (Go's internal-package visibility rule).
type fakeCC struct {
	mu          sync.Mutex
	ListSeq     [][]ccpool.Session
	listIdx     int
	Closed      []string
	ClosedPurge []bool
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
	return nil
}

func (f *fakeCC) List(context.Context) ([]ccpool.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	mu          sync.Mutex
	OpenErrAt   map[string]bool // dir -> Open fails (not inside any git repository)
	RemoveErrAt map[string]bool // dir -> the opened manager's RemoveWorktree fails (not a linked worktree)
	Removed     []string        // every path RemoveWorktree was actually invoked with
}

func (o *fakeWorktreeOpener) Open(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
	if o.OpenErrAt[dir] {
		return nil, gitclient.ErrNotARepository
	}
	return &fakeWorktreeManager{owner: o}, nil
}

type fakeWorktreeManager struct{ owner *fakeWorktreeOpener }

var _ gitclient.WorktreeManager = (*fakeWorktreeManager)(nil)

func (m *fakeWorktreeManager) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) error {
	return nil // unused by preShutdown's teardown sweep
}

func (m *fakeWorktreeManager) RemoveWorktree(_ context.Context, path string, _ bool) error {
	m.owner.mu.Lock()
	defer m.owner.mu.Unlock()
	m.owner.Removed = append(m.owner.Removed, path)
	if m.owner.RemoveErrAt[path] {
		return errors.New("git: not a linked worktree")
	}
	return nil
}

func (m *fakeWorktreeManager) PruneWorktrees(context.Context) error { return nil }

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
	teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, "pg-router-")
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
	n := teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, "pg-router-")
	if n != 2 {
		t.Errorf("teardownAllSessions closed count = %d, want 2 (pg-router- sessions only); closed=%v", n, cc.Closed)
	}
}

// TestTeardownAllSessions_preservesNeedsInput: a pg-router session in
// needs_input is left alive (NOT closed) so the operator can still attach
// after the pass; other pg-router sessions are still reaped, and the
// returned count excludes the preserved one.
func TestTeardownAllSessions_preservesNeedsInput(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-need", Live: true, State: ccpool.StateNeedsInput},
		{ExternalID: "pg-router-worker-zr-done", Live: true, State: ccpool.StateIdle},
		{ExternalID: "cc-unrelated", Live: true, State: ccpool.StateWorking},
	}}}
	n := teardownAllSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, "pg-router-")
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
	n := teardownAllSessions(context.Background(), cc, open.Open, "pg-router-")
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
	if !closeUnlessNeedsInput(context.Background(), cc, open.Open, s) {
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
	if !closeUnlessNeedsInput(context.Background(), cc, open.Open, s) {
		t.Fatal("closeUnlessNeedsInput = false, want true: a RemoveWorktree failure must not be treated as a close failure")
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-x" {
		t.Errorf("session must still be closed; closed=%v", cc.Closed)
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/repo/root" {
		t.Errorf("RemoveWorktree must still be attempted; calls=%v", open.Removed)
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
	n := teardownAllSessions(context.Background(), cc, open.Open, "pg-router-")
	if n != 2 {
		t.Fatalf("teardownAllSessions closed count = %d, want 2 (both sessions closed despite the first's worktree removal failing); closed=%v", n, cc.Closed)
	}
	if !contains(open.Removed, "/repo/root") || !contains(open.Removed, "/wt/b") {
		t.Errorf("RemoveWorktree must have been attempted for both sessions; calls=%v", open.Removed)
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
	code := servePreShutdown(cc, (&fakeWorktreeOpener{}).Open, "pg-router-", strings.NewReader(`{"schemaVersion":"1","id":"hs-1"}`), &stdout)
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
	code := servePreShutdown(cc, (&fakeWorktreeOpener{}).Open, "pg-router-", strings.NewReader(`{"schemaVersion":"1"}`), &stdout) // missing id
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d on a schema-invalid request", code, conformance.ExitError)
	}
	if len(cc.Closed) != 0 {
		t.Errorf("a malformed request must not run the sweep; closed=%v", cc.Closed)
	}
}
