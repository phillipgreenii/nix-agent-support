package groupsvc

import (
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// ErrorPolicy is the GitHub reading of an origin failure, for the shared group
// refresh service (sharedsvc.Config.Errors): the rate-limit rules of metered and
// the authentication sentinel of vcs.
type ErrorPolicy struct{}

var _ sharedsvc.ErrorPolicy = ErrorPolicy{}

// Classify is metered.Classify: a rate-limit answer pauses the background
// classes and becomes a typed error that is not an authentication failure.
func (ErrorPolicy) Classify(g *governor.Governor, fallbackPause time.Duration, err error) error {
	return metered.Classify(g, fallbackPause, err)
}

// IsRateLimit is metered.IsRateLimit.
func (ErrorPolicy) IsRateLimit(err error) bool { return metered.IsRateLimit(err) }

// IsUnauthenticated reports an answer that wraps vcs.ErrAuthInvalid.
func (ErrorPolicy) IsUnauthenticated(err error) bool { return errors.Is(err, vcs.ErrAuthInvalid) }
