package internal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type seenRequest struct {
	path, rawQuery, xClient, traceparent, accept string
}

// fakeDaemon serves status and body for every request and records what it
// was asked.
func fakeDaemon(t *testing.T, status int, contentType, body string) (url string, seen func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, seenRequest{r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Client"), r.Header.Get("traceparent"), r.Header.Get("Accept")})
		mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), reqs...)
	}
}

const calendarBody = `{"as_of":"2026-10-10T15:00:00.000Z","stale":false,"calendar_id":"focus-cycles","calendar":"Focus cycles",
"events":[{"id":"ev-1","title":"Deep work","start":"2026-10-10T14:00:00.000Z","end":"2026-10-10T15:00:00.000Z","notes":"cycle_type: deep-work\n---\n"}]}`

func TestCalendar_SendsWindowCalendarAndIdentityHeaders(t *testing.T) {
	url, seen := fakeDaemon(t, 200, "application/json", calendarBody)
	c := NewHTTPClient("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	from := time.Date(2026, 10, 10, 14, 0, 0, 0, time.FixedZone("x", 3600)) // 13:00Z
	to := from.Add(2 * time.Hour)

	cal, err := c.Calendar(context.Background(), url+"/", from, to, "focus-cycles")
	if err != nil {
		t.Fatal(err)
	}
	if cal.CalendarID != "focus-cycles" || len(cal.Events) != 1 || cal.Events[0].ID != "ev-1" || cal.Events[0].Notes != "cycle_type: deep-work\n---\n" {
		t.Errorf("decoded = %+v", cal)
	}
	reqs := seen()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	if r.path != "/api/v1/calendar" {
		t.Errorf("path = %q", r.path)
	}
	if want := "calendar=focus-cycles&from=2026-10-10T13%3A00%3A00Z&to=2026-10-10T15%3A00%3A00Z"; r.rawQuery != want {
		t.Errorf("query = %q, want %q (an RFC 3339 UTC window, sorted)", r.rawQuery, want)
	}
	if r.xClient != "connector" {
		t.Errorf("X-Client = %q, want the daemon's reserved value connector", r.xClient)
	}
	if r.traceparent != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent = %q", r.traceparent)
	}
	if r.accept != "application/json" {
		t.Errorf("Accept = %q", r.accept)
	}
}

func TestCalendar_OmitsEmptyCalendarFilterAndTraceparent(t *testing.T) {
	url, seen := fakeDaemon(t, 200, "application/json", calendarBody)
	_, err := NewHTTPClient("").Calendar(context.Background(), url, time.Unix(0, 0), time.Unix(60, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	r := seen()[0]
	if strings.Contains(r.rawQuery, "calendar=") || r.traceparent != "" {
		t.Errorf("request = %+v, want no calendar filter and no traceparent", r)
	}
}

func TestAttention_DecodesItems(t *testing.T) {
	url, seen := fakeDaemon(t, 200, "application/json", `{"as_of":"2026-10-10T15:00:00.000Z","items":[
{"type":"task","id":"day:2026-10-10:plan","summary":"Plan","severity":"high","group":{"key":"daily","label":"Today"},"url":"http://x/#/tasks/day:2026-10-10:plan"}]}`)
	att, err := NewHTTPClient("").Attention(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if len(att.Items) != 1 || att.Items[0].Severity != "high" || att.Items[0].Group.Key != "daily" || att.Items[0].URL == "" {
		t.Errorf("decoded = %+v", att)
	}
	if r := seen()[0]; r.path != "/api/v1/attention" || r.rawQuery != "" {
		t.Errorf("request = %+v", r)
	}
}

func TestGet_ProblemResponsesKeepStatusReasonAndDetail(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   DaemonError
	}{
		{"refused window", 400, `{"reason":"invalid_request","detail":"The window is empty."}`, DaemonError{Status: 400, Reason: "invalid_request", Detail: "The window is empty."}},
		{"not ready", 503, `{"reason":"not_ready","detail":"replaying"}`, DaemonError{Status: 503, Reason: "not_ready", Detail: "replaying"}},
		{"no problem body", 500, `oops`, DaemonError{Status: 500}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, _ := fakeDaemon(t, c.status, "application/problem+json", c.body)
			_, err := NewHTTPClient("").Attention(context.Background(), url)
			var de *DaemonError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want a *DaemonError", err)
			}
			if de.Status != c.want.Status || de.Reason != c.want.Reason || de.Detail != c.want.Detail {
				t.Errorf("error = %+v, want %+v", de, c.want)
			}
		})
	}
}

func TestGet_UnreachableDaemonIsStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more

	_, err := NewHTTPClient("").Attention(context.Background(), url)
	var de *DaemonError
	if !errors.As(err, &de) || de.Status != 0 {
		t.Fatalf("err = %v, want a DaemonError with status 0", err)
	}
	if !strings.Contains(err.Error(), "unreachable") || !strings.Contains(err.Error(), "is it running") {
		t.Errorf("message = %q, want it to say the daemon is unreachable", err)
	}
}

func TestGet_MalformedBodyIsADecodeError(t *testing.T) {
	url, _ := fakeDaemon(t, 200, "application/json", `{"as_of":`)
	_, err := NewHTTPClient("").Calendar(context.Background(), url, time.Unix(0, 0), time.Unix(60, 0), "")
	var de *DaemonError
	if !errors.As(err, &de) || de.Status != 200 || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("err = %v, want a malformed-response DaemonError", err)
	}
}

func TestGet_OversizedBodyIsRefused(t *testing.T) {
	url, _ := fakeDaemon(t, 200, "application/json", `{"as_of":"`+strings.Repeat("x", maxResponseBytes)+`"}`)
	_, err := NewHTTPClient("").Attention(context.Background(), url)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("err = %v, want the size limit to refuse the body", err)
	}
}

func TestGet_BadBaseURL(t *testing.T) {
	_, err := NewHTTPClient("").Attention(context.Background(), "http://[::1")
	var de *DaemonError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want a DaemonError", err)
	}
}
