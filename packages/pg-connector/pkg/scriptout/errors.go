package scriptout

import (
	"errors"
	"fmt"
)

// The six Err* sentinels are the Go-side mapping of the wire protocol's
// closed error-code taxonomy (not_found, unauthenticated, unavailable,
// unknown_op, version_mismatch, invalid_argument). A handler wraps one of
// these via WrapError so callers use errors.Is rather than
// substring-matching, the same pattern vcs.ErrAuthInvalid already
// establishes in packages/pg-pr.
//
// ErrInvalidArgument was added (bead pg2-r9iok, design §4.2) to close a real
// gap: without it, a caller-input-validation failure (an empty required
// field, a malformed id) had nowhere to route except ErrUnavailable, whose
// own doc comment below defines it as "this backend cannot currently be
// used" — actively misleading for a problem that is the CALLER's fault, not
// the backend's health. §4.2 already scoped its wire-code set as "at least"
// these five, leaving room for exactly this addition. It does not touch
// pg-connector's own CLI exit-code scheme (§4.5): a targeted op still exits
// 1 for invalid_argument, the same as every other non-not_found code — only
// the wire body's error.code (and the Go sentinel a caller can errors.Is
// against) gains the extra precision.
//
// ExitCodeForError/ExitCodeForCode (bead pg2-7vgn5) are the separate,
// lower-layer widening: the backend PROCESS's own exit code (serve.go's
// writeErrorResponse) now also carries this same six-way classification,
// rather than a plain 1 for every failure. See ExitCodeForError's own doc
// comment for why this is layered on top of — not a replacement for — the
// wire body's error.code.
var (
	ErrNotFound        = errors.New("scriptout: not found")
	ErrUnauthenticated = errors.New("scriptout: unauthenticated")
	ErrUnavailable     = errors.New("scriptout: unavailable")
	ErrUnknownOp       = errors.New("scriptout: unknown op")
	ErrVersionMismatch = errors.New("scriptout: version mismatch")
	ErrInvalidArgument = errors.New("scriptout: invalid argument")
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
	"invalid_argument": ErrInvalidArgument,
}

// WrapError wraps message with sentinel (via fmt.Errorf("%w: %s", ...)) so
// errors.Is(err, sentinel) holds. sentinel should be one of the six Err*
// values above.
func WrapError(sentinel error, message string) error {
	return fmt.Errorf("%w: %s", sentinel, message)
}

// codeForError maps a Go error to its wire code by walking the six known
// sentinels with errors.Is. An error matching none of them (a handler
// returned a plain, unwrapped error) falls back to "unavailable" — a
// freedom-boundary choice: it is the taxonomy's closest fit for "something
// went wrong and this backend cannot currently be used," which keeps every
// wire response's error.code within the closed six-value set even when a
// handler forgot to wrap its error. A handler that means "the caller's
// input was bad" MUST wrap ErrInvalidArgument explicitly — that meaning is
// never inferred from an unwrapped error.
func codeForError(err error) string {
	for _, code := range []string{"not_found", "unauthenticated", "unavailable", "unknown_op", "version_mismatch", "invalid_argument"} {
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

// exitCodeForCode maps each wire-taxonomy code to the backend process's own
// exit code (bead pg2-7vgn5). Values start at 2, per this workspace's
// code-file-standards exit-code convention: exit 1 is the
// generic/catch-all failure and MUST NOT be given a specific branchable
// meaning, so every code that DOES carry one here is >=2. The specific
// assignment (not_found=2 .. invalid_argument=7) follows codeToSentinel's
// own declared order — there is no severity ranking among the six wire
// codes to assign by, so the order is simply "the one place the taxonomy
// is already enumerated," keeping this table and codeToSentinel trivially
// comparable.
var exitCodeForCode = map[string]int{
	"not_found":        2,
	"unauthenticated":  3,
	"unavailable":      4,
	"unknown_op":       5,
	"version_mismatch": 6,
	"invalid_argument": 7,
}

// ExitCodeForError returns the backend-process-level exit code
// writeErrorResponse (serve.go) uses for err: the code from exitCodeForCode
// matching err's wire-taxonomy classification, computed through the exact
// same codeForError walk the JSON error body's Code field uses — so a
// response's process exit code and its wire error.code can never disagree.
// codeForError is total (every error, wrapped or not, resolves to one of
// the six known codes via its own "unavailable" fallback), so this lookup
// always succeeds.
//
// This is what widens scriptout's backend-process exit code past the
// historical plain 0/1 to satisfy this workspace's code-file-standards
// exit-code convention (bead pg2-7vgn5): a caller that wants to branch on
// the failure category without parsing stdout JSON now can. Nothing
// requires a caller to do so — pkg/scriptout's own exec.go deliberately
// continues to treat only the JSON body as the contract (see its package
// doc comment) — this only widens what the exit code ITSELF is capable of
// expressing.
//
// A wire-level failure with no classifiable error at all (serve.go's own
// response-write failure, where there is no err to classify) does not go
// through this function and stays at the generic exit 1.
func ExitCodeForError(err error) int {
	return ExitCodeForCode(codeForError(err))
}

// ExitCodeForCode returns the backend-process exit code for a bare wire
// taxonomy code string (one of codeToSentinel's six keys) — the same
// mapping ExitCodeForError uses, exported so a caller holding only the
// JSON error body's Code field (e.g. pkg/scriptout/conformance, checking a
// live backend's reply with no Go error value to classify) can compute the
// expected exit code without one. An unrecognized code (should not happen
// against a well-behaved backend) returns 0 — the zero value, distinguishable
// from every real assignment (1-7) a well-formed error response can produce.
func ExitCodeForCode(code string) int {
	return exitCodeForCode[code]
}
