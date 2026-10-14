package feed

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The two cursor-key kinds.
const (
	// KindPR is the PR feed (`pr changes`).
	KindPR = "pr"
	// KindCI is the CI view (`ci changes`).
	KindCI = "ci"
)

// MovedAsideKey is the store_meta key NoteMovedAside writes: an RFC 3339 time.
const MovedAsideKey = "moved_aside_at"

// ErrQueryNotRecognized is the existing wire code query_not_recognized: the
// query name is not one the daemon is configured with. It is the scriptout
// sentinel itself, so the ops map it with the rest of the closed taxonomy.
var ErrQueryNotRecognized = scriptout.ErrQueryNotRecognized

// ErrAckBeyondHead is returned (wrapped) by Ack for a next_seq above the head of
// the change log. It is an invalid_argument.
var ErrAckBeyondHead = scriptout.WrapError(scriptout.ErrInvalidArgument, "ack beyond the head of the change log")

// CursorExpiredError answers a key whose position is no longer served. Its
// Message begins "cursor_expired:" and tells the caller to re-run with --reset;
// it is an invalid_argument.
type CursorExpiredError struct{ Message string }

func (e *CursorExpiredError) Error() string { return e.Message }

// Is makes errors.Is(err, scriptout.ErrInvalidArgument) hold.
func (e *CursorExpiredError) Is(target error) bool { return target == scriptout.ErrInvalidArgument }

// KeyConflictError refuses the creation of a key while the same consumer holds
// the other kind's key for the same query; it is an invalid_argument. Existing
// names the key that exists, as "(consumer, kind, query)".
type KeyConflictError struct{ Existing, Message string }

func (e *KeyConflictError) Error() string { return e.Message }

// Is makes errors.Is(err, scriptout.ErrInvalidArgument) hold.
func (e *KeyConflictError) Is(target error) bool { return target == scriptout.ErrInvalidArgument }

// keyID is a cursor key.
type keyID struct{ consumer, kind, query string }

func (k keyID) String() string { return "(" + k.consumer + ", " + k.kind + ", " + k.query + ")" }

func (k keyID) other() keyID {
	if k.kind == KindCI {
		k.kind = KindPR
	} else {
		k.kind = KindCI
	}
	return k
}

// keyLocks serialises the calls of one key without growing: an entry exists
// only while a call holds or waits for it.
type keyLocks struct {
	mu sync.Mutex
	m  map[keyID]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

// lock takes the key's lock and returns its release.
func (l *keyLocks) lock(k keyID) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[keyID]*keyLock{}
	}
	e := l.m[k]
	if e == nil {
		e = &keyLock{}
		l.m[k] = e
	}
	e.refs++
	l.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.m, k)
		}
		l.mu.Unlock()
	}
}

func (l *keyLocks) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// validate checks the request fields every call shares.
func validate(kind, consumer, query string) error {
	switch {
	case kind != KindPR && kind != KindCI:
		return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("feed kind %q is neither %q nor %q", kind, KindPR, KindCI))
	case consumer == "":
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "a consumer name is required")
	case query == "":
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "a query name is required")
	}
	return nil
}

// checkKnown refuses a query the daemon does not know.
func (f *Feed) checkKnown(query string) error {
	if !f.cfg.Queries.Known(query) {
		return fmt.Errorf("%w: %q is not a configured query", ErrQueryNotRecognized, query)
	}
	return nil
}

// checkConflict refuses the creation of k while the other kind's key exists.
func (f *Feed) checkConflict(ctx context.Context, k keyID) error {
	o := k.other()
	_, ok, err := f.cfg.Store.GetConsumer(ctx, o.consumer, o.kind, o.query)
	if err != nil {
		return err
	}
	if ok {
		return &KeyConflictError{
			Existing: o.String(),
			Message: fmt.Sprintf("invalid_argument: cannot create the %s key %s while the %s key %s exists: one consumer acting on both feeds would act twice on one CI event",
				k.kind, k, o.kind, o),
		}
	}
	return nil
}

// movedAside returns the store's move-aside marker when it is in force: set,
// parsable, not in the future and younger than ConsumerStaleAfter. A missing,
// unparsable or future marker counts as absent.
func (f *Feed) movedAside(ctx context.Context, now time.Time) (time.Time, bool, error) {
	v, ok, err := f.cfg.Store.GetMeta(ctx, MovedAsideKey)
	if err != nil {
		return time.Time{}, false, err
	}
	if !ok {
		return time.Time{}, false, nil
	}
	at, perr := time.Parse(time.RFC3339Nano, v)
	if perr != nil || at.After(now) || now.Sub(at) >= f.cfg.ConsumerStaleAfter {
		return time.Time{}, false, nil
	}
	return at, true, nil
}

// checkMovedAside answers a key the move-aside marker covers: a key that does
// not exist, or whose last sighting predates the marker (a key of the store that
// was moved aside), unless the call is a Reset.
func (f *Feed) checkMovedAside(ctx context.Context, k keyID, now time.Time, exists bool, seen time.Time, reset bool) error {
	if reset {
		return nil
	}
	at, ok, err := f.movedAside(ctx, now)
	if err != nil || !ok {
		return err
	}
	if !exists || seen.Before(at) {
		return &CursorExpiredError{Message: fmt.Sprintf("cursor_expired: the store of this daemon was moved aside at %s, so changes after the position of %s %s are unknown; re-run with --reset",
			at.UTC().Format(time.RFC3339), k.kind, k)}
	}
	return nil
}

// expired reports whether a key at pos can no longer be served from the log: a
// gap below the oldest retained row (the dense sequence numbers expose it), or
// an empty log whose head is above pos. A key at or past the head never expires.
func (f *Feed) expired(ctx context.Context, pos, head int64) (bool, error) {
	if pos >= head {
		return false, nil
	}
	oldest, ok, err := f.cfg.Store.ChangeOldest(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return pos < oldest-1, nil
}

func (f *Feed) expiredError(k keyID, pos int64) error {
	return &CursorExpiredError{Message: fmt.Sprintf("cursor_expired: the position %d of %s %s is outside the retained change log (change_log_retention %s); re-run with --reset",
		pos, k.kind, k, f.cfg.Retention)}
}

// NoteMovedAside records that the daemon started on a fresh store because the
// previous one was moved aside at at. For consumer_stale_after the first poll or
// ack of every key that does not exist is answered cursor_expired: until it
// Resets, rather than silently starting at the tail and skipping what changed in
// between.
func (f *Feed) NoteMovedAside(ctx context.Context, at time.Time) error {
	return f.cfg.Store.PutMeta(ctx, MovedAsideKey, at.UTC().Format(time.RFC3339Nano))
}

var errNoStore = errors.New("feed: a store is required")
