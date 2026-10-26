package ccpool

import "context"

// AlreadyClosed reports whether s is a row that was closed and whose tmux
// session is gone: ccpool has nothing left to tear down, so a further
// non-purge close would only re-stamp the reason and append another "close"
// event for the same session (bead pg2-92rfu). A row carrying a reason while
// still Live is NOT closed: its teardown failed, and the next close retries it.
func (s Session) AlreadyClosed() bool {
	return s.CloseReason != "" && !s.Live
}

// CloseIfOpen is Runner.Close for a path that may meet a row somebody already
// closed (an absorbed handler-closed settled row, a hard stop on it): it skips
// the close, returning closed=false, when the row is AlreadyClosed, so a
// session records exactly one close however many dispatches touch it.
//
// It never skips a purge (that deletes the row), a row it cannot find, or a
// List failure: each falls through to a real Close, the pre-existing behavior.
func CloseIfOpen(ctx context.Context, r Runner, externalID string, purge bool) (closed bool, err error) {
	if !purge {
		if sessions, lerr := r.List(ctx); lerr == nil {
			for _, s := range sessions {
				if s.ExternalID == externalID && s.AlreadyClosed() {
					return false, nil
				}
			}
		}
	}
	return true, r.Close(ctx, externalID, purge)
}
