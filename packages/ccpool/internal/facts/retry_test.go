package facts

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// shortRetry makes the retry policy fast for a test and restores it after.
func shortRetry(t *testing.T, attempts int) {
	t.Helper()
	a, b, d := readAttempts, readBackoff, readAttemptTimeout
	readAttempts, readBackoff, readAttemptTimeout = attempts, time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { readAttempts, readBackoff, readAttemptTimeout = a, b, d })
}

// slowRecorder serves hello and query on a unix socket. The first slowHellos
// hellos wait past the attempt timeout (a recorder whose hello queues behind its
// write connection); with slowHellos < 0 every hello waits that long.
func slowRecorder(t *testing.T, slowHellos int64) (opts client.Options, hellos, queries *atomic.Int64) {
	t.Helper()
	hellos, queries = new(atomic.Int64), new(atomic.Int64)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", func(w http.ResponseWriter, r *http.Request) {
		n := hellos.Add(1)
		if slowHellos < 0 || n <= slowHellos {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service": "fake", "protocol_version": client.ProtocolVersion, "caught_up": true,
		})
	})
	mux.HandleFunc("/v1/query", func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"columns": []string{"time"},
			"rows":    [][]any{{"2026-01-01T00:00:00Z"}},
			"as_of":   "c1",
		})
	})
	dir, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return client.Options{Socket: sock, Name: "retry-test", ConnectTimeout: time.Second}, hellos, queries
}

// TestRead_slowHelloIsRetriedThenSucceeds: a hello at connect time that misses
// the attempt budget twice is retried and the read then succeeds.
func TestRead_slowHelloIsRetriedThenSucceeds(t *testing.T) {
	shortRetry(t, 4)
	opts, hellos, queries := slowRecorder(t, 2)
	r := NewWith(opts)
	mark, err := r.Mark(context.Background())
	if err != nil {
		t.Fatalf("Mark with a slow hello: %v", err)
	}
	if mark != "c1" {
		t.Fatalf("mark = %q, want c1", mark)
	}
	if got := hellos.Load(); got != 3 {
		t.Fatalf("hellos = %d, want 3 (two slow, one good)", got)
	}
	if queries.Load() != 1 {
		t.Fatalf("queries = %d, want 1", queries.Load())
	}
}

// TestRead_neverAnsweringRecoversWithClearError: a recorder that never answers
// is tried readAttempts times and the read fails with an error that names the
// recorder and the attempts, and sends no query.
func TestRead_neverAnsweringRecoversWithClearError(t *testing.T) {
	shortRetry(t, 3)
	opts, hellos, queries := slowRecorder(t, -1)
	r := NewWith(opts)
	_, err := r.OutputTokens("/p/" + "s.jsonl")
	if err == nil {
		t.Fatal("want an error from a recorder that never answers")
	}
	if msg := err.Error(); !strings.Contains(msg, "the recorder did not answer after 3 attempts") {
		t.Fatalf("err = %v; want it to name the recorder and the attempts", err)
	}
	if got := hellos.Load(); got != 3 {
		t.Fatalf("hellos = %d, want exactly 3 attempts", got)
	}
	if queries.Load() != 0 {
		t.Fatalf("queries = %d, want 0", queries.Load())
	}
}

// TestRead_unreachableSocketRetries: with no socket the read still retries the
// bounded number of times and reports the recorder as unavailable.
func TestRead_unreachableSocketRetries(t *testing.T) {
	shortRetry(t, 3)
	r := NewWith(client.Options{Socket: filepath.Join(t.TempDir(), "none.sock"), ConnectTimeout: 50 * time.Millisecond})
	start := time.Now()
	_, err := r.ProcessAlive(1, "x")
	if !errors.Is(err, ErrDaemonOffline) {
		t.Fatalf("err = %v; want ErrDaemonOffline", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("retries not bounded: took %s", time.Since(start))
	}
}

// TestPing_isRetried: Ping goes through the same retry.
func TestPing_isRetried(t *testing.T) {
	shortRetry(t, 4)
	opts, hellos, _ := slowRecorder(t, 1)
	if err := NewWith(opts).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if hellos.Load() < 2 {
		t.Fatalf("hellos = %d, want a retry", hellos.Load())
	}
}

// TestRead_cancelledContextStopsRetries: a cancelled caller context ends the
// retries without exhausting the attempts.
func TestRead_cancelledContextStopsRetries(t *testing.T) {
	shortRetry(t, 50)
	opts, hellos, _ := slowRecorder(t, -1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := NewWith(opts).Mark(ctx); err == nil {
		t.Fatal("want an error")
	}
	if hellos.Load() >= 50 {
		t.Fatalf("hellos = %d; retries ignored the cancelled context", hellos.Load())
	}
}
