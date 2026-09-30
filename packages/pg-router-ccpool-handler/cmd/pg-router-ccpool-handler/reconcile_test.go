package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
)

// TestReconcilableState covers every ccpool.SessionState against
// reconcilableState's own decision — only StateIdle/StateNeedsInput qualify
// (pg2-hrppg's Ask: "idle/needs_input (not actively working)"); every other
// state, including StateWorking, must not.
func TestReconcilableState(t *testing.T) {
	cases := []struct {
		state ccpool.SessionState
		want  bool
	}{
		{ccpool.StateIdle, true},
		{ccpool.StateNeedsInput, true},
		{ccpool.StateWorking, false},
		{ccpool.StateStarting, false},
		{ccpool.StateReady, false},
		{ccpool.StateErrored, false},
	}
	for _, c := range cases {
		if got := reconcilableState(c.state); got != c.want {
			t.Errorf("reconcilableState(%q) = %v, want %v", c.state, got, c.want)
		}
	}
}

// TestReconcileClosedBeadSessions_closesIdleWithClosedBead is pg2-hrppg's
// core regression: an idle session whose bead has already closed must be
// purged by this periodic sweep, not left to leak until the next shutdown.
func TestReconcileClosedBeadSessions_closesIdleWithClosedBead(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-worker-zr-done",
			State:      ccpool.StateIdle,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-done"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 1 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 1; closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-done" {
		t.Errorf("must close the idle closed-bead session; closed=%v", cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_closesNeedsInputWithClosedBead mirrors
// zr-50s7h.2's own incident shape (needs_input, not idle).
func TestReconcileClosedBeadSessions_closesNeedsInputWithClosedBead(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-review-zr-50s7h.2-20260919T025624.285144000",
			State:      ccpool.StateNeedsInput,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-50s7h.2"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-50s7h.2 --json": `{"status":"closed"}`}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 1 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 1; closed=%v", n, cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_leavesWorkingSessionAlone proves a
// StateWorking session is left untouched regardless of its bead's status —
// it is a live in-flight dispatch, never an orphan, per pg2-hrppg's Ask.
func TestReconcileClosedBeadSessions_leavesWorkingSessionAlone(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-worker-zr-working",
			State:      ccpool.StateWorking,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-working"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-working --json": `{"status":"closed"}`}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 0 || len(cc.Closed) != 0 {
		t.Fatalf("reconcileClosedBeadSessions must not touch a working session; n=%d closed=%v", n, cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_leavesOpenBeadSessionAlone proves an idle
// session whose bead is still open is left alone — reconciliation only ever
// fires on an unambiguously closed bead.
func TestReconcileClosedBeadSessions_leavesOpenBeadSessionAlone(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-worker-zr-open",
			State:      ccpool.StateIdle,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-open"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-open --json": `{"status":"open"}`}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 0 || len(cc.Closed) != 0 {
		t.Fatalf("reconcileClosedBeadSessions must not touch an open-bead session; n=%d closed=%v", n, cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_leavesUntaggedSessionAlone covers a session
// with no pgrouter.bead metadata at all — beadAlreadyClosed fails closed
// (preserve), matching closeUnlessNeedsInput's own posture.
func TestReconcileClosedBeadSessions_leavesUntaggedSessionAlone(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-untagged", State: ccpool.StateIdle},
	}}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", nil)
	if n != 0 || len(cc.Closed) != 0 {
		t.Fatalf("reconcileClosedBeadSessions must not touch an untagged session; n=%d closed=%v", n, cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_leavesSessionAloneOnBdLookupError proves a
// bd outage fails soft (preserve) rather than risk purging a session because
// bd could not be reached.
func TestReconcileClosedBeadSessions_leavesSessionAloneOnBdLookupError(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-worker-zr-x",
			State:      ccpool.StateNeedsInput,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-x"},
		},
	}}}
	br := fakeBR{err: errors.New("bd down")}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 0 || len(cc.Closed) != 0 {
		t.Fatalf("reconcileClosedBeadSessions must fail soft on a bd lookup error; n=%d closed=%v", n, cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_ignoresOtherStatesEvenWithClosedBead covers
// starting/ready/errored — none of these qualify as reconcilable even when
// the bead has closed, since this sweep runs continuously while the daemon
// is up and must never race a session that might still be doing something.
func TestReconcileClosedBeadSessions_ignoresOtherStatesEvenWithClosedBead(t *testing.T) {
	for _, state := range []ccpool.SessionState{ccpool.StateStarting, ccpool.StateReady, ccpool.StateErrored} {
		cc := &fakeCC{ListSeq: [][]ccpool.Session{{
			{
				ExternalID: "pg-router-worker-zr-" + string(state),
				State:      state,
				Meta:       map[string]string{ccpool.MetaKeyBead: "zr-" + string(state)},
			},
		}}}
		br := fakeBR{out: map[string]string{"show zr-" + string(state) + " --json": `{"status":"closed"}`}}
		n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
		if n != 0 || len(cc.Closed) != 0 {
			t.Fatalf("state %q: reconcileClosedBeadSessions must not touch it; n=%d closed=%v", state, n, cc.Closed)
		}
	}
}

// TestReconcileClosedBeadSessions_ignoresNonMatchingPrefix proves a session
// outside SessionPrefix is left alone even if idle with a closed bead.
func TestReconcileClosedBeadSessions_ignoresNonMatchingPrefix(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "cc-unrelated",
			State:      ccpool.StateIdle,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-done"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 0 || len(cc.Closed) != 0 {
		t.Fatalf("reconcileClosedBeadSessions must not touch a non-prefix-matching session; n=%d closed=%v", n, cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_removesWorktreeOfClosedSession proves the
// closed session's own worktree is best-effort removed via
// closeSessionAndWorktree, mirroring teardownAllSessions's own behavior.
func TestReconcileClosedBeadSessions_removesWorktreeOfClosedSession(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-worker-zr-done",
			State:      ccpool.StateIdle,
			CWD:        "/wt/done",
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-done"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	open := &fakeWorktreeOpener{}
	n := reconcileClosedBeadSessions(context.Background(), cc, open.Open, br, "pg-router-", "/repo/root", nil)
	if n != 1 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 1", n)
	}
	if len(open.Removed) != 1 || open.Removed[0] != "/wt/done" {
		t.Errorf("RemoveWorktree calls = %v, want exactly one call for /wt/done", open.Removed)
	}
}

// TestReconcileClosedBeadSessions_deletesAnchorBranch proves repoRoot is
// threaded all the way through to closeSessionAndWorktree's own
// branch-delete step (bead pg2-ci75j via pg2-tpa18) from THIS call site too
// — not just teardownAllSessions's — since closeSessionAndWorktree is
// shared verbatim between preshutdown.go's shutdown-time sweep and this
// periodic reconciliation.
func TestReconcileClosedBeadSessions_deletesAnchorBranch(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-worker-zr-done",
			State:      ccpool.StateIdle,
			CWD:        "/wt/zr-done",
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-done"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	open := &fakeWorktreeOpener{}
	n := reconcileClosedBeadSessions(context.Background(), cc, open.Open, br, "pg-router-", "/repo/root", nil)
	if n != 1 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 1", n)
	}
	if len(open.BranchDeletes) != 1 || open.BranchDeletes[0] != (branchDelete{Branch: "pg-router/zr-done", Force: true}) {
		t.Fatalf("DeleteBranch calls = %v, want exactly one [pg-router/zr-done force=true]", open.BranchDeletes)
	}
	if len(open.Opens) != 2 || open.Opens[0] != "/wt/zr-done" || open.Opens[1] != "/repo/root" {
		t.Errorf("expected Open(cwd) then Open(repoRoot), got %v", open.Opens)
	}
}

// TestReconcileClosedBeadSessions_mixedSweep proves one sweep across several
// sessions closes only the reconcilable ones, leaving the rest untouched —
// the multi-row shape a real `ccpool list --all` reply has.
func TestReconcileClosedBeadSessions_mixedSweep(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-a-zr-1", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-1"}},       // closed bead -> close
		{ExternalID: "pg-router-b-zr-2", State: ccpool.StateNeedsInput, Meta: map[string]string{ccpool.MetaKeyBead: "zr-2"}}, // open bead -> preserve
		{ExternalID: "pg-router-c-zr-3", State: ccpool.StateWorking, Meta: map[string]string{ccpool.MetaKeyBead: "zr-3"}},    // working -> preserve regardless
		{ExternalID: "cc-unrelated", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-1"}},           // wrong prefix -> preserve
	}}}
	br := fakeBR{out: map[string]string{
		"show zr-1 --json": `{"status":"closed"}`,
		"show zr-2 --json": `{"status":"open"}`,
		"show zr-3 --json": `{"status":"closed"}`,
	}}
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", nil)
	if n != 1 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 1; closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-a-zr-1" {
		t.Errorf("must close exactly pg-router-a-zr-1; closed=%v", cc.Closed)
	}
}

// TestReconcileClosedBeadSessions_listFailureIsSoft proves a `ccpool list`
// failure fails soft (zero closed, no panic) rather than aborting the
// caller's own query reply.
func TestReconcileClosedBeadSessions_listFailureIsSoft(t *testing.T) {
	cc := &fakeCC{}
	cc.listErr = errors.New("ccpool down")
	n := reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, fakeBR{}, "pg-router-", "/repo/root", nil)
	if n != 0 {
		t.Fatalf("reconcileClosedBeadSessions = %d, want 0 on a list failure", n)
	}
}

// TestReconcileClosedBeadSessions_keepsWorktreeSharedWithLivePeer is the
// pg2-u3t04 / pg2-aqpqx regression: two sessions (e.g. review + feedback)
// share one per-bead worktree. The idle one's bead is closed, so it is
// purged, but the worktree must survive while the peer is still working.
func TestReconcileClosedBeadSessions_keepsWorktreeSharedWithLivePeer(t *testing.T) {
	const cwd = "/wt/zr-shared"
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-review-zr-shared",
			State:      ccpool.StateIdle,
			Live:       true,
			CWD:        cwd,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-shared"},
		},
		{
			ExternalID: "pg-router-feedback-zr-shared",
			State:      ccpool.StateWorking,
			Live:       true,
			CWD:        cwd,
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-shared"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-shared --json": `{"status":"closed"}`}}
	wo := &fakeWorktreeOpener{}
	n := reconcileClosedBeadSessions(context.Background(), cc, wo.Open, br, "pg-router-", "/repo/root", nil)
	if n != 1 || len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-review-zr-shared" {
		t.Fatalf("idle session must still be purged; n=%d closed=%v", n, cc.Closed)
	}
	if len(wo.Removed) != 0 {
		t.Errorf("worktree shared with a working peer must not be removed; removed=%v", wo.Removed)
	}
	if len(wo.BranchDeletes) != 0 {
		t.Errorf("anchor branch must be kept too; deletes=%v", wo.BranchDeletes)
	}
}

// TestReconcileClosedBeadSessions_removesWorktreeWhenNoLivePeer guards the
// other direction: a sole idle session's worktree is still reclaimed.
func TestReconcileClosedBeadSessions_removesWorktreeWhenNoLivePeer(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{
			ExternalID: "pg-router-review-zr-solo",
			State:      ccpool.StateIdle,
			Live:       true,
			CWD:        "/wt/zr-solo",
			Meta:       map[string]string{ccpool.MetaKeyBead: "zr-solo"},
		},
	}}}
	br := fakeBR{out: map[string]string{"show zr-solo --json": `{"status":"closed"}`}}
	wo := &fakeWorktreeOpener{}
	reconcileClosedBeadSessions(context.Background(), cc, wo.Open, br, "pg-router-", "/repo/root", nil)
	if len(wo.Removed) != 1 || wo.Removed[0] != "/wt/zr-solo" {
		t.Errorf("sole session's worktree must be removed; removed=%v", wo.Removed)
	}
}

// TestReconcileClosedBeadSessions_defersRecentTranscriptActivity is
// pg2-03icc item 1: an idle closed-bead session whose transcript (or
// subagent transcripts) is still being written must be left alone this sweep.
func TestReconcileClosedBeadSessions_defersRecentTranscriptActivity(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	mk := func() (*fakeCC, fakeBR) {
		return &fakeCC{ListSeq: [][]ccpool.Session{{{
				ExternalID:     "pg-router-worker-zr-done",
				State:          ccpool.StateIdle,
				TranscriptPath: "/t/x.jsonl",
				Meta:           map[string]string{ccpool.MetaKeyBead: "zr-done"},
			}}}},
			fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	}
	run := func(age time.Duration, ok bool) int {
		cc, br := mk()
		latest := func(string) (time.Time, bool) { return now.Add(-age), ok }
		q := newTranscriptQuietCheck(2*time.Minute, latest, func() time.Time { return now })
		return reconcileClosedBeadSessions(context.Background(), cc, (&fakeWorktreeOpener{}).Open, br, "pg-router-", "/repo/root", q)
	}
	if n := run(30*time.Second, true); n != 0 {
		t.Fatalf("recent transcript activity must defer the purge; closed=%d", n)
	}
	if n := run(5*time.Minute, true); n != 1 {
		t.Fatalf("quiet transcript must be purged; closed=%d", n)
	}
	if n := run(0, false); n != 1 {
		t.Fatalf("unobservable transcript must proceed; closed=%d", n)
	}
	if q := newTranscriptQuietCheck(0, nil, nil); q != nil {
		t.Fatal("window <= 0 must disable the guard")
	}
}
