package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// serveSnapshot starts a test daemon serving body at Path and returns the
// host:port form Fetch accepts.
func serveSnapshot(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			t.Errorf("requested path = %q, want %q", r.URL.Path, Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestFetchDecodesSnapshot(t *testing.T) {
	want := snapshot.Snapshot{
		GeneratedAt:         time.Date(2026, 8, 13, 13, 34, 20, 0, time.UTC),
		SyncIntervalSeconds: 60,
		StaleAfterSeconds:   120,
		AgeSeconds:          1,
		Stale:               false,
		Team: []snapshot.TeamRow{{
			Repo:           "acme/widgets",
			Number:         4242,
			Title:          "feat(web): safe middleware header parsing",
			Owner:          "teammate",
			URL:            "https://github.com/acme/widgets/pull/4242",
			CIStatus:       "failure",
			HumanApproved:  true,
			LinesChanged:   556,
			FilesChanged:   171,
			NeedsAttention: true,
			MatchReason:    []string{"label:team/lbl-one"},
		}},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	got, err := Fetch(context.Background(), serveSnapshot(t, http.StatusOK, string(raw)))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(got.Team) != 1 {
		t.Fatalf("len(Team) = %d, want 1", len(got.Team))
	}
	if got.Team[0].Number != 4242 {
		t.Errorf("Team[0].Number = %d, want 4242", got.Team[0].Number)
	}
	if got.Team[0].URL != want.Team[0].URL {
		t.Errorf("Team[0].URL = %q, want %q", got.Team[0].URL, want.Team[0].URL)
	}
	if !got.Team[0].NeedsAttention {
		t.Error("Team[0].NeedsAttention = false, want true")
	}
	if got.StaleAfterSeconds != 120 {
		t.Errorf("StaleAfterSeconds = %d, want 120", got.StaleAfterSeconds)
	}
}

func TestFetchReportsErrNoSnapshotOn503(t *testing.T) {
	addr := serveSnapshot(t, http.StatusServiceUnavailable, `{"error":"snapshot not yet populated"}`)
	_, err := Fetch(context.Background(), addr)
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Fetch() error = %v, want ErrNoSnapshot", err)
	}
}

func TestFetchErrorsOnUnexpectedStatus(t *testing.T) {
	addr := serveSnapshot(t, http.StatusInternalServerError, `boom`)
	_, err := Fetch(context.Background(), addr)
	if err == nil {
		t.Fatal("Fetch() error = nil, want an error")
	}
	if errors.Is(err, ErrNoSnapshot) {
		t.Error("a 500 must not be reported as ErrNoSnapshot")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not name the status", err)
	}
}

func TestFetchErrorsOnMalformedBody(t *testing.T) {
	addr := serveSnapshot(t, http.StatusOK, `{"team": "not-an-array"}`)
	_, err := Fetch(context.Background(), addr)
	if err == nil {
		t.Fatal("Fetch() error = nil, want a decode error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error %q does not identify itself as a decode failure", err)
	}
}

// TestFetchErrorNamesAddrWhenUnreachable pins the operator-facing half of the
// contract: a daemon that is not running must produce an error naming the
// address that was tried, so the remedy is obvious.
func TestFetchErrorNamesAddrWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing is listening now

	_, err := Fetch(context.Background(), addr)
	if err == nil {
		t.Fatal("Fetch() error = nil, want a transport error")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q does not name the address %q", err, addr)
	}
}
