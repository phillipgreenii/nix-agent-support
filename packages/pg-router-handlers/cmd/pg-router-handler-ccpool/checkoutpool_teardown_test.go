package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/checkoutpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// Pooled-checkout teardown: a row carrying a pg-checkout-pool lease has the
// lease released to the pool where a worktree would be removed, and is purged
// only once the pool has the checkout back.

const (
	poolLeaseID  = "lease-7"
	poolLeaseCWD = "/pool/slots/slot-01/tree"
)

type recPool struct {
	mu       sync.Mutex
	log      *callLog
	released []string
	err      error
}

func (p *recPool) Lease(context.Context, checkoutpool.LeaseRequest) (checkoutpool.Lease, error) {
	return checkoutpool.Lease{}, errors.New("unexpected lease")
}
func (p *recPool) Renew(context.Context, string) error { return nil }
func (p *recPool) Release(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = append(p.released, id)
	p.log.add("Release:" + id)
	return p.err
}

// withTeardownPool installs a recording pool client for the teardown seam.
func withTeardownPool(t *testing.T, log *callLog, err error) *recPool {
	t.Helper()
	p := &recPool{log: log, err: err}
	old := teardownPoolClient
	teardownPoolClient = func(string) checkoutpool.Client { return p }
	t.Cleanup(func() { teardownPoolClient = old })
	return p
}

func leasedSession() ccpool.Session {
	return ccpool.Session{
		ExternalID: tpID, State: ccpool.StateIdle, CWD: poolLeaseCWD,
		Meta: map[string]string{ccpool.MetaKeyBead: tpBead, ccpool.MetaKeyCheckoutLease: poolLeaseID},
	}
}

// Order: SetMeta(purge_pending) -> Close(false) -> Release -> Close(true), and
// no worktree is opened or removed.
func TestCloseSession_checkoutLeaseTwoPhaseReleasesThenPurges(t *testing.T) {
	e := newTPEnv(t)
	withTeardownPool(t, e.log, nil)
	if !closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, leasedSession(), false) {
		t.Fatal("closeSession = false, want true (purged)")
	}
	e.wantLog(
		"SetMeta:"+tpID+":pgrouter.purge_pending=1",
		"Close:"+tpID+":purge=false",
		"Release:"+poolLeaseID,
		"Close:"+tpID+":purge=true",
	)
	if len(e.open.Opens) != 0 {
		t.Errorf("a pooled checkout must never be opened as a worktree; Opens=%v", e.open.Opens)
	}
}

// A failed release keeps the row, marked, so the next reconcile retries it.
func TestCloseSession_checkoutLeaseReleaseFailureKeepsRow(t *testing.T) {
	e := newTPEnv(t)
	withTeardownPool(t, e.log, errors.New("pool wedged"))
	if closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, leasedSession(), false) {
		t.Fatal("closeSession = true, want false (release failed)")
	}
	for _, c := range e.log.all() {
		if c == "Close:"+tpID+":purge=true" {
			t.Fatalf("row purged although the release failed: %q", e.log.all())
		}
	}
}

// A lease the pool no longer knows (reaped) is the goal state: the row purges.
func TestCloseSession_checkoutLeaseAlreadyGonePurges(t *testing.T) {
	e := newTPEnv(t)
	withTeardownPool(t, e.log, checkoutpool.ErrNoLease)
	if !closeSession(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, leasedSession(), false) {
		t.Fatal("closeSession = false, want true: no such lease means already released")
	}
}

// The purge_pending retry releases the lease of a marked row.
func TestRetryPurgePending_checkoutLeaseReleased(t *testing.T) {
	e := newTPEnv(t)
	p := withTeardownPool(t, e.log, nil)
	row := leasedSession()
	row.Meta[ccpool.MetaKeyPurgePending] = "1"
	row.CloseReason, row.Live = "handler", false
	purged, attempted := retryPurgePending(context.Background(), e.cc, e.open.Open, tpRepoRoot, e.wtDir, nil, []ccpool.Session{row}, row)
	if !purged || !attempted {
		t.Fatalf("purged=%v attempted=%v, want both", purged, attempted)
	}
	if len(p.released) != 1 || p.released[0] != poolLeaseID {
		t.Errorf("released = %v, want [%s]", p.released, poolLeaseID)
	}
}

// Orphan reconcile: a reclaimed orphan's lease is released and nothing is
// removed as a worktree, whatever the role's isolation type says.
func TestReclaimIdleOrphan_checkoutLeaseReleasedNotRemoved(t *testing.T) {
	row := orphanRow(ccpool.StateIdle)
	row.CWD = poolLeaseCWD
	row.Meta[ccpool.MetaKeyCheckoutLease] = poolLeaseID
	h := newOrphanHarness(t, row)
	p := &recPool{log: &callLog{}}
	h.deps.Pool = p
	role := orphanRole(roles.CloseOnly, 25*time.Minute)
	role.CCPool.Isolation = roles.IsolationConfig{Type: "checkout-pool"}
	r, _ := h.run(role)
	if r != 1 {
		t.Fatalf("reclaimed = %d, want 1", r)
	}
	if len(p.released) != 1 || p.released[0] != poolLeaseID {
		t.Errorf("released = %v, want [%s]", p.released, poolLeaseID)
	}
	if len(h.opener.Removed) != 0 || len(h.opener.BranchDeletes) != 0 {
		t.Errorf("a pooled checkout must not be removed; removed=%v branches=%v", h.opener.Removed, h.opener.BranchDeletes)
	}
}

// The orphan hard stop never resets a pooled checkout (the release captures its
// uncommitted work to a rescue ref first).
func TestOrphanResetDir(t *testing.T) {
	if got := orphanResetDir(leasedSession()); got != "" {
		t.Errorf("pooled orphan reset dir = %q, want empty", got)
	}
	if got := orphanResetDir(orphanRow(ccpool.StateWorking)); got != orphanCWD {
		t.Errorf("worktree orphan reset dir = %q, want %q", got, orphanCWD)
	}
}
