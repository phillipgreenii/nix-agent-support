package detail

import (
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ErrorPolicy is the Jira reading of an origin failure, for the shared group
// refresh service (groupsvc.Config.Errors).
type ErrorPolicy struct{}

var _ groupsvc.ErrorPolicy = ErrorPolicy{}

// IsRateLimit is origin.IsRateLimited.
func (ErrorPolicy) IsRateLimit(err error) bool { return origin.IsRateLimited(err) }

// IsUnauthenticated is origin.IsUnauthenticated.
func (ErrorPolicy) IsUnauthenticated(err error) bool { return origin.IsUnauthenticated(err) }

// Classify returns the scriptout error of a failure: unauthenticated when the
// origin refused the credentials, not_found when pjira says the issue does not
// exist, unavailable for everything else (a rate-limit answer, a paused
// origin, an unreadable answer). The returned error also wraps the original, so
// IsRateLimit and IsUnauthenticated still recognise it. Classify never pauses
// the governor: the origin runner already paused it for a 429. A nil error and
// a governor refusal (the daemon's own decision not to spend, which is not an
// answer from the origin) come back unchanged.
func (ErrorPolicy) Classify(_ *governor.Governor, _ time.Duration, err error) error {
	if err == nil || errors.Is(err, governor.ErrRefused) {
		return err
	}
	sentinel := scriptout.ErrUnavailable
	switch {
	case origin.IsUnauthenticated(err):
		sentinel = scriptout.ErrUnauthenticated
	case origin.IsRateLimited(err), errors.Is(err, origin.ErrPaused):
	case errors.Is(internal.ClassifyPjiraError(err.Error()), scriptout.ErrNotFound):
		sentinel = scriptout.ErrNotFound
	}
	if errors.Is(err, sentinel) {
		return err
	}
	return fmt.Errorf("%w: %w", sentinel, err)
}
