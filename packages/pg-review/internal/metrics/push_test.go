package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCmd struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeCmd) run(_ context.Context, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.err
}

func testPusher(env map[string]string, cmd *fakeCmd, onPath bool) (*Pusher, *bytes.Buffer) {
	var dbg bytes.Buffer
	p := NewPusher()
	p.Getenv = func(k string) string { return env[k] }
	p.Debug = &dbg
	p.Timeout = 300 * time.Millisecond
	p.RunCmd = cmd.run
	p.LookPath = func(string) (string, error) {
		if onPath {
			return "/fake/ccpool", nil
		}
		return "", errors.New("not found")
	}
	return p, &dbg
}

var sample = []Gauge{
	{Name: "pg_review_pending_items", Labels: map[string]string{"lens": "lens-a", "scope": "scope-a"}, Value: 3},
	{Name: "pg_review_runs", Labels: map[string]string{"lens": "lens-a", "scope": "scope-a", "outcome": "filed"}, Value: 2},
}

func TestNoEndpointIsSilentNoOp(t *testing.T) {
	cmd := &fakeCmd{}
	p, dbg := testPusher(map[string]string{DebugEnv: "1"}, cmd, true)
	p.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("a request was made with no endpoint")
		return nil, errors.New("unexpected")
	})}
	p.Push(context.Background(), sample)
	if len(cmd.calls) != 0 || dbg.Len() != 0 {
		t.Fatalf("calls %v debug %q", cmd.calls, dbg.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPushPostsOTLPJSON(t *testing.T) {
	var got []byte
	var path, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		path, ctype = r.URL.Path, r.Header.Get("Content-Type")
	}))
	defer srv.Close()
	cmd := &fakeCmd{}
	p, dbg := testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": srv.URL, "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json"}, cmd, true)
	p.Push(context.Background(), sample)
	if path != "/v1/metrics" || ctype != "application/json" {
		t.Fatalf("path %q type %q", path, ctype)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pg_review_pending_items", "pg_review_runs", `"stringValue":"scope-a"`, `"service.name"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("payload lacks %s: %s", want, got)
		}
	}
	if len(cmd.calls) != 0 || dbg.Len() != 0 {
		t.Errorf("fallback or debug output after a good push: %v %q", cmd.calls, dbg.String())
	}
}

func TestUnreachableEndpointLogsAtDebugAndFallsBackToCcpool(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // now unreachable
	cmd := &fakeCmd{}
	p, dbg := testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": url, DebugEnv: "1"}, cmd, true)
	start := time.Now()
	p.Push(context.Background(), sample)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("push took %v", time.Since(start))
	}
	if !strings.Contains(dbg.String(), "OTLP push failed") {
		t.Errorf("debug log %q", dbg.String())
	}
	if len(cmd.calls) != 2 {
		t.Fatalf("fallback calls %v", cmd.calls)
	}
	joined := strings.Join(cmd.calls[0], " ")
	if !strings.HasPrefix(joined, "ccpool emit-gauge --name pg_review_pending_items --value 3 --label lens=lens-a --label scope=scope-a") {
		t.Errorf("call %q", joined)
	}
}

func TestDebugOffIsSilentAndNoCcpoolMeansNoFallback(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	cmd := &fakeCmd{}
	p, dbg := testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": url}, cmd, false)
	p.Push(context.Background(), sample)
	if dbg.Len() != 0 || len(cmd.calls) != 0 {
		t.Fatalf("debug %q calls %v", dbg.String(), cmd.calls)
	}
}

func TestGRPCProtocolUsesFallback(t *testing.T) {
	cmd := &fakeCmd{}
	p, _ := testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, cmd, true)
	p.Push(context.Background(), sample)
	if len(cmd.calls) != 2 {
		t.Fatalf("calls %v", cmd.calls)
	}
}

func TestHangingEndpointIsBoundedByTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	cmd := &fakeCmd{}
	p, _ := testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": srv.URL}, cmd, false)
	start := time.Now()
	p.Push(context.Background(), sample)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("push took %v with a 300ms timeout", d)
	}
}

func TestFailingFallbackIsSwallowed(t *testing.T) {
	cmd := &fakeCmd{err: errors.New("boom")}
	p, dbg := testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc", DebugEnv: "1"}, cmd, true)
	p.Push(context.Background(), sample)
	if !strings.Contains(dbg.String(), "fallback failed") || len(cmd.calls) != 1 {
		t.Fatalf("debug %q calls %v", dbg.String(), cmd.calls)
	}
}

func TestEmitSwallowsEverything(t *testing.T) {
	Emit(context.Background(), nil, time.Now()) // nil ledger: no panic
	l := openLedger(t)
	saved := Default
	t.Cleanup(func() { Default = saved })
	cmd := &fakeCmd{}
	Default, _ = testPusher(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, cmd, true)
	Emit(context.Background(), l, time.Now())
	if len(cmd.calls) == 0 {
		t.Error("an empty ledger still emits the zero pending gauge")
	}
	_ = l.Close()
	Emit(context.Background(), l, time.Now()) // closed ledger: logged, not raised
}
