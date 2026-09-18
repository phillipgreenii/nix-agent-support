package wire

import (
	"errors"
	"fmt"
)

// The Err* sentinels are the Go-side mapping of the wire protocol's closed
// error-code taxonomy, reused unchanged from pg-connector's own six-value
// set (this repo's ADR 0062 principle 3: not_found, unauthenticated,
// unavailable, unknown_op, version_mismatch, invalid_argument). A handler
// wraps one of these via WrapError so a caller can use errors.Is rather
// than substring-matching a message.
//
// unknown_op also covers "unknown service": a request names a service this
// daemon has no handler registered for, which is indistinguishable in kind
// from naming an op a known service doesn't implement — both are "this
// (service, op) pair is not one this daemon answers" — so it does not need
// an eighth taxonomy member of its own.
var (
	ErrNotFound        = errors.New("wire: not found")
	ErrUnauthenticated = errors.New("wire: unauthenticated")
	ErrUnavailable     = errors.New("wire: unavailable")
	ErrUnknownOp       = errors.New("wire: unknown op")
	ErrVersionMismatch = errors.New("wire: version mismatch")
	ErrInvalidArgument = errors.New("wire: invalid argument")
)

// codeToSentinel maps every wire code in the closed taxonomy to its Go
// sentinel. codeForError and sentinelForCode both derive from this one
// table so the two directions of the mapping can never drift apart.
var codeToSentinel = map[string]error{
	"not_found":        ErrNotFound,
	"unauthenticated":  ErrUnauthenticated,
	"unavailable":      ErrUnavailable,
	"unknown_op":       ErrUnknownOp,
	"version_mismatch": ErrVersionMismatch,
	"invalid_argument": ErrInvalidArgument,
}

// codeOrder is the taxonomy's declared order, walked by codeForError so the
// classification is deterministic when an error happens to wrap more than
// one sentinel.
var codeOrder = []string{
	"not_found",
	"unauthenticated",
	"unavailable",
	"unknown_op",
	"version_mismatch",
	"invalid_argument",
}

// WrapError wraps message with sentinel (via fmt.Errorf("%w: %s", ...)) so
// errors.Is(err, sentinel) holds. sentinel should be one of the six Err*
// values above.
func WrapError(sentinel error, message string) error {
	return fmt.Errorf("%w: %s", sentinel, message)
}

// codeForError maps a Go error to its wire code by walking the six known
// sentinels with errors.Is, in codeOrder. An error matching none of them
// (a handler returned a plain, unwrapped error — e.g. a real EventKit
// failure with no more specific classification) falls back to
// "unavailable": the taxonomy's closest fit for "something went wrong and
// this service cannot currently be used."
func codeForError(err error) string {
	for _, code := range codeOrder {
		if errors.Is(err, codeToSentinel[code]) {
			return code
		}
	}
	return "unavailable"
}

// ErrorResponse builds a Response carrying a wire-taxonomy error envelope
// derived from err. SchemaVersion is left at its zero value; the caller
// (socketserver) fills it in from the responding service once known.
func ErrorResponse(err error) *Response {
	return &Response{
		ProtocolVersion: ProtocolVersion,
		Error: &ErrorBody{
			Code:    codeForError(err),
			Message: err.Error(),
		},
	}
}
