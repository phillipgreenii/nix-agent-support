package serve

import (
	"fmt"
	"strings"
	"time"
)

// Hooks are the timing seams of a test build. A shipped build never sets them
// (the binary reads no environment for them; they arrive as a link-time string,
// main.serveHooks, like main.clientBudgetOverride), so a real-process test can
// watch a slow migration, a viewer switch or a failed refresh without waiting
// out the production intervals. The zero value is production.
type Hooks struct {
	// ViewerInterval replaces the 10-minute viewer re-resolution.
	ViewerInterval time.Duration
	// RetryDelay replaces the scheduler's 30-second wait before a failed task
	// is retried.
	RetryDelay time.Duration
	// PollInterval replaces the scheduler's 1-second poll.
	PollInterval time.Duration
	// MigrationDelay makes the first migration step sleep this long before it
	// runs, so the daemon is observably migrating.
	MigrationDelay time.Duration
	// SweepInterval replaces the 10-minute expiry sweep.
	SweepInterval time.Duration
}

// ParseHooks reads "key=duration,key=duration" (keys: viewer_interval,
// retry_delay, poll_interval, migration_delay, sweep_interval). The empty
// string is the zero Hooks.
func ParseHooks(s string) (Hooks, error) {
	var h Hooks
	s = strings.TrimSpace(s)
	if s == "" {
		return h, nil
	}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			return Hooks{}, fmt.Errorf("serve hooks: %q is not key=duration", kv)
		}
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d <= 0 {
			return Hooks{}, fmt.Errorf("serve hooks: %q: want a positive duration", kv)
		}
		switch strings.TrimSpace(k) {
		case "viewer_interval":
			h.ViewerInterval = d
		case "retry_delay":
			h.RetryDelay = d
		case "poll_interval":
			h.PollInterval = d
		case "migration_delay":
			h.MigrationDelay = d
		case "sweep_interval":
			h.SweepInterval = d
		default:
			return Hooks{}, fmt.Errorf("serve hooks: unknown key %q", k)
		}
	}
	return h, nil
}
