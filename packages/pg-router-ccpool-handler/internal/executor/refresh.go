package executor

import (
	"context"
	"log/slog"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
)

// RefreshItem returns it with its Metadata overlaid by the bead's CURRENT
// metadata, read from bd at dispatch time (pg2-1pt7r).
//
// The Item a dispatch carries is decoded from the queued event's payload,
// which was snapshotted when the event was enqueued. A review-pr bead that is
// reopened on a head advance gets a fresh metadata.head_sha, but an event
// queued before the advance still carries the OLD head_sha, so the review
// prompt (checkout instruction + the head_sha the review pins) named a stale
// commit while the bead and the live PR were already at the new head. The bead
// is the source of truth: its metadata keys overwrite the payload's, and
// payload-only keys are kept.
//
// Only bead items are refreshed. An item whose id does not have the shape of a
// bd id (a PR item `owner/repo#N`, a heartbeat/tick item keyed by an RFC3339
// timestamp) is not a bead, so it is returned untouched with no bd call and no
// WARN; the id shape is used rather than Item.Type because Type is source
// specific (bead-list sources emit the bead's issue_type, changes/sweep sources
// emit the connector entity type). Detecting by id shape cannot skip a genuine
// bead, since bd only mints ids of that shape (pg2-rk7t9).
//
// Best effort: a bd read failure (or a bead with no metadata) leaves the
// payload item untouched and is logged, never turned into a dispatch failure —
// a stale-but-present prompt is no worse than before this refresh existed.
func RefreshItem(ctx context.Context, bd beads.Runner, it item.Item) item.Item {
	it, _ = RefreshItemIssue(ctx, bd, it)
	return it
}

// RefreshItemIssue is RefreshItem that also hands back the bead it read, so a
// caller that needs the bead's current status (PrecheckReview, pg2-5x29j) does
// not pay a second `bd show`. The returned *beads.Issue is nil exactly when no
// bead was read: a non-bead item, a nil runner, or a bd failure (already
// logged). Everything else about the item is as RefreshItem documents.
func RefreshItemIssue(ctx context.Context, bd beads.Runner, it item.Item) (item.Item, *beads.Issue) {
	if bd == nil || !beads.IsID(it.ID) {
		return it, nil
	}
	iss, err := beads.ShowObj(ctx, bd, it.ID)
	if err != nil {
		slog.Warn("dispatch: could not refresh item metadata from bd; using the event payload's", "bead", it.ID, "err", err)
		return it, nil
	}
	if len(iss.Metadata) == 0 {
		return it, &iss
	}
	merged := make(map[string]any, len(it.Metadata)+len(iss.Metadata))
	for k, v := range it.Metadata {
		merged[k] = v
	}
	for k, v := range iss.Metadata {
		merged[k] = v
	}
	it.Metadata = merged
	return it, &iss
}
