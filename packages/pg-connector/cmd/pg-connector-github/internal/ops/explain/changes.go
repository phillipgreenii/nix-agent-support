package explain

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
)

// MaxLastChanges is how many of the entity's newest change rows an answer
// carries.
const MaxLastChanges = 5

// PendingSettle is the pending settle row of the pull request (JSON
// "pending_settle", omitted when it has none): the groups the settle barrier
// still awaits (each counts only when a fetch of it started at or after its
// since time), the instant the change was first detected, the deadline after
// which the row is ended whether or not the groups arrived, and the change
// kinds the row has accumulated so far.
type PendingSettle struct {
	Awaited    []AwaitedGroup `json:"awaited"`
	DetectedAt time.Time      `json:"detected_at"`
	Deadline   time.Time      `json:"deadline"`
	Kinds      []string       `json:"kinds,omitempty"`
}

// AwaitedGroup is one group a pending row waits for.
type AwaitedGroup struct {
	Group string    `json:"group"`
	Since time.Time `json:"since"`
}

// ChangeRef is one row of the change log in short form (JSON "last_changes",
// newest first, at most MaxLastChanges, omitted when the pull request has none).
type ChangeRef struct {
	Seq     int64     `json:"seq"`
	Version int64     `json:"version"`
	Kinds   []string  `json:"kinds"`
	Origin  string    `json:"origin,omitempty"`
	At      time.Time `json:"at"`
}

// ChangeReader is what the answer reads of the change engine's tables. The
// explain package defines it and does not import the engine or the feed: an
// adapter outside the package (internal/changeview) satisfies it from the
// store. Both methods read the store only and make no origin call. A nil
// Config.Changes leaves the two fields out.
type ChangeReader interface {
	// PendingSettle is the pending row of the entity key (the pull request's node
	// id); ok is false when it has none.
	PendingSettle(ctx context.Context, nodeID string) (p PendingSettle, ok bool, err error)
	// LastChanges are the newest n change rows of the pull request's wire
	// reference owner/repo#n, newest first.
	LastChanges(ctx context.Context, ref string, n int) ([]ChangeRef, error)
}

// fillChanges adds the change engine's part of the answer.
func (h *handler) fillChanges(ctx context.Context, res *Result, nodeID string, ref entity.Ref) error {
	if h.cfg.Changes == nil {
		return nil
	}
	p, ok, err := h.cfg.Changes.PendingSettle(ctx, nodeID)
	if err != nil {
		return err
	}
	if ok {
		res.PendingSettle = &p
	}
	rows, err := h.cfg.Changes.LastChanges(ctx, ref.String(), MaxLastChanges)
	if err != nil {
		return err
	}
	if len(rows) > MaxLastChanges {
		rows = rows[:MaxLastChanges]
	}
	res.LastChanges = rows
	return nil
}
