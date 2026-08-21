package snapshot

import "time"

// MergedRetentionWindow is how long a merged PR of MINE lingers in the
// snapshot's Mine panel after merging — sorted below every active row and
// marked MineRow.Merged for de-emphasis — before it is dropped automatically.
// Recomputed fresh every time it is checked (Build, and the daemon's
// snapshot-owner pruning) against the CURRENT clock: there is no persisted
// "seen"/dismissed state, per pg2-ew4kf.
const MergedRetentionWindow = 24 * time.Hour

// WithinMergedRetention reports whether a PR merged at mergedAt (RFC3339,
// pkg/api.PR.MergedAt) is still inside MergedRetentionWindow as of now —
// i.e. now.Sub(mergedAt) < MergedRetentionWindow.
//
// An empty or unparsable mergedAt (no merge timestamp available) reports
// false: a merged PR whose merge instant is unknown fails safe to the
// pre-pg2-ew4kf behavior (immediate removal) rather than being retained
// indefinitely.
func WithinMergedRetention(mergedAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, mergedAt)
	if err != nil {
		return false
	}
	return now.Sub(t) < MergedRetentionWindow
}
