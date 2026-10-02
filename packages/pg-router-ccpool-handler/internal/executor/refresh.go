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
// Best effort: a bd read failure (or a bead with no metadata) leaves the
// payload item untouched and is logged, never turned into a dispatch failure —
// a stale-but-present prompt is no worse than before this refresh existed.
func RefreshItem(ctx context.Context, bd beads.Runner, it item.Item) item.Item {
	if bd == nil || it.ID == "" {
		return it
	}
	iss, err := beads.ShowObj(ctx, bd, it.ID)
	if err != nil {
		slog.Warn("dispatch: could not refresh item metadata from bd; using the event payload's", "bead", it.ID, "err", err)
		return it
	}
	if len(iss.Metadata) == 0 {
		return it
	}
	merged := make(map[string]any, len(it.Metadata)+len(iss.Metadata))
	for k, v := range it.Metadata {
		merged[k] = v
	}
	for k, v := range iss.Metadata {
		merged[k] = v
	}
	it.Metadata = merged
	return it
}
