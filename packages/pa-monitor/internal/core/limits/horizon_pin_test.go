package limits

import (
	"testing"

	ct "github.com/phillipgreenii/claude-transcript"
)

// TestSevenDayWindowMatchesTranscriptHorizon guards the cross-MODULE sync
// invariant behind the reset-instant upper bound (bead pg2-yzs6a).
//
// A rate-limit reset instant enters the daemon by two independent routes that
// cannot share one helper, because they live in two different Go modules and the
// dependency only runs one way (pa-monitor imports claude-transcript, never the
// reverse) — and this package is additionally a pure leaf that imports only the
// standard library so both internal/daemon and internal/core/corpus can consume
// it:
//
//   - the per-session transcript routes (prose clause + legacy retryInMs), bounded
//     in claude-transcript by ct.MaxResetHorizon;
//   - the account-global status-line route (five_hour/seven_day_resets_at epochs),
//     bounded here by boundedReset.
//
// Both bounds mean "one usage window past the observation", and the LONGEST window
// Claude reports — the weekly one — is the value the transcript side uses (its
// messages do not say which window they hit). So the two constants MUST hold the
// same duration; drifting one silently makes the two routes reject different
// inputs. This test is the mechanical link the shared helper cannot be.
func TestSevenDayWindowMatchesTranscriptHorizon(t *testing.T) {
	if sevenDayWindow != ct.MaxResetHorizon {
		t.Errorf("limits.sevenDayWindow = %v, but claudetranscript.MaxResetHorizon = %v; "+
			"they MUST stay in sync (see the 'Kept in sync' comments in limits.go and ratelimit.go)",
			sevenDayWindow, ct.MaxResetHorizon)
	}
}
