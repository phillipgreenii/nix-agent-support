package store

import (
	"encoding/json"
	"fmt"
)

// SyncRetry is the automatic-retry state of one PR entity's recorded
// interpretation.sync_error (bead pg2-xb6fs): how many consecutive runs have
// failed, whether another automatic retry is coming (and when), or why not.
//
// It lives in the meta table under SyncRetryKey(entityID) as a JSON object —
// the same per-entity meta-key pattern pg-desk reconcile already uses for its
// reconcile.checked.<id> and reconcile.anchor-audited.<id> stamps — so it
// needs no new column and no migration rung. That matters twice over here:
// the migration ladder deliberately stops at schema version 1, and the
// version-2 cutover drops interpretation.sync_error altogether, so retry
// state tied to that column only ever has meaning on a version-1 store.
//
// The pipeline writes it on every failed run of an entity that has a
// recorded sync_error and deletes it on that entity's next successful run.
// Reconcile reads it to decide whether a retry is due; status, doctor and
// serve's /metrics read it to show a retrying row apart from an exhausted
// one. The retry policy itself (classification, backoff, bound) belongs to
// internal/sync, not to this data layer.
type SyncRetry struct {
	// Attempts counts consecutive failed runs since the sync error was
	// first recorded; the original failure is attempt 1, so automatic
	// retries made so far are Attempts-1 (see Retries).
	Attempts int `json:"attempts"`
	// MaxRetries is the retry bound in force when this state was written,
	// kept here so a reader can print "retry 3/10" without loading config.
	MaxRetries int `json:"max_retries"`
	// State is one of SyncRetryRetrying, SyncRetryExhausted or
	// SyncRetryNonTransient.
	State string `json:"state"`
	// LastFailedAt is when the latest failed run was recorded (RFC3339 UTC).
	LastFailedAt string `json:"last_failed_at"`
	// NextRetryAt is the earliest time reconcile will re-drive the entity
	// (RFC3339 UTC). Empty unless State is SyncRetryRetrying.
	NextRetryAt string `json:"next_retry_at,omitempty"`
}

// The three values of SyncRetry.State.
const (
	// SyncRetryRetrying: the latest failure was transient and the retry
	// bound is not reached; reconcile re-drives the entity once NextRetryAt
	// has passed.
	SyncRetryRetrying = "retrying"
	// SyncRetryExhausted: the latest failure was transient but the retry
	// bound is reached; no further automatic retry. The row stays a
	// sync_error until an operator acts (or an event-driven run succeeds).
	SyncRetryExhausted = "exhausted"
	// SyncRetryNonTransient: the latest failure needs a person (for example
	// an authentication or validation error), so it is never retried
	// automatically.
	SyncRetryNonTransient = "non-transient"
)

// syncRetryKeyPrefix prefixes the per-entity meta key holding SyncRetry.
const syncRetryKeyPrefix = "sync_retry."

// SyncRetryKey is the meta key holding entityID's SyncRetry.
func SyncRetryKey(entityID string) string { return syncRetryKeyPrefix + entityID }

// Retries is the number of automatic retries already made (Attempts-1,
// never negative).
func (r SyncRetry) Retries() int {
	if r.Attempts < 1 {
		return 0
	}
	return r.Attempts - 1
}

// EffectiveState is State, or SyncRetryRetrying for the zero value. A
// sync_error row with no recorded retry state (one recorded before retry
// state existed, or whose state write was lost) is due for a retry, so it
// reads as retrying.
func (r SyncRetry) EffectiveState() string {
	if r.State == "" {
		return SyncRetryRetrying
	}
	return r.State
}

// GetSyncRetry returns entityID's retry state, or found=false when none is
// recorded.
func (s *Store) GetSyncRetry(entityID string) (SyncRetry, bool, error) {
	raw, found, err := s.GetMeta(SyncRetryKey(entityID))
	if err != nil || !found {
		return SyncRetry{}, found, err
	}
	var r SyncRetry
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return SyncRetry{}, false, fmt.Errorf("store: decode sync retry state for %s: %w", entityID, err)
	}
	return r, true, nil
}

// SetSyncRetry records entityID's retry state, replacing any previous one.
func (s *Store) SetSyncRetry(entityID string, r SyncRetry) error {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("store: encode sync retry state for %s: %w", entityID, err)
	}
	return s.SetMeta(SyncRetryKey(entityID), string(b))
}

// DeleteSyncRetry removes entityID's retry state. Deleting absent state is
// not an error.
func (s *Store) DeleteSyncRetry(entityID string) error {
	return s.DeleteMeta(SyncRetryKey(entityID))
}
