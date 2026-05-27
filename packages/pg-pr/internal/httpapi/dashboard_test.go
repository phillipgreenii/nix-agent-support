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
