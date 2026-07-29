// Package freshness holds pg-pr's ONE staleness policy: the bound past which a
// store-derived read surface must flag its data stale, plus the predicate that
// applies it.
//
// Both of pg-pr's acted-on read surfaces share it — the `pg-pr pr list --json`
// seam (per-PR as-of = the store's pull_request.last_synced_at column) and the
// daemon dashboard payload (payload-level as-of = Snapshot.GeneratedAt) — so the
// two halves of the freshness contract (expose an as-of time, AND flag the data
// stale once it exceeds a bound) come from a single definition instead of being
// re-derived per seam.
//
// Policy authority: the pr-pool deployment's INV-FRESH-1 ("don't act on stale
// truth": any surface an operator or observer acts on MUST expose its own as-of
// time and flag its data as stale once that data exceeds a bound; a readiness or
// status signal derived from data past that bound MUST NOT be presented as
// current).
package freshness

import "time"

// BoundIntervals is how many refresh intervals a store-derived fact MAY age
// before it is flagged stale. Two intervals tolerates one wholly missed tick
// (clock skew, a slow provider round-trip) while still catching a wedged syncer
// on the following one. It is the same bound the dashboard's external Grafana
// "snapshot age" panel already thresholds on — "Red > sync_interval_seconds * 2"
// (docs/superpowers/plans/2026-05-26-pg-pr-dashboard.md) — so the payload flag
// and that panel cannot disagree.
const BoundIntervals = 2

// DefaultSyncIntervalSeconds is the refresh cadence assumed when a surface
// declares none. Two surfaces need it: a snapshot built outside daemon mode
// (SyncIntervalSeconds reads 0, see sync.Deps.SyncInterval), and EVERY `pg-pr pr
// list` invocation — a one-shot CLI read has no way to ask the running daemon
// what interval it ticks at.
//
// It MUST equal sync.DefaultDaemonInterval; TestFreshnessDefaultIntervalParity
// (internal/sync) pins the two together so the fallback cannot drift away from
// the cadence the daemon actually runs. It is deliberately a constant, not a
// config knob: nothing has asked to tune it, and a second knob would let the
// two surfaces disagree.
const DefaultSyncIntervalSeconds = 60

// BoundSeconds returns the staleness bound, in seconds, for a surface whose
// declared refresh cadence is syncIntervalSeconds. A non-positive (undeclared)
// cadence falls back to DefaultSyncIntervalSeconds, so a surface can never end
// up with a zero bound that flags every read stale.
func BoundSeconds(syncIntervalSeconds int) int {
	if syncIntervalSeconds <= 0 {
		syncIntervalSeconds = DefaultSyncIntervalSeconds
	}
	return syncIntervalSeconds * BoundIntervals
}

// IsStale reports whether data whose as-of time is asOf has aged past
// boundSeconds as of now.
//
// A ZERO asOf is stale by definition: a surface that cannot say how old its
// data is MUST NOT present that data as current. This is what makes an absent
// or unparseable as-of fail CLOSED rather than silently reading as "fresh".
//
// A FUTURE asOf (forward clock skew) is not stale — negative age is fresh.
func IsStale(asOf, now time.Time, boundSeconds int) bool {
	if asOf.IsZero() {
		return true
	}
	return now.Sub(asOf) > time.Duration(boundSeconds)*time.Second
}

// AgeSeconds returns the whole seconds of age between asOf and now, floored at
// 0 so a future as-of (clock skew) reads as 0 rather than negative. A zero asOf
// has an UNKNOWN age and also reports 0 — read the IsStale flag, not the age,
// to judge such a surface.
func AgeSeconds(asOf, now time.Time) int {
	if asOf.IsZero() {
		return 0
	}
	d := now.Sub(asOf)
	if d < 0 {
		return 0
	}
	return int(d.Seconds())
}

// ParseAsOf parses a stored as-of timestamp in the format the store writes
// (RFC3339 UTC, see store.nowRFC3339). An empty or malformed value yields the
// ZERO time — which IsStale treats as stale — so a caller can thread the result
// straight through without a separate error branch.
func ParseAsOf(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
