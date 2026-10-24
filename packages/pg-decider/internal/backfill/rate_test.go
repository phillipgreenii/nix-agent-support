package backfill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheOldBackendLogSourceReadsTheNewestReadingByTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, p, strings.Join([]string{
		`{"time":"2026-10-24T10:00:00Z","msg":"start"}`,
		`{"time":"2026-10-24T10:05:00Z","graphql_remaining":4100,"graphql_reset_at":"2026-10-24T11:00:00Z"}`,
		`{"time":"2026-10-24T10:09:00Z","graphql_remaining":2500}`,
		`{"time":"2026-10-24T10:07:00Z","graphql_remaining":3900}`, // older, later in the file
		`not json`,
		`{"time":"2026-10-24T10:09:30Z","level":"info"}`, // no reading: ignored
	}, "\n")+"\n")
	r, err := LogSource{Path: p}.Newest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Remaining != 2500 || !r.At.Equal(time.Date(2026, 10, 24, 10, 9, 0, 0, time.UTC)) || r.Source != p {
		t.Fatalf("%+v", r)
	}
}

func TestTheOldBackendLogSourceTakesALaterLineOnATieAndFallsBackToTheRotatedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	writeFile(t, p, `{"time":"2026-10-24T10:00:00Z","graphql_remaining":4000}`+"\n"+`{"time":"2026-10-24T10:00:00Z","graphql_remaining":3000}`+"\n")
	if r, err := (LogSource{Path: p}).Newest(context.Background()); err != nil || r.Remaining != 3000 {
		t.Fatalf("tie: %+v %v", r, err)
	}
	// The live file holds no reading: the rotated sibling answers.
	writeFile(t, p, `{"time":"2026-10-24T10:00:00Z","msg":"start"}`+"\n")
	writeFile(t, p+".1", `{"time":"2026-10-24T09:00:00Z","graphql_remaining":3300}`+"\n")
	r, err := LogSource{Path: p}.Newest(context.Background())
	if err != nil || r.Remaining != 3300 || r.Source != p+".1" {
		t.Fatalf("rotated: %+v %v", r, err)
	}
}

func TestTheOldBackendLogSourceRefusesAReadingOlderThanItsBound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, p, `{"time":"2026-10-24T10:00:00Z","graphql_remaining":4000}`+"\n")
	now := time.Date(2026, 10, 24, 11, 30, 0, 0, time.UTC)
	if r, err := (LogSource{Path: p, MaxAge: 2 * time.Hour, Now: func() time.Time { return now }}).Newest(context.Background()); err != nil || r.Remaining != 4000 {
		t.Fatalf("a fresh enough reading: %+v %v", r, err)
	}
	_, err := LogSource{Path: p, MaxAge: time.Hour, Now: func() time.Time { return now }}.Newest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "older than 1h0m0s") || !strings.Contains(err.Error(), "daemon cutover") {
		t.Fatalf("a stale reading was accepted: %v", err)
	}
}

func TestTheOldBackendLogSourceFailsClosedWithoutAReading(t *testing.T) {
	dir := t.TempDir()
	if _, err := (LogSource{Path: filepath.Join(dir, "missing")}).Newest(context.Background()); err == nil {
		t.Error("a missing log was a reading")
	}
	p := filepath.Join(dir, "events.jsonl")
	writeFile(t, p, `{"time":"2026-10-24T10:00:00Z","msg":"start"}`+"\n")
	if _, err := (LogSource{Path: p}).Newest(context.Background()); err == nil {
		t.Error("a log with no reading was a reading")
	}
}

// roundTripFunc is an http.RoundTripper that answers from a function, so the metrics
// tests open no socket (the nix build sandbox allows none).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// metricsClient answers every request with status and body, noting the request.
func metricsClient(body string, status int, seen *string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if seen != nil {
			*seen = r.URL.Path + "?" + r.URL.RawQuery
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	})}
}

func TestTheDaemonMetricsSourceQueriesTheGaugeAndTakesTheLowestFreshWindow(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 10, 0, 0, time.UTC)
	ts := now.Add(-time.Minute).Unix()
	body := fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[
	  {"metric":{"bucket":"graphql"},"value":[%d,"3500"]},
	  {"metric":{"bucket":"graphql"},"value":[%d,"2800.5"]},
	  {"metric":{"bucket":"graphql"},"value":[%d,"100"]}]}}`, ts, ts, now.Add(-time.Hour).Unix())
	var seen string
	r, err := MetricsSource{BaseURL: "http://metrics.invalid:9090", Client: metricsClient(body, 200, &seen), Now: func() time.Time { return now }}.Newest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The stale sample (100, an hour old) is not a reading; the lowest fresh one decides.
	if r.Remaining != 2800 {
		t.Fatalf("%+v", r)
	}
	if !strings.HasPrefix(seen, "/api/v1/query?") || !strings.Contains(seen, "pg_connector_github_budget_remaining") || !strings.Contains(seen, "graphql") {
		t.Fatalf("request %q", seen)
	}
}

func TestTheDaemonMetricsSourceFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 24, 10, 10, 0, 0, time.UTC)
	stale := fmt.Sprintf(`{"status":"success","data":{"result":[{"value":[%d,"5000"]}]}}`, now.Add(-time.Hour).Unix())
	for name, c := range map[string]struct {
		body   string
		status int
	}{
		"an HTTP error":       {`oops`, 500},
		"an error status":     {`{"status":"error","error":"bad query"}`, 200},
		"no series":           {`{"status":"success","data":{"result":[]}}`, 200},
		"only a stale sample": {stale, 200},
		"not JSON":            {`<html>`, 200},
	} {
		if r, err := (MetricsSource{BaseURL: "http://metrics.invalid", Client: metricsClient(c.body, c.status, nil), Now: func() time.Time { return now }}).Newest(context.Background()); err == nil {
			t.Errorf("%s: a reading %+v", name, r)
		}
	}
	down := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })}
	if _, err := (MetricsSource{BaseURL: "http://metrics.invalid", Client: down}).Newest(context.Background()); err == nil {
		t.Error("an unreachable endpoint was a reading")
	}
	if _, err := (MetricsSource{}).Newest(context.Background()); err == nil {
		t.Error("no URL was a reading")
	}
}

func TestTheRateSourceIsAConfigurationKeyWithBothImplementations(t *testing.T) {
	c := &Config{RateSource: SourceOldBackendLog, RateLog: "/x/events.jsonl"}
	if s, err := c.RateSourceFor(); err != nil || s.(LogSource).Path != "/x/events.jsonl" || s.(LogSource).MaxAge != DefaultLogMaxAge {
		t.Fatalf("%v %v", s, err)
	}
	c = &Config{RateSource: SourceDaemonMetrics, MetricsURL: "http://m:9090"}
	if s, err := c.RateSourceFor(); err != nil || s.(MetricsSource).BaseURL != "http://m:9090" {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := (&Config{RateSource: "carrier-pigeon"}).RateSourceFor(); err == nil {
		t.Fatal("an unknown source")
	}
	// The default before the cutover is the old backend's log.
	d := &Config{}
	d.WithDefaults()
	if d.RateSource != SourceOldBackendLog || d.Floor != 3000 || !strings.HasSuffix(d.RateLog, "pg-connector-pr-github/events.jsonl") {
		t.Fatalf("defaults %+v", d)
	}
}

func TestLoadConfigReadsAFileAndRefusesABadKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeFile(t, p, `{"queries_file":"/q","rate_source":"daemon-metrics","pace":"500ms","floor":3500,"unknown_key":1}`)
	c, err := LoadConfig(p)
	if err != nil || c.QueriesFile != "/q" || c.RateSource != SourceDaemonMetrics || c.PaceDuration() != 500*time.Millisecond || c.Floor != 3500 {
		t.Fatalf("%+v %v", c, err)
	}
	for body, want := range map[string]string{`{"rate_source":"nope"}`: "rate_source", `{"pace":"soon"}`: "pace", `[`: "JSON"} {
		writeFile(t, p, body)
		if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
	}
	if _, err := LoadConfig(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("an explicitly named absent file")
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(ConfigEnv, "")
	if c, err := LoadConfig(""); err != nil || c.QueriesFile != "" {
		t.Errorf("the absent default file: %+v %v", c, err)
	}
}
