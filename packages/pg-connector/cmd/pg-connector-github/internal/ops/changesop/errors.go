package changesop

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// wireSentinels are the closed seven-value wire taxonomy.
var wireSentinels = []error{
	scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrUnavailable, scriptout.ErrUnknownOp,
	scriptout.ErrVersionMismatch, scriptout.ErrInvalidArgument, scriptout.ErrQueryNotRecognized,
}

func isWire(err error) bool {
	for _, s := range wireSentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// classified is an error on the wire taxonomy: it is its sentinel (errors.Is),
// wraps its cause, and reads as its message.
type classified struct {
	sentinel error
	msg      string
	cause    error
}

func (e *classified) Error() string { return e.msg }

func (e *classified) Unwrap() []error {
	if e.cause == nil {
		return []error{e.sentinel}
	}
	return []error{e.sentinel, e.cause}
}

// mapError puts an error of the feed (or of a query run it made) on the closed
// wire taxonomy; op names the op in the message of a failure that had no
// message of its own for the caller.
//
//   - What already is on the taxonomy passes unchanged: a feed.CursorExpiredError
//     (its message begins "cursor_expired:") and a feed.KeyConflictError (its
//     message names the existing key) are invalid_argument, feed.ErrAckBeyondHead
//     is invalid_argument, feed.ErrQueryNotRecognized is query_not_recognized.
//   - A *governor.Refusal is unavailable.
//   - An error matching vcs.ErrAuthInvalid is unauthenticated.
//   - Anything else (a failed query run, a store error, a context that ended) is
//     unavailable.
func mapError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case isWire(err):
		return err
	}
	var refusal *governor.Refusal
	switch {
	case errors.As(err, &refusal):
		return &classified{sentinel: scriptout.ErrUnavailable, msg: fmt.Sprintf("%s: %s", op, refusal.Reason), cause: err}
	case errors.Is(err, vcs.ErrAuthInvalid):
		return &classified{sentinel: scriptout.ErrUnauthenticated, msg: fmt.Sprintf("%s: %v", op, err), cause: err}
	}
	return &classified{sentinel: scriptout.ErrUnavailable, msg: fmt.Sprintf("%s: %v", op, err), cause: err}
}

func invalid(format string, args ...any) error {
	return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf(format, args...))
}
