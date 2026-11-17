//go:build soak

package store

import "testing"

// Scale version of the first-collection check (V-4: load-shaped, so soak tag only;
// run by the pg-claude-code-recorder-soak check): the concurrent writer must see
// no failed write and no wait near the busy_timeout over 30,000 rows. It does not
// prove the analyzeStep fix by itself: a full ANALYZE of 30,000 rows (and of
// 1,000,000, measured) finishes inside the limit on a fast host, so the test
// passes with the sampling and the step timeout removed. Those are guarded by
// TestCollectStatsSamplesIndexesInsteadOfScanningWhole and
// TestCollectStatsSkipsSlowStepWithoutStamping.
func TestSoakFirstCollectionDoesNotBlockWriterNearBusyTimeout(t *testing.T) {
	firstCollectionWriterWait(t, 30000)
}
