// Package usage reads per-session token usage from a Claude transcript, behind a
// Reader interface so the watchdog never couples to the claude-transcript types.
package usage

import "context"

// Snapshot is cumulative token usage for one session at one instant.
type Snapshot struct {
	Model               string
	InputTokens         int
	CacheCreationTokens int
	CacheReadTokens     int
	OutputTokens        int
	// CacheCreationEphemeral1hTokens and CacheCreationEphemeral5mTokens are the
	// per-TTL breakdown of CacheCreationTokens (a 1h cache write costs 2.0x a
	// model's base input price against 5m's 1.25x, pg2-xgzen). Both are a
	// subset already counted in CacheCreationTokens, so Total() must not sum
	// them again.
	CacheCreationEphemeral1hTokens int
	CacheCreationEphemeral5mTokens int
}

// Total is the cumulative-tokens meter (all components summed).
func (s Snapshot) Total() int {
	return s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens + s.OutputTokens
}

// Reader reads a session's cumulative usage from its transcript file.
type Reader interface {
	Read(ctx context.Context, transcriptPath string) (Snapshot, error)
}
