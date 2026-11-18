package facts

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// spyMetrics swaps the metric seams for counters keyed by op.
func spyMetrics(t *testing.T) (retries, failures map[string]int) {
	t.Helper()
	retries, failures = map[string]int{}, map[string]int{}
	or, of := recordRetry, recordFailure
	recordRetry = func(op string) { retries[op]++ }
	recordFailure = func(op string) { failures[op]++ }
	t.Cleanup(func() { recordRetry, recordFailure = or, of })
	return retries, failures
}

// captureLog routes slog to a buffer for the test.
func captureLog(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &b
}

// TestRead_slowHelloFinalErrorIsDaemonOffline: a hello that never meets the
// attempt budget ends as ErrDaemonOffline, like an unreachable socket, and the
// text says what failed and what to do.
func TestRead_slowHelloFinalErrorIsDaemonOffline(t *testing.T) {
	shortRetry(t, 2)
	opts, _, _ := slowRecorder(t, -1)
	_, err := NewWith(opts).OutputTokens("/p/s.jsonl")
	if !errors.Is(err, ErrDaemonOffline) {
		t.Fatalf("err = %v; want it to match ErrDaemonOffline", err)
	}
	if !strings.Contains(err.Error(), "check that the recorder daemon is running") {
		t.Fatalf("err = %v; want what to do", err)
	}
}

// TestRead_finalFailureIsLoggedAndCounted: every retry and the final failure are
// logged and counted by op.
func TestRead_finalFailureIsLoggedAndCounted(t *testing.T) {
	shortRetry(t, 3)
	retries, failures := spyMetrics(t)
	logs := captureLog(t)
	r := NewWith(client.Options{Socket: filepath.Join(t.TempDir(), "none.sock"), ConnectTimeout: 50 * time.Millisecond})
	if _, err := r.ProcessAlive(1, "x"); err == nil {
		t.Fatal("want an error")
	}
	if len(retries) != 1 || len(failures) != 1 {
		t.Fatalf("retries = %v, failures = %v; want one op each", retries, failures)
	}
	for op, n := range retries {
		if n != 2 || failures[op] != 1 {
			t.Fatalf("op %s: retries = %d (want 2), failures = %d (want 1)", op, n, failures[op])
		}
	}
	if !strings.Contains(logs.String(), "recorder read failed after the last attempt") {
		t.Fatalf("no final-failure log line in %q", logs.String())
	}
}

// TestRead_recoveredReadCountsRetryNotFailure: a read that recovers counts its
// retry and no failure.
func TestRead_recoveredReadCountsRetryNotFailure(t *testing.T) {
	shortRetry(t, 4)
	retries, failures := spyMetrics(t)
	opts, _, _ := slowRecorder(t, 1)
	if _, err := NewWith(opts).Mark(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %v; want none", failures)
	}
	total := 0
	for _, n := range retries {
		total += n
	}
	if total != 1 {
		t.Fatalf("retries = %v; want exactly one", retries)
	}
}

// TestRead_flappingRecorder: a recorder that is down at first and comes up while
// the read retries is served within that call; after it drops and returns, the
// next read dials again and succeeds. No read fails finally.
func TestRead_flappingRecorder(t *testing.T) {
	shortRetry(t, 6)
	_, failures := spyMetrics(t)
	dir, err := os.MkdirTemp("", "flap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	r := NewWith(client.Options{Socket: sock, Name: "flap", ConnectTimeout: 100 * time.Millisecond})

	start := func() *http.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/hello", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"service": "fake", "protocol_version": client.ProtocolVersion, "caught_up": true})
		})
		mux.HandleFunc("/v1/query", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"columns": []string{"time"}, "rows": [][]any{{"t"}}, "as_of": "c1"})
		})
		l, lerr := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
		if lerr != nil {
			t.Error(lerr)
			return nil
		}
		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(l) }()
		return srv
	}

	srvCh := make(chan *http.Server, 1)
	go func() {
		time.Sleep(15 * time.Millisecond)
		srvCh <- start()
	}()
	if _, err := r.Mark(context.Background()); err != nil {
		t.Fatalf("read while the recorder comes up: %v", err)
	}
	srv := <-srvCh
	if srv == nil {
		return
	}

	_ = srv.Close()
	_ = os.Remove(sock)
	go func() {
		time.Sleep(15 * time.Millisecond)
		srvCh <- start()
	}()
	if _, err := r.Mark(context.Background()); err != nil {
		t.Fatalf("read after the recorder dropped and returned: %v", err)
	}
	if s := <-srvCh; s != nil {
		_ = s.Close()
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %v; want none", failures)
	}
}
