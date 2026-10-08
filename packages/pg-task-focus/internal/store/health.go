package store

import (
	"errors"
	"time"
)

// ErrStoreUnavailable is returned by Append once the store is read-only
// (INV-LOG-23): nothing is attempted, and only reopening the store clears it.
var ErrStoreUnavailable = errors.New("the event store is read-only")

// ReadOnlyReason is why the store entered read-only mode (INV-LOG-22). It is
// one of the four constants below, each a closed plain sentence naming the
// failed step, and never free text.
type ReadOnlyReason string

// The reasons the store enters read-only mode.
const (
	// ReasonWriteRollbackTruncate: an append write failed and the rollback
	// could not truncate the log.
	ReasonWriteRollbackTruncate ReadOnlyReason = "the append write failed and the rollback could not truncate the log"
	// ReasonWriteRollbackSync: an append write failed and the rollback
	// truncation could not be made durable.
	ReasonWriteRollbackSync ReadOnlyReason = "the append write failed and the rollback could not be synced"
	// ReasonAppendSync: the fsync of an append failed, so the state of the
	// file is unknown.
	ReasonAppendSync ReadOnlyReason = "the append fsync failed"
	// ReasonAdopt: the events are durable but the engine could not adopt the
	// validated state; it reports this through MarkReadOnly.
	ReasonAdopt ReadOnlyReason = "the new state could not be adopted after a durable append"
)

// Health is the store's write health. The zero value means healthy.
type Health struct {
	// ReadOnly is set once the store entered read-only mode.
	ReadOnly bool
	// Reason is the first cause, one of the four ReadOnlyReason constants.
	Reason ReadOnlyReason
	// Since is the instant (Options.Now) the store entered read-only mode.
	Since time.Time
}

// Health reports whether the store is read-only, why and since when. The
// zero value means healthy. It never waits for a write in progress.
func (s *Store) Health() Health {
	s.state.Lock()
	defer s.state.Unlock()
	return s.health
}

// Writable reports whether an Append can be attempted: the store is open and
// not read-only. It never waits for a write in progress.
func (s *Store) Writable() bool {
	s.state.Lock()
	defer s.state.Unlock()
	return !s.closed && !s.health.ReadOnly
}

// MarkReadOnly puts the store into read-only mode for a reason the store
// cannot see itself: the engine uses it with ReasonAdopt. The first cause and
// its instant are kept (INV-LOG-23), so a call on a store that is already
// read-only changes nothing.
func (s *Store) MarkReadOnly(reason ReadOnlyReason) {
	s.state.Lock()
	defer s.state.Unlock()
	if s.health.ReadOnly {
		return
	}
	s.health = Health{ReadOnly: true, Reason: reason, Since: s.now()}
}
