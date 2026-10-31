package internal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests dial only a loopback httptest server, never PagerDuty.

func TestHTTPClient_SendsAuthHeadersAndLiteralBracketQuery(t *testing.T) {
	var gotAuth, gotAccept, gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccept, gotRaw = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.URL.RawQuery
		_, _ = w.Write([]byte(`{"incidents":[{"id":"P1"}],"more":false}`))
	}))
	defer srv.Close()
	got, err := NewHTTPClient().Incidents(context.Background(), srv.URL, "tok", "statuses[]=triggered&service_ids[]=A&service_ids[]=B")
	if err != nil || len(got) != 1 || got[0].ID != "P1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if gotAuth != "Token token=tok" || !strings.Contains(gotAccept, "version=2") {
		t.Fatalf("headers auth=%q accept=%q", gotAuth, gotAccept)
	}
	if !strings.HasPrefix(gotRaw, "statuses[]=triggered&service_ids[]=A&service_ids[]=B&limit=100&offset=0") {
		t.Fatalf("raw query = %q", gotRaw)
	}
}

func TestHTTPClient_FollowsPaging(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		offsets = append(offsets, off)
		if off == "0" {
			_, _ = w.Write([]byte(`{"incidents":[{"id":"P1"}],"more":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"incidents":[{"id":"P2"}],"more":false}`))
	}))
	defer srv.Close()
	got, err := NewHTTPClient().Incidents(context.Background(), srv.URL, "t", "statuses[]=triggered")
	if err != nil || len(got) != 2 || strings.Join(offsets, ",") != "0,100" {
		t.Fatalf("got %+v offsets=%v err=%v", got, offsets, err)
	}
}

func TestHTTPClient_StatusMapping(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{{401, errUnauthorized}, {403, errUnauthorized}, {404, errMissing}}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(c.code) }))
		_, err := NewHTTPClient().Incident(context.Background(), srv.URL, "t", "P1")
		srv.Close()
		if !errors.Is(err, c.want) {
			t.Errorf("HTTP %d: err = %v, want %v", c.code, err, c.want)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	_, err := NewHTTPClient().Incidents(context.Background(), srv.URL, "t", "x")
	if err == nil || errors.Is(err, errUnauthorized) || errors.Is(err, errMissing) {
		t.Fatalf("500: err = %v", err)
	}
}

func TestHTTPClient_MalformedAndUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }))
	c := NewHTTPClient()
	if _, err := c.Incidents(context.Background(), srv.URL, "t", "x"); err == nil {
		t.Error("malformed incidents accepted")
	}
	if _, err := c.Incident(context.Background(), srv.URL, "t", "P1"); err == nil {
		t.Error("malformed incident accepted")
	}
	if _, err := c.IncidentAlerts(context.Background(), srv.URL, "t", "P1"); err == nil {
		t.Error("malformed alerts accepted")
	}
	url := srv.URL
	srv.Close()
	if _, err := c.Incidents(context.Background(), url, "t", "x"); err == nil {
		t.Error("unreachable accepted")
	}
}

func TestHTTPClient_IncidentAlertsPath(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"alerts":[{"id":"A1","severity":"critical"}]}`))
	}))
	defer srv.Close()
	got, err := NewHTTPClient().IncidentAlerts(context.Background(), srv.URL, "t", "P1")
	if err != nil || path != "/incidents/P1/alerts" || len(got) != 1 || got[0].Severity != "critical" {
		t.Fatalf("got %+v path=%q err=%v", got, path, err)
	}
}
