package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
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
