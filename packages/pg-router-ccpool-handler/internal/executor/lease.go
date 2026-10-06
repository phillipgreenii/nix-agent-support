package executor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
)

// Supervision lease (bead pg2-g2u9m, INV-CCH-18).
//
// A handler process is a per-dispatch child of the daemon and alone supervises
// the session it launched: the completion wait, the budget watchdog and the
// terminal cleanup all die with it (daemon restart, crash, or giving up at
// MaxWait). To let a LATER dispatch tell a supervised session from an orphan,
// the handler stamps pgrouter.lease_until = now + LeaseTTL on the session's
// metadata every PollInterval for as long as the dispatch is alive; an expired
// lease means nobody is supervising the session. The initial value is written
// atomically by `ccpool new --meta` (ccpool.DispatchMeta) and covers the whole
// launch wait, so a session still inside Ensure is never an orphan.

const (
	// leaseWriteTimeout bounds one lease refresh, so a wedged `ccpool meta set`
	// cannot stall the refresh loop past the lease TTL.
	leaseWriteTimeout = 30 * time.Second
	// leaseErrorAfter is the consecutive-failure count at which a failing refresh
	// escalates from WARN to ERROR (and every further multiple of it), so a live
	// handler whose lease is lapsing is visible in the log.
	leaseErrorAfter = 3
	// absorbLockWait bounds how long an absorbing dispatch waits for an in-flight
	// orphan reclaim of the same session before giving up.
	absorbLockWait = 30 * time.Second
	// absorbLockStep is the retry step while waiting for that lock.
	absorbLockStep = 50 * time.Millisecond
)

// leaseRefresher writes the lease for one session and tracks consecutive
// failures. It is used by a single goroutine at a time.
type leaseRefresher struct {
	r        *ccpoolRun
	name     string
	failures int
}

// refresh stamps pgrouter.lease_until = now + LeaseTTL. A failure never fails
// the dispatch: it is logged (WARN; ERROR at every leaseErrorAfter-th
// consecutive failure) and retried on the next tick.
func (l *leaseRefresher) refresh(ctx context.Context) {
	wctx, cancel := context.WithTimeout(ctx, leaseWriteTimeout)
	defer cancel()
	until := l.r.deps.clock().Add(l.r.deps.Cfg.LeaseTTL)
	err := l.r.deps.CC.SetMeta(wctx, l.name, ccpool.MetaKeyLeaseUntil, ccpool.FormatMetaTime(until))
	if err == nil {
		if l.failures > 0 {
			slog.Info("lease refresh recovered", "session", l.name, "after_failures", l.failures)
		}
		l.failures = 0
		return
	}
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return // the dispatch is ending; not a lease fault
	}
	l.failures++
	if l.failures%leaseErrorAfter == 0 {
		slog.Error("lease refresh keeps failing; this session's lease is lapsing and a later dispatch may reclaim it as an orphan",
			"session", l.name, "consecutive_failures", l.failures, "lease_ttl", l.r.deps.Cfg.LeaseTTL, "err", err)
		return
	}
	slog.Warn("lease refresh failed", "session", l.name, "consecutive_failures", l.failures, "err", err)
}

// startLease starts the background lease refresh for session name and returns
// its stop function (idempotent; it waits for the loop to exit). It writes once
// immediately, then every PollInterval, until stop is called or ctx ends. A
// non-positive LeaseTTL disables the lease (stop is a no-op), which is also how
// a session launched without lease metadata stays unleased. Call it from Ensure
// success (or absorb) and defer the stop so the lease covers Send, the wait, the
// watchdog, the worktree cleanup's quiet wait and the settled-session close.
func (r *ccpoolRun) startLease(ctx context.Context, name string) (stop func()) {
	if r.deps.Cfg.LeaseTTL <= 0 {
		return func() {}
	}
	poll := r.deps.Cfg.PollInterval
	if poll <= 0 {
		poll = 10 * time.Second
	}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	l := &leaseRefresher{r: r, name: name}
	go func() {
		defer close(done)
		l.refresh(lctx)
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				l.refresh(lctx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// takeOverForAbsorb is the lease handshake an absorbing dispatch performs
// BEFORE it starts waiting on an existing session (bead pg2-g2u9m, INV-CCH-18):
// it takes the same per-session flock the orphan reconcile takes, re-reads the
// row, and refreshes the lease while holding it, so a reconcile that observed
// an expired lease either finishes first (and the row is then closed, so the
// session is not absorbable) or sees the fresh lease and aborts. ok is false
// when the row can no longer be absorbed (gone, closed, or reclaimed as an
// orphan): the caller then launches a fresh session. Without a configured lock
// dir (unit tests; production always sets one) there is nothing to exclude, and
// the lease is simply refreshed.
func (r *ccpoolRun) takeOverForAbsorb(ctx context.Context, existing ccpool.Session, eventID string) (ccpool.Session, bool, error) {
	if r.deps.LockDir == "" {
		r.refreshLeaseOnce(ctx, existing.ExternalID)
		return existing, true, nil
	}
	lock, err := r.lockSession(ctx, existing.ExternalID)
	if err != nil {
		return ccpool.Session{}, false, err
	}
	defer lock.Unlock()
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		// Can't tell. Keep the original absorb decision: findSessionByName already
		// chose this row, and an unreadable list is also what active() treats as
		// "keep waiting".
		r.refreshLeaseOnce(ctx, existing.ExternalID)
		return existing, true, nil
	}
	for _, s := range sessions {
		if s.ExternalID != existing.ExternalID {
			continue
		}
		if crashOrphaned(s) || staleSettledRow(s, eventID) {
			slog.Info("absorb: session was closed or reclaimed while taking over; launching afresh",
				"session", s.ExternalID, "close_reason", s.CloseReason)
			return ccpool.Session{}, false, nil
		}
		r.refreshLeaseOnce(ctx, s.ExternalID)
		return s, true, nil
	}
	return ccpool.Session{}, false, nil
}

// refreshLeaseOnce writes the lease synchronously, best effort.
func (r *ccpoolRun) refreshLeaseOnce(ctx context.Context, name string) {
	if r.deps.Cfg.LeaseTTL <= 0 {
		return
	}
	(&leaseRefresher{r: r, name: name}).refresh(ctx)
}

// lockSession takes the per-session flock, retrying until absorbLockWait. The
// holder is an orphan reconcile mid-action on this very session, so the wait is
// short and bounded; an error means the dispatch must not absorb blind.
func (r *ccpoolRun) lockSession(ctx context.Context, externalID string) (*sessionlock.Lock, error) {
	deadline := time.Now().Add(absorbLockWait)
	for {
		l, err := sessionlock.TryLock(r.deps.LockDir, externalID)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, sessionlock.ErrHeld) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, errors.New("session " + externalID + " is being reclaimed as an orphan; could not take over within " + absorbLockWait.String())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(absorbLockStep):
		}
	}
}
