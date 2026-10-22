package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/x/gitclient"
)

// Tests for INV-CCH-34 and INV-CCH-38 as the ccpool executor realizes them: a
// failure before ccpool new returns ready is a pre-accept launch-failed decline
// (purge, reclaim, no bead mutation); the handler's own cancelled context and a
// non-low-disk isolation failure are NOT declines; the nudge after Ensure sends
// only to a live session.

const fakeBearer = "Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"

func TestRun_launchFailed_declinesWithRedactedTail_purgesReclaimsMutatesNothing(t *testing.T) {
	cc := &dtest.FakeCC{EnsureErr: fmt.Errorf("ccpool new: did not reach ready (state=starting): %s", fakeBearer)}
	h := newAbandonedHarness(t, cc, workerRole)
	bd := h.run.deps.BD.(*dtest.ScriptBD)

	res, err := h.run.run(context.Background(), DispatchContext{Role: h.role, Item: item.Item{ID: "zr-w"}})

	if !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("err = %v, want errors.Is(err, ErrLaunchFailed)", err)
	}
	var lf *LaunchFailedError
	if !errors.As(err, &lf) {
		t.Fatalf("err = %T, want *LaunchFailedError", err)
	}
	if !strings.Contains(lf.Tail, "did not reach ready") {
		t.Errorf("Tail = %q, want the ccpool new error text", lf.Tail)
	}
	if strings.Contains(lf.Tail, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("Tail must be redacted, got %q", lf.Tail)
	}
	if !errors.Is(err, lf.Err) || !strings.Contains(lf.Err.Error(), "ensure "+abandonedExt) {
		t.Errorf("the underlying launch error must be reachable: %v", lf.Err)
	}
	if errors.Is(err, ErrPoolAtCapacity) {
		t.Errorf("a launch failure is not at-capacity: %v", err)
	}
	if len(res.Actions) != 0 {
		t.Errorf("a decline reports no action, got %+v", res.Actions)
	}
	if len(cc.Closed) != 1 || !cc.ClosedPurge[0] {
		t.Errorf("the abandoned session must be purge-closed once; Closed=%v purge=%v", cc.Closed, cc.ClosedPurge)
	}
	if !h.reclaimed() {
		t.Errorf("the worktree and anchor branch must be reclaimed; calls=%v", h.opener.WTM.Calls)
	}
	if len(bd.Updates) != 0 || len(bd.Comments) != 0 {
		t.Errorf("a decline mutates no bead (no label, no human, no comment); updates=%v comments=%v", bd.Updates, bd.Comments)
	}
	if len(cc.Sent) != 0 {
		t.Errorf("nothing may be sent to a session that never reached ready; Sent=%v", cc.Sent)
	}
}

func TestLaunchFailedError_isUnwrapAndIs(t *testing.T) {
	inner := errors.New("boom")
	e := &LaunchFailedError{Tail: "t", Err: inner}
	if !errors.Is(e, ErrLaunchFailed) || !errors.Is(e, inner) {
		t.Fatalf("must match both ErrLaunchFailed and the underlying error: %v", e)
	}
	if errors.Is(e, ErrPoolUnreadable) || errors.Is(e, ErrBusy) {
		t.Fatalf("must not match another decline sentinel: %v", e)
	}
	if !strings.Contains(e.Error(), "boom") {
		t.Errorf("Error() = %q, want the underlying text", e.Error())
	}
}

// A cancelled handler context (shutdown) is not a decline: core checks busy
// before shutdown in Offer, so a decline here would be counted as a fault. The
// error returns as a plain failure (still purging and reclaiming), with no
// escalation either.
func TestRun_cancelledContext_doesNotDecline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &dtest.FakeCC{}
	h := newAbandonedHarness(t, cancelOnEnsureCC{FakeCC: fake, cancel: cancel}, workerRole)
	bd := h.run.deps.BD.(*dtest.ScriptBD)

	res, err := h.run.run(ctx, DispatchContext{Role: h.role, Item: item.Item{ID: "zr-w"}})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("a cancelled handler context must not decline: %v", err)
	}
	if len(res.Actions) != 0 || len(bd.Updates) != 0 {
		t.Errorf("no escalation or bead write on a cancelled context; res=%+v updates=%v", res, bd.Updates)
	}
	if !h.reclaimed() {
		t.Errorf("the abandoned worktree is still reclaimed; calls=%v", h.opener.WTM.Calls)
	}
}

// failingWTM is a WorktreeManager whose worktree add fails with an ordinary
// (not disk-full) git error: a per-bead worktree defect.
type failingWTM struct{ lowDiskWTM }

func (failingWTM) CreateWorktree(context.Context, string, string, gitclient.CreateWorktreeOptions) (*gitclient.Handle, error) {
	return nil, &gitclient.GitError{ExitCode: 128, Stderr: "fatal: a branch named 'pg-router/zr-w' already exists"}
}

// A non-low-disk isolation failure (before any session exists) is NOT a decline:
// it is an accepted failure that writes no label and escalates nothing.
func TestRun_isolationFailure_isAcceptedFailureWithNoLabel(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":["pool-launch-fail"]}`}}
	cc := &dtest.FakeCC{}
	e := newExec(cc, bd, cfg)
	e.deps.GitOpener = func(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
		if dir == cfg.WorktreeDir+"/zr-w" {
			return nil, gitclient.ErrNotARepository
		}
		return failingWTM{}, nil
	}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}

	res, err := e.run(context.Background(), d)

	if err == nil || !strings.Contains(err.Error(), "isolation zr-w") {
		t.Fatalf("err = %v, want an isolation failure", err)
	}
	if errors.Is(err, ErrLaunchFailed) || errors.Is(err, ErrLowDisk) {
		t.Fatalf("an ordinary isolation failure is neither a launch-failed nor a low-disk decline: %v", err)
	}
	if len(res.Actions) != 0 || len(bd.Updates) != 0 {
		t.Errorf("no escalation, label or bead write; res=%+v updates=%v", res, bd.Updates)
	}
	if len(cc.Ensured) != 0 {
		t.Error("no session may be launched when isolation failed")
	}
}

// The nudge after Ensure sends only to a live session. A session that is not
// live (its row says so, or it vanished) takes the post-ready failure path: the
// session is closed, the bead unclaimed and the worktree reclaimed, and it
// stays an accepted dispatch, never a launch-failed decline.
func TestRun_nudge_notLiveSession_takesPostReadyPath(t *testing.T) {
	for name, list := range map[string][]ccpool.Session{
		"row not live": {{ExternalID: abandonedExt, Live: false, State: ccpool.StateReady}},
		"row absent":   {},
	} {
		t.Run(name, func(t *testing.T) {
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{list}}
			h := newAbandonedHarness(t, cc, workerRole)
			bd := h.run.deps.BD.(*dtest.ScriptBD)

			res, err := h.run.run(context.Background(), DispatchContext{Role: h.role, Item: item.Item{ID: "zr-w"}})

			if err == nil || errors.Is(err, ErrLaunchFailed) {
				t.Fatalf("err = %v, want a plain post-ready failure (accepted), not a decline", err)
			}
			if len(cc.Sent) != 0 {
				t.Fatalf("a not-live session must not be sent to; Sent=%v", cc.Sent)
			}
			if v := verbOf(res); v != report.Unclaimed {
				t.Errorf("verb = %q, want Unclaimed", v)
			}
			if !dtest.HasUpdate(bd, "update zr-w --status=open --assignee=") {
				t.Errorf("the bead must be unclaimed; updates=%v", bd.Updates)
			}
			if len(cc.Closed) != 1 || cc.ClosedPurge[0] {
				t.Errorf("the session must be closed once, without purge; Closed=%v purge=%v", cc.Closed, cc.ClosedPurge)
			}
		})
	}
}

// A ready, live session is sent the nudge and the dispatch is accepted.
func TestRun_nudge_liveSession_isSent(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	if _, err := dispatchWorker(t, cc, bd, cfg, "pg-router-worker-zr-w"); err != nil {
		t.Fatalf("a ready launch is accepted: %v", err)
	}
	if len(cc.Sent) != 1 {
		t.Fatalf("the nudge must be sent to the live session once; Sent=%v", cc.Sent)
	}
}
