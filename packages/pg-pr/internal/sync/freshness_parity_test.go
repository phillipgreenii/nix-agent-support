package sync

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
)

// TestFreshnessDefaultIntervalParity pins freshness.DefaultSyncIntervalSeconds
// to DefaultDaemonInterval. The freshness package cannot import this one (this
// package imports snapshot, which imports freshness — the reverse edge would
// cycle), so the fallback cadence is DUPLICATED as a constant there. This test
// is the anti-drift lock: if the daemon's default tick changes, the fallback
// bound used by `pg-pr pr list` (and by any snapshot built outside daemon mode)
// must change with it, or both surfaces would judge staleness against a cadence
// pg-pr no longer runs at.
func TestFreshnessDefaultIntervalParity(t *testing.T) {
	want := int(DefaultDaemonInterval.Seconds())
	if freshness.DefaultSyncIntervalSeconds != want {
		t.Errorf("freshness.DefaultSyncIntervalSeconds = %d, but sync.DefaultDaemonInterval is %ds (%v) — keep them in sync",
			freshness.DefaultSyncIntervalSeconds, want, DefaultDaemonInterval)
	}
}
