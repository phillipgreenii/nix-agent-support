package sync

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// OldestAnchorCheckAge returns the age in seconds of the stalest applied
// anchor check: the oldest last_synced_at among ledger rows with
// kind=anchor, a non-empty bead_id (a planned row has no bead to be stale
// against), and a last_synced_content_hash other than the closed sentinel.
// 0 when there is no such row, or when the store has no ledger.
//
// This is the staleness signal for the Anchor rule's unchanged-check path
// (bead pg2-u4c1s): an unchanged check no longer touches the anchor BEAD, it
// only advances the ledger row's last_synced_at, so a stalled sync shows up
// here instead of on the bead. It backs the
// pg_desk_oldest_anchor_check_age_seconds gauge and the
// oldest_anchor_check_age_seconds line of `pg-desk status`.
//
// The ledger exists only on the old schema (version 1; the cutover drops
// it), so any other version reports 0 rather than querying a missing table.
func OldestAnchorCheckAge(st *store.Store, now time.Time) (int, error) {
	version, err := st.SchemaVersion()
	if err != nil {
		return 0, err
	}
	if version != 1 {
		return 0, nil
	}
	rows, err := st.ListLedger()
	if err != nil {
		return 0, err
	}
	return OldestAnchorCheckAgeOf(rows, now), nil
}

// OldestAnchorCheckAgeOf is OldestAnchorCheckAge's pure core over an
// already-read ledger. A row whose last_synced_at is empty or unparseable
// is skipped; an age that would be negative (clock skew) counts as 0.
func OldestAnchorCheckAgeOf(rows []store.LedgerEntry, now time.Time) int {
	oldest := 0
	for _, l := range rows {
		if l.Kind != KindAnchor || l.BeadID == "" || l.LastSyncedContentHash == ClosedSentinel {
			continue
		}
		t, err := time.Parse(time.RFC3339, l.LastSyncedAt)
		if err != nil {
			continue
		}
		if age := int(now.Sub(t).Seconds()); age > oldest {
			oldest = age
		}
	}
	return oldest
}
