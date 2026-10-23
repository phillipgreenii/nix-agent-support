package feed

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// MovedAsideKey is the store_meta key NoteMovedAside writes: an RFC 3339 time.
const MovedAsideKey = store.MovedAsideKey

// ErrQueryNotRecognized is the wire code query_not_recognized: the query name is
// not one the daemon is configured with. It is the scriptout sentinel itself, so
// the ops map it with the rest of the closed taxonomy.
var ErrQueryNotRecognized = scriptout.ErrQueryNotRecognized

// ErrAckBeyondHead is returned (wrapped) by Ack for a next_seq above the head of
// the change log. It is an invalid_argument.
var ErrAckBeyondHead = newAckBeyondHead()

func newAckBeyondHead() error {
	return scriptout.WrapError(scriptout.ErrInvalidArgument, "ack beyond the head of the change log")
}

// CursorExpiredError answers a cursor whose position is no longer served. Its
// Message begins "cursor_expired:" and tells the caller to re-run with --reset;
// it is an invalid_argument.
type CursorExpiredError struct{ Message string }

func (e *CursorExpiredError) Error() string { return e.Message }

// Is makes errors.Is(err, scriptout.ErrInvalidArgument) hold.
func (e *CursorExpiredError) Is(target error) bool { return target == scriptout.ErrInvalidArgument }

// KeyConflictError refuses the creation of a cursor while the same consumer
// holds the cursor of another view for the same scope; it is an
// invalid_argument. Existing names the cursor that exists, as "(consumer, view,
// scope)".
type KeyConflictError struct{ Existing, Message string }

func (e *KeyConflictError) Error() string { return e.Message }

// Is makes errors.Is(err, scriptout.ErrInvalidArgument) hold.
func (e *KeyConflictError) Is(target error) bool { return target == scriptout.ErrInvalidArgument }

// keyID is a cursor key.
type keyID struct{ consumer, view, scope string }

func (k keyID) String() string { return "(" + k.consumer + ", " + k.view + ", " + k.scope + ")" }

// keyLocks serialises the calls of one cursor without growing: an entry exists
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

// resolveView returns the registered view named name; the empty name is the
// default view, Spec.Views[0].
func (f *Feed) resolveView(name string) (registry.View, error) {
	if name == "" {
		return f.cfg.Spec.Views[0], nil
	}
	for _, v := range f.cfg.Spec.Views {
		if v.Name == name {
			return v, nil
		}
	}
	return registry.View{}, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("feed view %q is not one of the registered views", name))
}

// validate checks the request fields every call shares and returns the view.
func (f *Feed) validate(view, consumer, scope string) (registry.View, error) {
	v, err := f.resolveView(view)
	switch {
	case err != nil:
		return registry.View{}, err
	case consumer == "":
		return registry.View{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "a consumer name is required")
	case scope == "":
		return registry.View{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "a query name is required")
	}
	return v, nil
}

// checkKnown refuses a scope the daemon does not know.
func (f *Feed) checkKnown(scope string) error {
	if !f.cfg.Queries.Known(scope) {
		return fmt.Errorf("%w: %q is not a configured query", ErrQueryNotRecognized, scope)
	}
	return nil
}

// checkConflict refuses the creation of k while the same consumer holds the
// cursor of any OTHER registered view for the same scope.
func (f *Feed) checkConflict(ctx context.Context, k keyID) error {
	for _, v := range f.cfg.Spec.Views {
		if v.Name == k.view {
			continue
		}
		o := keyID{k.consumer, v.Name, k.scope}
		_, ok, err := f.cfg.Store.GetConsumer(ctx, o.consumer, o.view, o.scope)
		if err != nil {
			return err
		}
		if ok {
			return &KeyConflictError{
				Existing: o.String(),
				Message: fmt.Sprintf("invalid_argument: cannot create the %s key %s while the %s key %s exists: %s",
					k.view, k, o.view, o, f.cfg.ConflictReason),
			}
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

// checkMovedAside answers a cursor the move-aside marker covers: a cursor that
// does not exist, or whose last sighting predates the marker (a cursor of the
// store that was moved aside), unless the call is a Reset.
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
			at.UTC().Format(time.RFC3339), k.view, k)}
	}
	return nil
}

// expired reports whether a cursor at pos can no longer be served from the log:
// a gap below the oldest retained row (the dense sequence numbers expose it), or
// an empty log whose head is above pos. A cursor at or past the head never
// expires.
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
		pos, k.view, k, f.cfg.Retention)}
}

// NoteMovedAside records that the daemon started on a fresh store because the
// previous one was moved aside at at. For consumer_stale_after the first poll or
// ack of every cursor that does not exist is answered cursor_expired: until it
// Resets, rather than silently starting at the tail and skipping what changed in
// between.
func (f *Feed) NoteMovedAside(ctx context.Context, at time.Time) error {
	return f.cfg.Store.PutMeta(ctx, MovedAsideKey, at.UTC().Format(time.RFC3339Nano))
}

// defaultConflictReason is the reason a KeyConflictError gives when the Config
// names none.
func defaultConflictReason() string {
	return "one consumer acting on both feeds would act twice on one event"
}
