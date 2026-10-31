//go:build integration

// conformance_test.go: runs pkg/scriptout/conformance's shared suite against
// the REAL COMPILED pg-connector-alert-pagerduty binary, with an httptest
// server on loopback standing in for PagerDuty (hermetic: no outside network,
// and the Keychain is disabled by config so the real one is never read).
// Mirrors cmd/pg-connector-alert-grafana/conformance_test.go and carries the
// `integration` tag for the same reason (it builds and runs a subprocess).
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

const (
	buildDeadline = 8 * time.Minute
	runDeadline   = 2 * time.Minute
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pg-connector-alert-pagerduty")
	ctx, cancel := context.WithTimeout(context.Background(), buildDeadline)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// fakePagerDuty serves two firing incidents (one acknowledged), one resolved
// incident (which must never surface) and their alerts. It records the raw
// query of the last incidents-list request and requires the Token header.
func fakePagerDuty(t *testing.T, lastRaw *string) *httptest.Server {
	t.Helper()
	const incidents = `{"incidents":[
	  {"id":"PTRIG","incident_number":1,"title":"Disk","status":"triggered","urgency":"low","created_at":"2026-10-01T10:00:00Z","html_url":"https://pd.example.localhost/i/PTRIG","service":{"id":"S","summary":"svc"}},
	  {"id":"PACK","incident_number":2,"title":"Disk2","status":"acknowledged","urgency":"high","created_at":"2026-10-01T10:05:00Z","html_url":"https://pd.example.localhost/i/PACK","service":{"id":"S","summary":"svc"}}],
	  "more":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token token=conformance-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/incidents":
			*lastRaw = r.URL.RawQuery
			_, _ = w.Write([]byte(incidents))
		case "/incidents/PTRIG/alerts", "/incidents/PACK/alerts":
			_, _ = w.Write([]byte(`{"alerts":[{"id":"a","severity":"critical"}]}`))
		case "/incidents/PTRIG":
			_, _ = w.Write([]byte(`{"incident":{"id":"PTRIG","title":"Disk","status":"triggered","urgency":"low","created_at":"2026-10-01T10:00:00Z"}}`))
		case "/incidents/PRES":
			_, _ = w.Write([]byte(`{"incident":{"id":"PRES","title":"Old","status":"resolved","urgency":"low","created_at":"2026-10-01T10:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConformance_RealBinary_SharedSuite(t *testing.T) {
	bin := buildBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	results := conformance.Run(ctx, conformance.ExecBackend{Binary: bin})
	if len(results) == 0 {
		t.Fatal("conformance.Run produced no results")
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

func TestConformance_RealBinary_ListShowAttentionAuth(t *testing.T) {
	t.Setenv("PAGERDUTY_API_TOKEN", "conformance-token")
	bin := buildBinary(t)
	var raw string
	srv := fakePagerDuty(t, &raw)
	backend := conformance.ExecBackend{Binary: bin}
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	cfg := `{"disable_keychain":true,"base_url":"` + srv.URL + `","queries":{"mine":["service_ids[]=A&service_ids[]=B"]}}`

	invoke := func(req string) map[string]any {
		t.Helper()
		out, _, err := backend.Invoke(ctx, []byte(req))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("reply %q: %v", out, err)
		}
		return m
	}
	code := func(m map[string]any) string {
		e, _ := m["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}

	res, err := conformance.InvokeList(ctx, backend, "mine", false, json.RawMessage(cfg))
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("list: %v %+v", err, res)
	}
	var list schema.AlertListResult
	if err := json.Unmarshal(res.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.PresentIDs) != 2 || list.PresentIDs[0] != "pagerduty:PTRIG" || list.PresentIDs[1] != "pagerduty:PACK" {
		t.Fatalf("PresentIDs = %v", list.PresentIDs)
	}
	const want = "statuses[]=triggered&statuses[]=acknowledged&service_ids[]=A&service_ids[]=B&limit=100&offset=0"
	if raw != want {
		t.Fatalf("raw query = %q, want %q", raw, want)
	}

	if m := invoke(`{"op":"show","args":{"id":"pagerduty:PTRIG"},"config":` + cfg + `}`); code(m) != "" {
		t.Fatalf("show PTRIG: %v", m)
	}
	if m := invoke(`{"op":"show","args":{"id":"pagerduty:PRES"},"config":` + cfg + `}`); code(m) != "not_found" {
		t.Fatalf("show resolved: %v, want not_found", m)
	}

	// Both incidents carry a critical alert; the acknowledged one is lowered
	// to high, so the unacknowledged one is listed at critical.
	m := invoke(`{"op":"list_attention","args":{},"config":` + cfg + `}`)
	items, _ := m["result"].([]any)
	if len(items) != 2 {
		t.Fatalf("list_attention = %v", m)
	}
	sev := map[string]any{}
	for _, it := range items {
		o, _ := it.(map[string]any)
		sev[o["id"].(string)] = o["severity"]
	}
	if sev["pagerduty:PTRIG"] != "critical" || sev["pagerduty:PACK"] != "high" {
		t.Fatalf("attention severities = %v", sev)
	}

	if m := invoke(`{"op":"auth_status","args":{},"config":` + cfg + `}`); m["error"] != nil {
		t.Fatalf("auth_status: %v", m)
	}
	// A wrong key is rejected by the (fake) service as unauthenticated.
	bad := `{"disable_keychain":true,"base_url":"` + srv.URL + `"}`
	t.Setenv("PAGERDUTY_API_TOKEN", "wrong")
	if c := code(invoke(`{"op":"list","args":{},"config":` + bad + `}`)); c != "unauthenticated" {
		t.Fatalf("wrong key: code = %q", c)
	}
}
