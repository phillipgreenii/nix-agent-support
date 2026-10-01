// conformance_test.go: runs pkg/scriptout/conformance's shared suite
// (conformance.Run) and its list helper (conformance.InvokeList) against the
// REAL COMPILED pg-connector-alert-grafana binary, with an httptest server
// standing in for Grafana (hermetic: no network beyond loopback). Mirrors
// cmd/pg-connector-calendar-osx-bridge/conformance_test.go, and is likewise
// not behind a build tag so a plain `go test ./...` runs it.
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

// Deadlines are hang guards, not performance assertions (cold builds under
// heavy nix-check load are slow; see the calendar-osx-bridge precedent).
const (
	buildDeadline = 8 * time.Minute
	runDeadline   = 2 * time.Minute
)

func buildAlertGrafanaBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pg-connector-alert-grafana")
	ctx, cancel := context.WithTimeout(context.Background(), buildDeadline)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/pg-connector-alert-grafana: %v\n%s", err, out)
	}
	return bin
}

// fakeGrafana serves a fixed Alertmanager v2 alerts payload: one active
// critical alert, one active warning alert, one silenced alert. It records
// the filter= params of the last request.
func fakeGrafana(t *testing.T, lastFilters *[]string) *httptest.Server {
	t.Helper()
	const payload = `[
	  {"fingerprint":"f1","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"DiskFull","severity":"critical","__alert_rule_uid__":"u1"},"annotations":{"summary":"disk"},"status":{"state":"active"}},
	  {"fingerprint":"f2","startsAt":"2026-10-01T10:05:00Z","labels":{"alertname":"Slow","severity":"warning"},"annotations":{},"status":{"state":"active"}},
	  {"fingerprint":"f3","startsAt":"2026-10-01T10:06:00Z","labels":{"alertname":"Muted","severity":"critical"},"annotations":{},"status":{"state":"suppressed"}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*lastFilters = r.URL.Query()["filter"]
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConformance_RealBinary_SharedSuite(t *testing.T) {
	bin := buildAlertGrafanaBinary(t)
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

func TestConformance_RealBinary_ListOpAgainstFakeGrafana(t *testing.T) {
	bin := buildAlertGrafanaBinary(t)
	var filters []string
	srv := fakeGrafana(t, &filters)
	backend := conformance.ExecBackend{Binary: bin}
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()

	cfg := json.RawMessage(`{"base_url":"` + srv.URL + `","queries":{"crit":"{severity=\"critical\"}","multi":["{severity=\"critical\"}","{alertname=\"Slow\"}"]}}`)

	// Recognized named query: success, firing-only, filter reached Grafana.
	res, err := conformance.InvokeList(ctx, backend, "crit", false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "" || res.ExitCode != 0 {
		t.Fatalf("crit: code=%q exit=%d", res.ErrorCode, res.ExitCode)
	}
	var list schema.AlertListResult
	if err := json.Unmarshal(res.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Entities) != 2 || list.PresentIDs[0] != "grafana:f1" || list.PresentIDs[1] != "grafana:f2" {
		t.Fatalf("firing-only result = %+v (the silenced alert must be excluded)", list.PresentIDs)
	}
	if len(filters) != 1 || filters[0] != `severity="critical"` {
		t.Fatalf("filter params = %v", filters)
	}

	// Omitted query: whole firing set, no filter params.
	res, err = conformance.InvokeList(ctx, backend, "", true, cfg)
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("no query: %v %+v", err, res)
	}
	if len(filters) != 0 {
		t.Fatalf("omitted query sent filters %v", filters)
	}

	// List-valued query: union, deduplicated by id.
	res, err = conformance.InvokeList(ctx, backend, "multi", false, cfg)
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("multi: %v %+v", err, res)
	}
	if err := json.Unmarshal(res.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.PresentIDs) != 2 {
		t.Fatalf("union PresentIDs = %v, want deduplicated f1,f2", list.PresentIDs)
	}

	// Unrecognized name: query_not_recognized.
	res, err = conformance.InvokeList(ctx, backend, "nope", false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "query_not_recognized" {
		t.Fatalf("unrecognized: ErrorCode = %q (exit %d)", res.ErrorCode, res.ExitCode)
	}

	// Unreachable Grafana: unavailable, not an empty success.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	res, err = conformance.InvokeList(ctx, backend, "", false, json.RawMessage(`{"base_url":"`+deadURL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "unavailable" {
		t.Fatalf("unreachable: ErrorCode = %q, want unavailable", res.ErrorCode)
	}
}

func TestConformance_RealBinary_ShowAttentionAuthAndHistory(t *testing.T) {
	bin := buildAlertGrafanaBinary(t)
	var filters []string
	srv := fakeGrafana(t, &filters)
	backend := conformance.ExecBackend{Binary: bin}
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	cfg := `{"base_url":"` + srv.URL + `"}`

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
	errCode := func(m map[string]any) string {
		e, _ := m["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}

	if m := invoke(`{"op":"show","args":{"id":"grafana:f1"},"config":` + cfg + `}`); errCode(m) != "" {
		t.Fatalf("show f1: %v", m)
	}
	if m := invoke(`{"op":"show","args":{"id":"grafana:f3"},"config":` + cfg + `}`); errCode(m) != "not_found" {
		t.Fatalf("show silenced f3: %v, want not_found", m)
	}

	m := invoke(`{"op":"list_attention","args":{},"config":` + cfg + `}`)
	items, _ := m["result"].([]any)
	if len(items) != 2 {
		t.Fatalf("list_attention items = %v", m)
	}
	first, _ := items[0].(map[string]any)
	if first["type"] != "alert" || first["id"] != "grafana:f1" || first["severity"] != "critical" || first["summary"] != "DiskFull" {
		t.Fatalf("first attention item = %v", first)
	}

	if c := errCode(invoke(`{"op":"auth_status","args":{},"config":` + cfg + `}`)); c != "unknown_op" {
		t.Fatalf("auth_status code = %q, want unknown_op (disabled: not applicable)", c)
	}
	if c := errCode(invoke(`{"op":"list_history","args":{"since":"2026-10-01T00:00:00Z","until":"2026-10-02T00:00:00Z"},"config":` + cfg + `}`)); c != "unknown_op" {
		t.Fatalf("list_history code = %q, want unknown_op (out of scope)", c)
	}
}
