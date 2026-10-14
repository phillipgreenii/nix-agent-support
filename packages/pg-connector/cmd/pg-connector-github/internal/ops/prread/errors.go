package prread

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// pendingPrefix begins every message of a review_pending lookup failure, so a
// caller can report it as reason detection_failed (INV-ERR-1 of the old backend).
const pendingPrefix = "review_pending: detection_failed: "

// taxonomy is the closed set of wire sentinels (scriptout's seven codes).
var taxonomy = []error{
	scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrUnavailable,
	scriptout.ErrUnknownOp, scriptout.ErrVersionMismatch, scriptout.ErrInvalidArgument,
	scriptout.ErrQueryNotRecognized,
}

func inTaxonomy(err error) bool {
	for _, s := range taxonomy {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// wire maps a failure of the group engine or of an origin call onto the closed
// taxonomy through metered.WireError: a governor refusal is unavailable naming
// the cap or the reserve, a rate-limit answer unavailable, an authentication
// failure unauthenticated, a missing PR not_found. Anything else (a deadline,
// a transport failure) is unavailable with its own message.
func wire(err error) error {
	mapped := metered.WireError(err)
	if inTaxonomy(mapped) {
		return mapped
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, mapped.Error())
}

// engineError maps an error of groupsvc.Service.Ensure itself (not a group's
// failure): a PR that does not exist is not_found, a malformed reference
// invalid_argument.
func engineError(id string, err error) error {
	switch {
	case errors.Is(err, groupsvc.ErrNoSuchPR):
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("no such pull request: %s", id))
	case errors.Is(err, groupsvc.ErrBadRef):
		return scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("id %q is not in \"<owner>/<repo>#<number>\" form", id))
	}
	return wire(err)
}

// groupError is the error of an Unavailable group: the cause behind it, mapped.
func groupError(group string, err error) error {
	if err == nil {
		err = fmt.Errorf("the %s group could not be fetched", group)
	}
	return wire(err)
}

// pendingError maps a failed pending lookup like the old
// classifyPendingReviewError: auth to unauthenticated, a missing PR to
// not_found, anything else to unavailable, every message beginning
// "review_pending: detection_failed:". No new code is introduced.
func pendingError(err error) error {
	if err == nil {
		err = errors.New("the pending group could not be fetched")
	}
	msg := pendingPrefix + err.Error()
	var classified error
	switch {
	case errors.Is(err, groupsvc.ErrNoSuchPR), errors.Is(err, groupsvc.ErrBadRef):
		classified = scriptout.ErrNotFound
	default:
		w := wire(err)
		switch {
		case errors.Is(w, scriptout.ErrNotFound):
			classified = scriptout.ErrNotFound
		case errors.Is(w, scriptout.ErrUnauthenticated):
			classified = scriptout.ErrUnauthenticated
		default:
			classified = scriptout.ErrUnavailable
		}
	}
	return scriptout.WrapError(classified, msg)
}
