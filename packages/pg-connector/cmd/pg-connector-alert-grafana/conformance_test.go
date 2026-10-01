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
	"strings"
	"sync/atomic"
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

// historyPathHits counts requests that reached Grafana's rule-enumeration or
// state-history endpoints, so a test can prove list_attention stays
// history-free (design 4).
type historyPathHits struct{ rules, history atomic.Int32 }

// Fabricated state-history frames in the live payload shape (design 9.2):
// microsecond times, label block inside the text field. "uid-disk" has one
// closed and one still-firing episode plus a row with an unparseable text;
// "uid-quiet" has no history.
const (
	fakeRulerBody = `{"Infra":[{"name":"g","rules":[
	  {"grafana_alert":{"uid":"uid-disk","title":"DiskFull"}},
	  {"grafana_alert":{"uid":"uid-quiet","title":"Quiet"}}]}]}`
	fakeDiskHistory = `{"data":{"values":[
	  [1790816400000000,1790820000000000,1790823600000000,1790827200000000],
	  ["DiskFull {host_name=web-1, severity=critical} - {}","DiskFull {host_name=web-1, severity=critical} - {}","DiskFull {host_name=web-2, severity=warning} - {}","garbled text without a block"],
	  ["Normal","Alerting","Normal","Normal"],
	  ["Alerting","Normal","Alerting","Alerting"],
	  [{},{},{},{}]]}}`
	fakeQuietHistory = `{"data":{"values":[[],[],[],[],[]]}}`
)

// fakeGrafana serves a fixed Alertmanager v2 alerts payload: one active
// critical alert, one active warning alert, one silenced alert. It records
// the filter= params of the last request. When hits is non-nil it also serves
// the ruler and per-rule history endpoints and counts requests to them.
func fakeGrafana(t *testing.T, lastFilters *[]string, hits ...*historyPathHits) *httptest.Server {
	t.Helper()
	var h *historyPathHits
	if len(hits) > 0 {
		h = hits[0]
	}
	const payload = `[
	  {"fingerprint":"f1","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"DiskFull","severity":"critical","__alert_rule_uid__":"u1"},"annotations":{"summary":"disk"},"status":{"state":"active"}},
	  {"fingerprint":"f2","startsAt":"2026-10-01T10:05:00Z","labels":{"alertname":"Slow","severity":"warning"},"annotations":{},"status":{"state":"active"}},
	  {"fingerprint":"f3","startsAt":"2026-10-01T10:06:00Z","labels":{"alertname":"Muted","severity":"critical"},"annotations":{},"status":{"state":"suppressed"}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ruler/grafana/api/v1/rules":
			if h != nil {
				h.rules.Add(1)
			}
			_, _ = w.Write([]byte(fakeRulerBody))
		case "/api/v1/rules/history":
			if h != nil {
				h.history.Add(1)
			}
			switch r.URL.Query().Get("ruleUID") {
			case "uid-disk":
				_, _ = w.Write([]byte(fakeDiskHistory))
			case "uid-quiet":
				_, _ = w.Write([]byte(fakeQuietHistory))
			default:
				http.Error(w, "ruleUID is required to query annotations", http.StatusBadRequest)
			}
		default:
			*lastFilters = r.URL.Query()["filter"]
			_, _ = w.Write([]byte(payload))
		}
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

func TestConformance_RealBinary_ShowAttentionAndAuth(t *testing.T) {
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
}

func TestConformance_RealBinary_ListHistory(t *testing.T) {
	bin := buildAlertGrafanaBinary(t)
	var filters []string
	var hits historyPathHits
	srv := fakeGrafana(t, &filters, &hits)
	backend := conformance.ExecBackend{Binary: bin}
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	cfg := `{"base_url":"` + srv.URL + `","queries":{"crit":"{severity=\"critical\"}"}}`

	invoke := func(req string) (map[string]any, int) {
		t.Helper()
		out, code, err := backend.Invoke(ctx, []byte(req))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("reply %q: %v", out, err)
		}
		return m, code
	}

	// capabilities advertises the op now.
	caps, _ := invoke(`{"op":"capabilities","args":{}}`)
	if !strings.Contains(string(mustMarshal(t, caps["ops"])), `"list_history"`) {
		t.Fatalf("capabilities ops = %v, want list_history advertised", caps["ops"])
	}

	// Whole window: three episodes across two rules (the quiet rule adds none;
	// the garbled row degrades to a rule-level episode instead of failing).
	const window = `"args":{"since":"2026-10-01T00:00:00Z","until":"2026-10-02T00:00:00Z"`
	m, _ := invoke(`{"op":"list_history",` + window + `},"config":` + cfg + `}`)
	if e, ok := m["error"]; ok {
		t.Fatalf("list_history error = %v", e)
	}
	var res schema.AlertHistoryResult
	raw, _ := json.Marshal(m["result"])
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if hits.rules.Load() != 1 || hits.history.Load() != 2 {
		t.Fatalf("rule enumerations = %d, history calls = %d; want 1 and one per rule (2)", hits.rules.Load(), hits.history.Load())
	}
	if len(res.Episodes) != 3 || res.Truncated {
		t.Fatalf("episodes = %+v truncated=%v", res.Episodes, res.Truncated)
	}
	first := res.Episodes[0]
	if first.RuleID != "uid-disk" || first.Title != "DiskFull" || first.StartedAt != "2026-10-01T01:00:00Z" ||
		first.EndedAt != "2026-10-01T02:00:00Z" || first.Severity != schema.SeverityCritical ||
		first.Attributes["label.host_name"] != "web-1" {
		t.Fatalf("first episode = %+v", first)
	}

	// Named query narrows client-side.
	m, _ = invoke(`{"op":"list_history","args":{"since":"2026-10-01T00:00:00Z","until":"2026-10-02T00:00:00Z","query":"crit"},"config":` + cfg + `}`)
	raw, _ = json.Marshal(m["result"])
	res = schema.AlertHistoryResult{}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Episodes) != 1 || res.Episodes[0].Attributes["label.host_name"] != "web-1" {
		t.Fatalf("crit query episodes = %+v", res.Episodes)
	}

	// Attention (and list) must not touch the history path.
	hits.rules.Store(0)
	hits.history.Store(0)
	if m, _ := invoke(`{"op":"list_attention","args":{},"config":` + cfg + `}`); m["error"] != nil {
		t.Fatalf("list_attention: %v", m)
	}
	if res, err := conformance.InvokeList(ctx, backend, "", false, json.RawMessage(cfg)); err != nil || res.ErrorCode != "" {
		t.Fatalf("list: %v %+v", err, res)
	}
	if hits.rules.Load() != 0 || hits.history.Load() != 0 {
		t.Fatalf("attention/list reached the history path: rules=%d history=%d", hits.rules.Load(), hits.history.Load())
	}

	// A rule whose history endpoint fails makes the op unavailable.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ruler/grafana/api/v1/rules" {
			_, _ = w.Write([]byte(fakeRulerBody))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()
	m, _ = invoke(`{"op":"list_history",` + window + `},"config":{"base_url":"` + failing.URL + `"}}`)
	if c := errCodeOf(m); c != "unavailable" {
		t.Fatalf("history 500: code = %q, want unavailable", c)
	}

	// Unreachable Grafana: unavailable.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	m, _ = invoke(`{"op":"list_history",` + window + `},"config":{"base_url":"` + deadURL + `"}}`)
	if c := errCodeOf(m); c != "unavailable" {
		t.Fatalf("unreachable: code = %q, want unavailable", c)
	}
}

func errCodeOf(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
