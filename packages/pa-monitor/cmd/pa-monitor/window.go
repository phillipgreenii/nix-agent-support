// window.go: the look-back window shared by `sessions` and `search`. pa-monitor
// only ever shows recent sessions, so both commands look back at most
// maxLookback; deeper history is the recorder's own search/query.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// maxLookback is the furthest back a `sessions` or `search` window may start,
// and the start of the window when no --since is given.
const maxLookback = 7 * 24 * time.Hour

// resolveWindow returns the window a command runs with: since defaults to
// now-maxLookback when unset. A since older than now-maxLookback is an error
// (never clamped, since a clamp would answer a smaller question than asked
// without saying so); exactly now-maxLookback is allowed. before is passed
// through (zero means open on that side). defaulted is the start of the
// default window, zero when since was given.
func resolveWindow(cmd string, since, before, now time.Time) (s, b, defaulted time.Time, err error) {
	limit := now.Add(-maxLookback)
	if since.IsZero() {
		return limit, before, limit, nil
	}
	if since.Before(limit) {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%s: --since %s is older than 7 days: pa-monitor looks back at most 7 days; search older history with pg-claude-code-recorder search or query directly", cmd, since.UTC().Format(time.RFC3339))
	}
	return since, before, time.Time{}, nil
}

// withNarrowHint words a recorder query that ran out of time: it says how to
// narrow the window (flag names the narrowing flags) or to use the recorder
// directly. Any other error is returned unchanged.
func withNarrowHint(err error, flag string) error {
	if errors.Is(err, client.ErrQueryTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: the query ran out of time; narrow it with %s, or use pg-claude-code-recorder search or query directly", err, flag)
	}
	return err
}
