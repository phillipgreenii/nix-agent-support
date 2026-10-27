package serve

import (
	"context"
	"time"
)

// ExitWithParent returns a context that ends when ctx does or when the
// process's parent changes (the parent died and the daemon was re-parented to
// init or a subreaper). Cancelling it drives the same drain as SIGTERM, so a
// daemon whose supervisor vanished without a signal (a test binary killed by a
// timeout, a SIGKILLed runner) stops by itself instead of living on, orphaned.
//
// interval <= 0 (production: Hooks.ParentWatch is never set) returns ctx
// unchanged with a no-op cancel and starts nothing. getppid is os.Getppid in
// the daemon; a test injects its own.
func ExitWithParent(ctx context.Context, interval time.Duration, getppid func() int) (context.Context, context.CancelFunc) {
	if interval <= 0 {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	initial := getppid()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if getppid() != initial {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
