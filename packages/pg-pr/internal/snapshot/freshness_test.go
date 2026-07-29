package snapshot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
)

// TestBuild_StaleAfterSecondsFromInterval: the freshness BOUND is derived from
// the declared sync cadence at build time, and an undeclared (zero) cadence —
// a snapshot built outside daemon mode — still gets a positive bound rather
// than a zero one that would flag every read stale.
func TestBuild_StaleAfterSecondsFromInterval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval int
		want     int
	}{
		{"declared 60s cadence", 60, 120},
		{"declared 10m cadence", 600, 1200},
		{"undeclared cadence falls back", 0, freshness.DefaultSyncIntervalSeconds * freshness.BoundIntervals},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := Build(BuilderInput{
				GeneratedAt:         time.Unix(1700000000, 0).UTC(),
				SyncIntervalSeconds: tc.interval,
			})
			if snap.StaleAfterSeconds != tc.want {
				t.Errorf("StaleAfterSeconds = %d, want %d", snap.StaleAfterSeconds, tc.want)
			}
			// A freshly built snapshot carries no verdict: staleness is a
			// serve-time question (WithFreshness), not a build-time one.
			if snap.Stale || snap.AgeSeconds != 0 {
				t.Errorf("a just-built snapshot must carry no staleness verdict, got stale=%v age=%d",
					snap.Stale, snap.AgeSeconds)
			}
		})
	}
}

// TestWithFreshness stamps the serve-time half of the freshness contract:
// age + stale judged against the payload's own declared bound, on a COPY so
// concurrent readers each get their own verdict.
func TestWithFreshness(t *testing.T) {
	generated := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	base := &Snapshot{
		GeneratedAt:         generated,
		SyncIntervalSeconds: 60,
		StaleAfterSeconds:   120,
	}

	for _, tc := range []struct {
		name      string
		now       time.Time
		wantAge   int
		wantStale bool
	}{
		{"served immediately", generated, 0, false},
		{"one tick behind, inside the bound", generated.Add(90 * time.Second), 90, false},
		{"exactly at the bound is not yet stale", generated.Add(120 * time.Second), 120, false},
		{"past the bound (tick wedged)", generated.Add(121 * time.Second), 121, true},
		{"daemon long dead", generated.Add(3 * time.Hour), 10800, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := base.WithFreshness(tc.now)
			if got.AgeSeconds != tc.wantAge {
				t.Errorf("AgeSeconds = %d, want %d", got.AgeSeconds, tc.wantAge)
			}
			if got.Stale != tc.wantStale {
				t.Errorf("Stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if got == base {
				t.Error("WithFreshness must return a copy, not the held snapshot")
			}
			if base.Stale || base.AgeSeconds != 0 {
				t.Errorf("WithFreshness must not mutate the held snapshot: %+v", base)
			}
		})
	}
}

// TestWithFreshness_UnsetBoundStillJudged: a payload whose bound was never set
// (hand-built, or decoded from an older producer) must not read as "never
// stale" — the default bound is applied.
func TestWithFreshness_UnsetBoundStillJudged(t *testing.T) {
	generated := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	snap := &Snapshot{GeneratedAt: generated} // no interval, no bound
	got := snap.WithFreshness(generated.Add(1 * time.Hour))
	if got.StaleAfterSeconds <= 0 {
		t.Errorf("an unset bound must be filled in, got %d", got.StaleAfterSeconds)
	}
	if !got.Stale {
		t.Errorf("an hour-old payload must be stale even with no declared bound: %+v", got)
	}
}

// TestSnapshotFreshnessJSONKeys pins the wire names the dashboard's consumers
// (the Grafana snapshot-age panel) bind to.
func TestSnapshotFreshnessJSONKeys(t *testing.T) {
	raw, err := json.Marshal((&Snapshot{
		GeneratedAt:         time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds: 60,
		StaleAfterSeconds:   120,
	}).WithFreshness(time.Unix(1700000000, 0).UTC().Add(5 * time.Minute)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"generated_at", "sync_interval_seconds", "stale_after_seconds", "age_seconds", "stale"} {
		if _, ok := m[key]; !ok {
			t.Errorf("dashboard payload missing key %q: %s", key, raw)
		}
	}
	if m["stale"] != true {
		t.Errorf("a 5-minute-old payload with a 120s bound must serialize stale=true: %s", raw)
	}
}
