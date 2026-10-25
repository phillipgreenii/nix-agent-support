package serve

import (
	"context"
	"fmt"
)

// background runs fn until the daemon drains. A panic in fn is logged and ends
// that loop only.
func (d *daemon) background(ctx context.Context, name string, fn func(ctx context.Context)) {
	d.mu.Lock()
	if d.draining { // the drain already began; nothing new starts
		d.mu.Unlock()
		return
	}
	d.bg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.bg.Done()
		defer func() {
			if r := recover(); r != nil {
				d.log.Log("background_panicked", "loop", name, "panic", fmt.Sprint(r))
			}
		}()
		fn(ctx)
	}()
}

// drainWrites waits for the in-flight writes of the group service to finish
// (within the drain deadline), before the background stops and before the WAL
// checkpoint and the lock release.
func (d *daemon) drainWrites(ctx context.Context) error {
	if s := d.detail.Load(); s != nil {
		return s.Group().DrainWrites(ctx)
	}
	return nil
}

// stopBackground ends the scheduler and the loops, after the in-flight requests
// finished (the drain runs hooks only then).
func (d *daemon) stopBackground(context.Context) error {
	d.mu.Lock()
	cancel := d.bgCancel
	d.draining = true
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.bg.Wait()
	return nil
}

// flushChanges appends every pending settle row, so a SIGTERM loses no detected
// change. It runs after stopBackground: the scheduler has stopped and waited for
// its running batches, so no refresh is in flight and no detection can appear
// after it. A daemon whose startup never built the lifecycle has nothing to
// flush.
func (d *daemon) flushChanges(ctx context.Context) error {
	l := d.settle.Load()
	if l == nil {
		return nil
	}
	n, err := l.FlushAll(ctx)
	if n > 0 {
		d.log.Log("pending_changes_flushed", "rows", n)
	}
	return err
}

// stopTelemetry flushes the exporters.
func (d *daemon) stopTelemetry(ctx context.Context) error {
	d.mu.Lock()
	shutdown := d.telShutdown
	d.mu.Unlock()
	if shutdown == nil {
		return nil
	}
	return shutdown(ctx)
}

// closeStore is the last hook: the WAL checkpoint, then the close.
func (d *daemon) closeStore(context.Context) error {
	st := d.store()
	if st == nil {
		return nil
	}
	cerr := st.Checkpoint()
	if err := st.Close(); err != nil && cerr == nil {
		cerr = err
	}
	return cerr
}
