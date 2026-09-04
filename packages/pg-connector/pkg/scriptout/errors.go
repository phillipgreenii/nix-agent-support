package scriptout

import (
	"errors"
	"fmt"
)

// The five Err* sentinels are the Go-side mapping of the wire protocol's
// closed error-code taxonomy (not_found, unauthenticated, unavailable,
// unknown_op, version_mismatch). A handler wraps one of these via WrapError
// so callers use errors.Is rather than substring-matching, the same pattern
// vcs.ErrAuthInvalid already establishes in packages/pg-pr.
var (
	ErrNotFound        = errors.New("scriptout: not found")
	ErrUnauthenticated = errors.New("scriptout: unauthenticated")
	ErrUnavailable     = errors.New("scriptout: unavailable")
	ErrUnknownOp       = errors.New("scriptout: unknown op")
	ErrVersionMismatch = errors.New("scriptout: version mismatch")
)

// codeToSentinel maps every wire code in the closed taxonomy to its Go
// sentinel. Both codeForError and sentinelForCode derive from this single
// table so the two directions of the mapping can never drift apart.
var codeToSentinel = map[string]error{
	"not_found":        ErrNotFound,
	"unauthenticated":  ErrUnauthenticated,
	"unavailable":      ErrUnavailable,
	"unknown_op":       ErrUnknownOp,
	"version_mismatch": ErrVersionMismatch,
}

// WrapError wraps message with sentinel (via fmt.Errorf("%w: %s", ...)) so
// errors.Is(err, sentinel) holds. sentinel should be one of the five Err*
// values above.
func WrapError(sentinel error, message string) error {
	return fmt.Errorf("%w: %s", sentinel, message)
}

// codeForError maps a Go error to its wire code by walking the five known
// sentinels with errors.Is. An error matching none of them (a handler
// returned a plain, unwrapped error) falls back to "unavailable" — a
// freedom-boundary choice: it is the taxonomy's closest fit for "something
// went wrong and this backend cannot currently be used," which keeps every
// wire response's error.code within the closed five-value set even when a
// handler forgot to wrap its error.
func codeForError(err error) string {
	for _, code := range []string{"not_found", "unauthenticated", "unavailable", "unknown_op", "version_mismatch"} {
		if errors.Is(err, codeToSentinel[code]) {
			return code
		}
	}
	return "unavailable"
}

// sentinelForCode maps a wire code back to its Go sentinel. An unrecognized
// code (should not happen against a well-behaved backend, since the
// taxonomy is closed) falls back to ErrUnavailable.
func sentinelForCode(code string) error {
	if s, ok := codeToSentinel[code]; ok {
		return s
	}
	return ErrUnavailable
}
