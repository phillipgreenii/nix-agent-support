package changes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// statsWindow is how far back Stats counts timeouts and failures; an event
// exactly this old still counts.
const statsWindow = time.Hour

// defaultInterval is the sweep period of a Lifecycle whose Interval is zero.
const defaultInterval = time.Second

// Queuer queues a group's refresh. *groupsvc.Service satisfies it; the group
// engine merges the request with a queued task and never downgrades one.
type Queuer interface {
	QueueGroup(ctx context.Context, id, group string, class governor.Class, due time.Time) error
}

// Stats are the settle lifecycle's counts: Pending is the stored pending-row
// count; Timeouts and Failures count the rows this lifecycle ended with
// ResultTimeout and ResultFailed within the last hour (in memory, so a restart
// resets them).
type Stats struct{ Pending, Timeouts, Failures int }

// Lifecycle is the part of the settle barrier that depends on time and on the
// process's life: it ends the pending rows whose deadline has passed (Run,
// Sweep), ends every one at SIGTERM (FlushAll) and picks the rows up again on
// start (Recover). The rows themselves are made and satisfied by the Engine;
// the lifecycle only ends them, through Engine.Complete.
//
// Only the Engine and Store fields are required. A zero Clock is the system
// clock, a zero Interval is one second and a nil Log discards. Its methods are
// safe for concurrent use.
type Lifecycle struct {
	Engine   *Engine
	Store    *store.Store
	Queue    Queuer
	Clock    governor.Clock
	Interval time.Duration
	Log      func(event string, kv ...any)

	mu    sync.Mutex
	ended []endedRow // timeouts and failures, oldest first
}

type endedRow struct {
	at     time.Time
	result string
}

func (l *Lifecycle) now() time.Time {
	if l.Clock == nil {
		return time.Now()
	}
	return l.Clock.Now()
}

func (l *Lifecycle) log(event string, kv ...any) {
	if l.Log != nil {
		l.Log(event, kv...)
	}
}

// Run blocks until ctx ends and calls Sweep every Interval. An error from a
// sweep is logged and the loop goes on.
func (l *Lifecycle) Run(ctx context.Context) {
	interval := l.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := l.Sweep(ctx); err != nil && ctx.Err() == nil {
				l.log("settle_sweep_error", "error", err)
			}
		}
	}
}

// Sweep ends every pending row whose deadline is not after the clock's now,
// each in its own transaction, through Engine.Complete. A row another path
// ended (or re-created with a later deadline) between the list and the
// transaction is skipped. A failure on one row does not stop the others; the
// errors are joined and the failed row stays for the next sweep. It never
// moves a deadline and never queues a group: the groups left stale stay queued
// by the scheduler at their own class.
func (l *Lifecycle) Sweep(ctx context.Context) (completed int, err error) {
	rows, err := l.Store.ListPending(ctx)
	if err != nil {
		return 0, fmt.Errorf("changes: list pending rows: %w", err)
	}
	now := l.now()
	var errs []error
	for _, p := range rows {
		if p.Deadline.After(now) {
			continue
		}
		done, err := l.end(ctx, p, now, true)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if done {
			completed++
		}
	}
	return completed, errors.Join(errs...)
}

// FlushAll ends EVERY pending row now, deadline or not (the SIGTERM drain). A
// row it ends before its deadline gets the result word the sweep rule gives:
// failed when a still-awaited group's stored row carries a last_error, timeout
// otherwise.
func (l *Lifecycle) FlushAll(ctx context.Context) (completed int, err error) {
	rows, err := l.Store.ListPending(ctx)
	if err != nil {
		return 0, fmt.Errorf("changes: list pending rows: %w", err)
	}
	now := l.now()
	var errs []error
	for _, p := range rows {
		done, err := l.end(ctx, p, now, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if done {
			completed++
		}
	}
	return completed, errors.Join(errs...)
}

// Recover is the start-up step: for each stored pending row it ends the row
// when its deadline has passed and otherwise queues each still-awaited group
// at governor.ClassInvalidated, due now (so the catch-up respects the governor
// like every other spend), appending nothing for it. A failure on one row or
// group does not stop the others; the errors are joined.
func (l *Lifecycle) Recover(ctx context.Context) (queued, completed int, err error) {
	rows, err := l.Store.ListPending(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("changes: list pending rows: %w", err)
	}
	now := l.now()
	var errs []error
	for _, p := range rows {
		if !p.Deadline.After(now) {
			done, err := l.end(ctx, p, now, true)
			if err != nil {
				errs = append(errs, err)
			} else if done {
				completed++
			}
			continue
		}
		aw, err := DecodeAwaited(p.Awaited)
		if err != nil {
			errs = append(errs, fmt.Errorf("changes: pending %s/%s: %w", p.Kind, p.ID, err))
			continue
		}
		for _, a := range aw {
			if l.Queue == nil {
				errs = append(errs, fmt.Errorf("changes: no queue to re-queue %s of %s", a.Group, p.ID))
				continue
			}
			if err := l.Queue.QueueGroup(ctx, p.ID, a.Group, governor.ClassInvalidated, now); err != nil {
				errs = append(errs, fmt.Errorf("changes: re-queue %s of %s: %w", a.Group, p.ID, err))
				continue
			}
			queued++
		}
	}
	return queued, completed, errors.Join(errs...)
}

// Stats reports the stored pending count and the last hour's timeouts and
// failures.
func (l *Lifecycle) Stats(ctx context.Context) (Stats, error) {
	n, err := l.Store.CountPending(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("changes: count pending rows: %w", err)
	}
	s := Stats{Pending: n}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(l.now())
	for _, e := range l.ended {
		switch e.result {
		case ResultTimeout:
			s.Timeouts++
		case ResultFailed:
			s.Failures++
		}
	}
	return s, nil
}

// prune drops the events older than the window. The caller holds mu.
func (l *Lifecycle) prune(now time.Time) {
	i := 0
	for i < len(l.ended) && now.Sub(l.ended[i].at) > statsWindow {
		i++
	}
	l.ended = l.ended[i:]
}

func (l *Lifecycle) record(now time.Time, result string) {
	if result != ResultTimeout && result != ResultFailed {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.ended = append(l.ended, endedRow{at: now, result: result})
}

// end ends p in its own transaction. The row is re-read inside the transaction
// and skipped (done false, no error) when it is gone, or, with onlyIfDue, when
// its deadline is now after the clock. The result word is recorded only after
// the transaction committed.
func (l *Lifecycle) end(ctx context.Context, p store.PendingChange, now time.Time, onlyIfDue bool) (done bool, err error) {
	var result string
	err = l.Store.InTx(ctx, func(tx *store.Tx) error {
		done, result = false, ""
		cur, ok, err := tx.GetPending(ctx, p.Kind, p.ID)
		if err != nil || !ok {
			return err
		}
		if onlyIfDue && cur.Deadline.After(now) {
			return nil
		}
		if result, err = l.resultWord(ctx, tx, cur); err != nil {
			return err
		}
		if err := l.Engine.Complete(ctx, tx, cur, result); err != nil {
			return err
		}
		done = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("changes: end pending %s/%s: %w", p.Kind, p.ID, err)
	}
	if done {
		l.record(now, result)
		l.log("settle_ended", "kind", p.Kind, "id", p.ID, "result", result)
	}
	return done, nil
}

// resultWord is the word of a row ended without its barrier satisfied: failed
// when a still-awaited group's stored row carries a non-empty last_error and
// timeout otherwise (a governor pause, a queue that did not reach the group). A
// row that awaits nothing was satisfied and is settled.
func (l *Lifecycle) resultWord(ctx context.Context, tx *store.Tx, p store.PendingChange) (string, error) {
	aw, err := DecodeAwaited(p.Awaited)
	if err != nil {
		return "", fmt.Errorf("pending %s/%s: %w", p.Kind, p.ID, err)
	}
	if len(aw) == 0 {
		return ResultSettled, nil
	}
	for _, a := range aw {
		row, ok, err := tx.GetGroup(ctx, p.Kind, p.ID, a.Group)
		if err != nil {
			return "", err
		}
		if ok && row.LastError != "" {
			return ResultFailed, nil
		}
	}
	return ResultTimeout, nil
}
