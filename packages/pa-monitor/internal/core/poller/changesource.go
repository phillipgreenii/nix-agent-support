package poller

import (
	"context"
	"time"
)

// MinFeedInterval is the fastest the producer asks the recorder for changes:
// the design polls the change feed every 5 to 10 seconds.
const MinFeedInterval = 5 * time.Second

// ChangeSource decides when the producer should re-Assemble. The default polls
// the recorder's change feed at a fixed cadence: each Assemble's Scan drains the
// feed from its cursor and reads only the files the feed named.
type ChangeSource interface {
	// WaitForChange blocks until the next poll is due, returning true; or until
	// ctx is done, returning false.
	WaitForChange(ctx context.Context) bool
}

// feedChangeSource fires once per interval.
type feedChangeSource struct {
	interval time.Duration
}

// newFeedChangeSource returns the default source. The interval is raised to
// MinFeedInterval when it is lower and non-positive means MinFeedInterval.
func newFeedChangeSource(interval time.Duration) *feedChangeSource {
	if interval < MinFeedInterval {
		interval = MinFeedInterval
	}
	return &feedChangeSource{interval: interval}
}

func (cs *feedChangeSource) WaitForChange(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	t := time.NewTimer(cs.interval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
