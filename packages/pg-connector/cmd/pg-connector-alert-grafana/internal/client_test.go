package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const twoAlerts = `[
 {"fingerprint":"aaa","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"A"},"status":{"state":"active"}},
 {"fingerprint":"bbb","startsAt":"2026-10-01T10:00:00Z","labels":{"alertname":"B"},"status":{"state":"suppressed"}}
]`

func TestHTTPClient_Alerts_SendsRepeatedFilterParamsToV2Path(t *testing.T) {
	var gotPath string
	var gotFilters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFilters = r.URL.Query()["filter"]
		_, _ = w.Write([]byte(twoAlerts))
	}))
	defer srv.Close()

	filters := []string{`severity="critical"`, `alertname=~"A|B"`}
	got, err := NewHTTPClient().Alerts(context.Background(), srv.URL+"/", filters)
	if err != nil {
		t.Fatalf("Alerts: %v", err)
	}
	if gotPath != alertsPath {
		t.Errorf("path = %q, want %q", gotPath, alertsPath)
	}
	if !reflect.DeepEqual(gotFilters, filters) {
		t.Errorf("filter params = %#v, want %#v", gotFilters, filters)
	}
	if len(got) != 2 || got[0].Fingerprint != "aaa" || got[1].Status.State != "suppressed" {
		t.Errorf("decoded alerts = %+v", got)
	}
}

func TestHTTPClient_Alerts_NoFiltersSendsNoFilterParam(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	got, err := NewHTTPClient().Alerts(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Alerts: %v", err)
	}
	if rawQuery != "" || len(got) != 0 {
		t.Errorf("rawQuery = %q, alerts = %v", rawQuery, got)
	}
}

func TestHTTPClient_Alerts_Failures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"http 500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }},
		{"http 404", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }},
		{"malformed json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{not json`)) }},
		{"wrong json shape", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"a":1}`)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			if _, err := NewHTTPClient().Alerts(context.Background(), srv.URL, nil); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestHTTPClient_Alerts_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens there any more
	if _, err := NewHTTPClient().Alerts(context.Background(), url, nil); err == nil {
		t.Fatal("want an error for an unreachable host")
	}
}

const rulerBody = `{
 "FolderB": [{"name":"g2","rules":[{"grafana_alert":{"uid":"uid-3","title":"Three"}}]}],
 "FolderA": [
  {"name":"g1","rules":[{"grafana_alert":{"uid":"uid-1","title":"One"}},{"grafana_alert":{"uid":"uid-2","title":"Two"}}]},
  {"name":"g1b","rules":[]}
 ]
}`

func TestHTTPClient_Rules_FlattensFoldersInSortedOrder(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(rulerBody))
	}))
	defer srv.Close()

	got, err := NewHTTPClient().Rules(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	want := []apiRule{{UID: "uid-1", Title: "One"}, {UID: "uid-2", Title: "Two"}, {UID: "uid-3", Title: "Three"}}
	if gotPath != rulerRulesPath || !reflect.DeepEqual(got, want) {
		t.Fatalf("path=%q rules=%+v, want %q %+v", gotPath, got, rulerRulesPath, want)
	}
}

func TestHTTPClient_RuleHistory_SendsRuleUIDWindowAndLimit(t *testing.T) {
	var gotPath string
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(`{"data":{"values":[[1759312800000000],["T {a=1}"],["Normal"],["Alerting"],[{}]]}}`))
	}))
	defer srv.Close()

	from := time.Unix(1759276800, 0)
	to := time.Unix(1759363200, 0)
	h, err := NewHTTPClient().RuleHistory(context.Background(), srv.URL, "uid-1", from, to, 1000)
	if err != nil {
		t.Fatalf("RuleHistory: %v", err)
	}
	if gotPath != historyPath {
		t.Errorf("path = %q, want %q", gotPath, historyPath)
	}
	for k, v := range map[string]string{"ruleUID": "uid-1", "from": "1759276800", "to": "1759363200", "limit": "1000"} {
		if len(gotQuery[k]) != 1 || gotQuery[k][0] != v {
			t.Errorf("query %s = %v, want %q", k, gotQuery[k], v)
		}
	}
	if h.Data == nil || len(h.Data.Values) != 5 {
		t.Fatalf("decoded frame = %+v", h)
	}
}

func TestHTTPClient_RulesAndHistory_Failures(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"HTTP error":       {http.StatusInternalServerError, "boom"},
		"malformed JSON":   {http.StatusOK, "<html>"},
		"wrong top-level":  {http.StatusOK, "[]"},
		"HTTP 400 no uid":  {http.StatusBadRequest, "ruleUID is required"},
		"non-object frame": {http.StatusOK, "[1,2]"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := NewHTTPClient().Rules(context.Background(), srv.URL); err == nil {
				t.Error("Rules: want error")
			}
			if _, err := NewHTTPClient().RuleHistory(context.Background(), srv.URL, "u", time.Unix(0, 0), time.Unix(1, 0), 10); err == nil {
				t.Error("RuleHistory: want error")
			}
		})
	}
}

func TestHTTPClient_RulesAndHistory_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := NewHTTPClient().Rules(context.Background(), url); err == nil {
		t.Error("Rules: want error")
	}
	if _, err := NewHTTPClient().RuleHistory(context.Background(), url, "u", time.Unix(0, 0), time.Unix(1, 0), 10); err == nil {
		t.Error("RuleHistory: want error")
	}
}
