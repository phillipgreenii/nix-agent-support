// Package store defines the persistence interfaces for pa-monitor's daemon
// state. SQLite is one implementation; in-memory shims for tests are another.
//
// All interfaces are read-only or write-only by convention — a single
// WriteService goroutine owns all mutations to serialise concurrent
// callers without an explicit mutex.
package store

import (
	"time"
)

// Filter is the active/all distinction enforced by SessionStore.List.
type Filter int

const (
	// FilterActive: pid IS NOT NULL AND in active block.
	FilterActive Filter = iota
	// FilterAll: pid IS NOT NULL OR in active block.
	FilterAll
)

// FreshnessWindow describes the per-entity freshness gate.
// Queries filter rows whose last_processed_at is older than the window.
type FreshnessWindow struct {
	Sessions time.Duration
	Blocks   time.Duration
	Weeks    time.Duration
}

// DefaultFreshness returns the 12x-poll-interval defaults from the spec.
func DefaultFreshness() FreshnessWindow {
	return FreshnessWindow{
		Sessions: 60 * time.Second,
		Blocks:   12 * time.Minute,
		Weeks:    12 * time.Minute,
	}
}
