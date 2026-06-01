// Package service hosts the WriteService (single-writer goroutine) and the
// ReadService (per-request DB queries materialised into aggregate.Tree).
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// WriteDeps bundles every Store the writer goroutine needs.
type WriteDeps struct {
	Sessions      store.SessionStore
	Blocks        store.BlockStore
	Weeks         store.WeekStore
	Contributions store.ContributionStore
	Toggles       store.ToggleStore
	Nudges        store.NudgeStore
}

// WriteService serialises mutations to the stores through a single goroutine.
// Concurrent callers submit closures; the goroutine runs them in arrival order.
type WriteService struct {
	deps      WriteDeps
	ch        chan writeOp
	stop      chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

type writeOp struct {
	fn   func(context.Context) error
	done chan error
}

const writeQueueDepth = 64

func NewWriteService(deps WriteDeps) *WriteService {
	return &WriteService{
		deps: deps,
		ch:   make(chan writeOp, writeQueueDepth),
		stop: make(chan struct{}),
	}
}

func (w *WriteService) Start(ctx context.Context) {
	w.startOnce.Do(func() {
		w.wg.Add(1)
		go w.loop(ctx)
	})
}

func (w *WriteService) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
	w.wg.Wait()
}

var errWriteServiceStopped = errors.New("write service stopped")

func (w *WriteService) loop(ctx context.Context) {
	defer w.wg.Done()
	defer func() {
		// Drain any ops left in the queue so their submitters are never
		// blocked forever after the loop exits.
		for {
			select {
			case op := <-w.ch:
				op.done <- errWriteServiceStopped
			default:
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case op := <-w.ch:
			op.done <- op.fn(ctx)
		}
	}
}

// submit enqueues fn and blocks until the writer goroutine returns its
// result. Three early-return paths:
//   - ctx is cancelled before the op is enqueued → returns ctx.Err()
//   - Stop() is called before the op is enqueued → returns
//     errWriteServiceStopped
//   - ctx is cancelled AFTER the op is enqueued → returns ctx.Err();
//     the op still executes (write durability is the writer's
//     responsibility, not the caller's).
//
// If Stop is called AFTER the op is enqueued, the deferred drain in
// loop will pop the op and reply via op.done; the caller returns either
// the real result (if drain processed first) or errWriteServiceStopped.
func (w *WriteService) submit(ctx context.Context, fn func(context.Context) error) error {
	done := make(chan error, 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.stop:
		return errWriteServiceStopped
	case w.ch <- writeOp{fn: fn, done: done}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.stop:
		// Stop fired after we enqueued. The drain (deferred in loop) will
		// pop us and reply via op.done, OR our op may have already run; in
		// either case, prefer the real result if it's ready.
		select {
		case err := <-done:
			return err
		default:
			return errWriteServiceStopped
		}
	case err := <-done:
		return err
	}
}

// Sync blocks until the queue drains. Used by tests; production typically
// doesn't need it (the next read picks up writes by virtue of the round trip).
func (w *WriteService) Sync(ctx context.Context) error {
	return w.submit(ctx, func(ctx context.Context) error { return nil })
}

// --- mutation surface ---

func (w *WriteService) UpsertSession(ctx context.Context, s store.Session) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.Upsert(ctx, s)
	})
}

func (w *WriteService) UpsertBlock(ctx context.Context, b store.Block) (int64, error) {
	var id int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.Upsert(ctx, b)
		id = got
		return err
	})
	return id, err
}

func (w *WriteService) UpsertWeek(ctx context.Context, weekRow store.Week) (int64, error) {
	var id int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.Upsert(ctx, weekRow)
		id = got
		return err
	})
	return id, err
}

func (w *WriteService) UpsertBlockContribution(ctx context.Context, c store.Contribution) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Contributions.UpsertBlock(ctx, c)
	})
}

func (w *WriteService) UpsertWeekContribution(ctx context.Context, c store.Contribution) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Contributions.UpsertWeek(ctx, c)
	})
}

func (w *WriteService) SetToggle(ctx context.Context, name string, value bool) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Toggles.Set(ctx, name, value)
	})
}

func (w *WriteService) RecordNudge(ctx context.Context, ev store.NudgeEvent) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Nudges.Record(ctx, ev)
	})
}

// Toggles returns the underlying ToggleStore. Used by the one-shot
// runtime.json migration on daemon startup (MigrateRuntimeJSON).
func (w *WriteService) Toggles() store.ToggleStore { return w.deps.Toggles }

// Nudges returns the underlying NudgeStore. Used by MigrateRuntimeJSON.
func (w *WriteService) Nudges() store.NudgeStore { return w.deps.Nudges }

// Sessions returns the underlying SessionStore. Used by MigrateRuntimeJSON.
func (w *WriteService) Sessions() store.SessionStore { return w.deps.Sessions }

// MarkSessionEscalated persists the escalation flip (last_error_retryable =
// false) for a session whose DisruptEscalated watermark is set. Called by the
// daemon's escalation loop so the DB reflects the in-memory flip.
func (w *WriteService) MarkSessionEscalated(ctx context.Context, sessionID string) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.MarkEscalated(ctx, sessionID)
	})
}

// MarkSessionsDeleted / Revived / HardDelete delegate to SessionStore;
// invoked by the GC sweeper.
func (w *WriteService) MarkSessionsDeleted(ctx context.Context, keepIDs []string) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.MarkDeleted(ctx, keepIDs, time.Now().UTC())
	})
}

func (w *WriteService) MarkSessionsRevived(ctx context.Context, reviveIDs []string) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.MarkRevived(ctx, reviveIDs)
	})
}

func (w *WriteService) HardDeleteSessions(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Sessions.HardDelete(ctx, cutoff)
		n = got
		return err
	})
	return n, err
}

// Mirror the block/week orphan + hard-delete operations.
func (w *WriteService) MarkBlockOrphansDeleted(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.MarkOrphansDeleted(ctx, time.Now().UTC())
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) MarkBlocksRevived(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.MarkRevived(ctx)
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) HardDeleteBlocks(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.HardDelete(ctx, cutoff)
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) MarkWeekOrphansDeleted(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.MarkOrphansDeleted(ctx, time.Now().UTC())
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) MarkWeeksRevived(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.MarkRevived(ctx)
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) HardDeleteWeeks(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.HardDelete(ctx, cutoff)
		n = got
		return err
	})
	return n, err
}
