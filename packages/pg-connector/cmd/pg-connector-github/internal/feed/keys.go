package feed

import (
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
)

// The two cursor-key kinds, which are the two views of the GitHub registration.
const (
	// KindPR is the PR feed (`pr changes`).
	KindPR = "pr"
	// KindCI is the CI view (`ci changes`).
	KindCI = "ci"
)

// MovedAsideKey is the store_meta key NoteMovedAside writes: an RFC 3339 time.
const MovedAsideKey = shared.MovedAsideKey

// ErrQueryNotRecognized is the existing wire code query_not_recognized: the
// query name is not one the daemon is configured with.
var ErrQueryNotRecognized = shared.ErrQueryNotRecognized

// ErrAckBeyondHead is returned (wrapped) by Ack for a next_seq above the head of
// the change log. It is an invalid_argument.
var ErrAckBeyondHead = shared.ErrAckBeyondHead

type (
	// CursorExpiredError answers a key whose position is no longer served. Its
	// Message begins "cursor_expired:" and tells the caller to re-run with
	// --reset; it is an invalid_argument.
	CursorExpiredError = shared.CursorExpiredError
	// KeyConflictError refuses the creation of a key while the same consumer holds
	// the other kind's key for the same query; it is an invalid_argument.
	KeyConflictError = shared.KeyConflictError
)
