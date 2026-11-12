package pipeline

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// Failure classes recorded with a failed-ingest entry. The values are the
// connector's wire error codes (nix-agent-support ADR 0062, principle 3: the
// closed taxonomy not_found, unauthenticated, unavailable, unknown_op,
// version_mismatch, invalid_argument). A local call deadline is classified
// unavailable, because gather reports it as ErrUnavailable. A failure that
// carries no connector class (an interpret or link failure, a lost version
// race) has the empty class.
const (
	FailureClassNotFound        = "not_found"
	FailureClassInvalidArgument = "invalid_argument"
	FailureClassUnauthenticated = "unauthenticated"
	FailureClassUnavailable     = "unavailable"
)

// failureClassOf returns the typed class of a hydration error. It reads the
// error's type (gather.ErrorCode), never its message text.
func failureClassOf(err error) string {
	return gather.ErrorCode(err)
}

// PermanentFailureClass reports whether a failure of this class can never
// succeed on a retry, so its entry is flagged instead of retried:
//
//   - not_found: the entity is gone; it will never hydrate.
//   - invalid_argument: the connector rejected the request built from the
//     entity id as malformed; the same id sends the same request.
//
// Every other class is retried at every poll, including unauthenticated (the
// operator restoring the credential makes the same row succeed), unknown_op
// and version_mismatch (a connector upgrade fixes them), unavailable and a
// local timeout (the backend comes back), and the empty class (an unclassified
// failure is never assumed permanent).
func PermanentFailureClass(class string) bool {
	return class == FailureClassNotFound || class == FailureClassInvalidArgument
}
