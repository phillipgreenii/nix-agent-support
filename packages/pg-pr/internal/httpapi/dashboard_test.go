package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

func TestDashboard503WhenEmpty(t *testing.T) {
	s := snapshot.NewStore()
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
}

func TestDashboard200WhenPopulated(t *testing.T) {
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:         time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds: 60,
		Mine:                []snapshot.MineRow{},
		Team:                []snapshot.TeamRow{},
	})
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q want application/json", ct)
	}
	var got snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SyncIntervalSeconds != 60 {
		t.Errorf("got interval %d want 60", got.SyncIntervalSeconds)
	}
}

// fixedNow pins the handler's serve-time clock for the test.
func fixedNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowUTC
	t.Cleanup(func() { nowUTC = prev })
	nowUTC = func() time.Time { return now }
}

// getDashboard GETs the endpoint and decodes the payload.
func getDashboard(t *testing.T, s *snapshot.Store) snapshot.Snapshot {
	t.Helper()
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// TestDashboardStampsFreshnessAtServeTime: the served payload carries the
// freshness bound plus an age/stale verdict computed at the moment of the
// request — NOT at the moment the daemon built the snapshot. The same held
// snapshot must read fresh early and stale once the bound has passed, which is
// the whole point: a wedged sync tick leaves the snapshot in place and only the
// serve-time verdict can catch it (pr-pool INV-FRESH-1).
func TestDashboardStampsFreshnessAtServeTime(t *testing.T) {
	generated := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:         generated,
		SyncIntervalSeconds: 60,
		StaleAfterSeconds:   120,
		Mine:                []snapshot.MineRow{},
		Team:                []snapshot.TeamRow{},
	})

	fixedNow(t, generated.Add(30*time.Second))
	fresh := getDashboard(t, s)
	if fresh.StaleAfterSeconds != 120 {
		t.Errorf("stale_after_seconds: got %d want 120", fresh.StaleAfterSeconds)
	}
	if fresh.AgeSeconds != 30 {
		t.Errorf("age_seconds: got %d want 30", fresh.AgeSeconds)
	}
	if fresh.Stale {
		t.Errorf("a 30s-old snapshot must not be flagged stale: %+v", fresh)
	}

	// SAME held snapshot, later request: the verdict flips.
	fixedNow(t, generated.Add(10*time.Minute))
	stale := getDashboard(t, s)
	if stale.AgeSeconds != 600 {
		t.Errorf("age_seconds: got %d want 600", stale.AgeSeconds)
	}
	if !stale.Stale {
		t.Errorf("a 10-minute-old snapshot past a 120s bound must be flagged stale: %+v", stale)
	}
	// The as-of time itself is untouched by the stamp.
	if !stale.GeneratedAt.Equal(generated) {
		t.Errorf("generated_at must survive the freshness stamp: got %v want %v", stale.GeneratedAt, generated)
	}

	// And the held snapshot itself was never mutated by serving it.
	held, _ := s.Get()
	if held.Stale || held.AgeSeconds != 0 {
		t.Errorf("serving must not mutate the held snapshot: %+v", held)
	}
}
