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
}

// Total is the cumulative-tokens meter (all components summed).
func (s Snapshot) Total() int {
	return s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens + s.OutputTokens
}

// Reader reads a session's cumulative usage from its transcript file.
type Reader interface {
	Read(ctx context.Context, transcriptPath string) (Snapshot, error)
}
