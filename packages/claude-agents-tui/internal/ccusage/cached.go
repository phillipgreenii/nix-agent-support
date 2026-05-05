package ccusage

import (
	"context"
	"sync"
	"time"
)

// CachedRunner shields the polling hot path from ccusage's slow startup.
//
// `ccusage blocks --active --json --offline` can take 10–30s on a machine
// with a large ~/.claude/projects/ tree because it parses every transcript
// line to compute burn rate. Running that call inline on every TUI refresh
// blocks the UI and stacks up subprocesses faster than they can finish.
//
// CachedRunner instead invokes ccusage on a fixed background interval and
// serves callers from an in-memory cache. Get is non-blocking and returns
// the most recent successful output (or nil before the first success).
type CachedRunner struct {
	runFn    func(ctx context.Context) ([]byte, error)
	interval time.Duration
	timeout  time.Duration

	mu       sync.Mutex
	probed   bool
	last     []byte
	lastOK   time.Time
	lastErr  error
	lastErrT time.Time
}

// NewCachedRunner builds a runner that invokes runFn every refresh interval
// with a per-call timeout. Callers should call Start once to kick off the
// background loop.
func NewCachedRunner(interval, timeout time.Duration, runFn func(ctx context.Context) ([]byte, error)) *CachedRunner {
	return &CachedRunner{runFn: runFn, interval: interval, timeout: timeout}
}

// Start launches the background refresh loop. It returns immediately; the
// first refresh runs in the launched goroutine. The loop exits when ctx is
// cancelled.
func (c *CachedRunner) Start(ctx context.Context) {
	go func() {
		c.refresh(ctx)
		t := time.NewTicker(c.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.refresh(ctx)
			}
		}
	}()
}

// refreshNow forces a synchronous refresh. Useful for tests.
func (c *CachedRunner) refresh(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	out, err := c.runFn(runCtx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probed = true
	if err == nil {
		c.last = out
		c.lastOK = time.Now()
		c.lastErr = nil
	} else {
		c.lastErr = err
		c.lastErrT = time.Now()
	}
}

// Probed returns true once the first background refresh has completed
// (whether or not it succeeded). Use this to distinguish "not yet checked"
// from "checked and failed" in the UI.
func (c *CachedRunner) Probed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probed
}

// LastErr returns the error from the most recent refresh attempt, or nil if
// the last attempt succeeded. Always nil before the first refresh.
func (c *CachedRunner) LastErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Get returns the most recent cached output. It never blocks. Before the
// first successful refresh it returns (nil, nil) so the poller treats the
// 5h block as simply not-yet-available rather than errored.
func (c *CachedRunner) Get(_ context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.last) > 0 {
		return c.last, nil
	}
	return nil, nil
}
