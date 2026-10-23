package groups

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// ErrGone is the Fetch error for a PR that is deleted or no longer accessible;
// the engine turns it into a tombstone. It is the shared sentinel itself, so a
// fetcher that returns it tombstones through the shared group refresh service.
var ErrGone = groupsvc.ErrGone

// Target is one PR a Fetcher is asked to fetch a group for.
type Target struct {
	NodeID string
	Ref    entity.Ref
	// Stamps are the head and base of the current summary.
	Stamps Stamps
	// HeadRefName is the PR's head branch, which the runs group needs.
	HeadRefName string
	// State is the PR's state as GitHub spells it.
	State string
	// Terminal is true for a merged or closed PR.
	Terminal bool
}

// Outcome is the result of fetching a group for one Target.
type Outcome struct {
	Content     []byte
	Fingerprint string
	// Stamps are the head and base the content describes.
	Stamps Stamps
	// FreshUntil, when non-zero, is the group's own content-dependent bound and
	// replaces Rules.Schedule's fresh_until (only the runs group uses it).
	FreshUntil time.Time
	// LastError, when non-empty, is a NON-FATAL problem the engine stores in the
	// row's last_error together with the new content (only the runs group uses
	// it, for a failed job fetch).
	LastError string
	// Cost is what the fetch spent, in the governor's unit for its bucket.
	Cost int64
	// Err, when non-nil, means nothing is stored; ErrGone marks a deleted PR.
	Err error
}

// Fetcher fetches one group for batches of PRs. The group packets implement it
// and the refresh engine runs it.
type Fetcher interface {
	// Group is the group name this fetcher produces.
	Group() string
	// MaxBatch is the most targets one Fetch call takes.
	MaxBatch() int
	// Spend is the governor request for fetching n targets as the class.
	Spend(class governor.Class, n int) governor.Request
	// Fetch fetches the group for every target and returns one Outcome per
	// Target, in order.
	Fetch(ctx context.Context, call ratefeed.Meta, targets []Target) []Outcome
}
