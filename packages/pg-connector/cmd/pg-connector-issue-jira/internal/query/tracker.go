package query

import (
	"context"

	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
)

// tracker is the Jira implementation of the shared query.Tracker.
type tracker struct{}

var _ lq.Tracker = tracker{}

// NewTracker returns the Tracker of the Jira daemon: every listed issue already
// has an entity row because the lister persisted it, so Track has nothing to do
// and returns no unresolved reference.
func NewTracker() lq.Tracker { return tracker{} }

// Track implements lq.Tracker.
func (tracker) Track(context.Context, []string) ([]string, error) { return nil, nil }
