package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Source identifies which producer emitted a nudge intent.
type Source string

const (
	SourceWindowReset Source = "window_reset"
	SourceDisrupted   Source = "disrupted"
	SourceManual      Source = "manual"
)

// IntentKey uniquely identifies one pending intent in the store.
// A single session may simultaneously hold up to one intent per Source.
type IntentKey struct {
	SessionID string
	Source    Source
}

// NudgeIntent is one pending request to nudge a session, owned by exactly
// one producer (Key.Source). The dispatcher reads these and clears them
// after a fire (or suppression). Cause is non-nil only for Disrupted.
type NudgeIntent struct {
	Key       IntentKey
	Text      string
	EmittedAt time.Time
	Cause     *transcript.ErrorRecord
}
