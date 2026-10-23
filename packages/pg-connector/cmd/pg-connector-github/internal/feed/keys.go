package feed

import (
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
)

// The two views of the GitHub registration, which a consumer's cursor is bound to.
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
	// CursorExpiredError answers a cursor whose position is no longer served. Its
	// Message begins "cursor_expired:" and tells the caller to re-run with
	// --reset; it is an invalid_argument.
	CursorExpiredError = shared.CursorExpiredError
	// KeyConflictError refuses a call that names another view than the one the
	// consumer's cursor is bound to; it is an invalid_argument.
	KeyConflictError = shared.KeyConflictError
	// ScopeConflictError refuses a call that names another scope than the one the
	// consumer's cursor is bound to, unless the call is a Reset; it is an
	// invalid_argument.
	ScopeConflictError = shared.ScopeConflictError
)
