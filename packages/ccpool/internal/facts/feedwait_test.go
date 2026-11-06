package facts_test

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

	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// fakeRecorder answers hello and serves feed through feed (status, body).
func fakeRecorder(t *testing.T, feed func(n int32) (int, any)) (string, *atomic.Int32) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/hello"):
			_ = json.NewEncoder(w).Encode(map[string]any{"service": "x", "protocol_version": client.ProtocolVersion, "schema_version": 1, "epoch": 1, "capabilities": []string{"feed", "hello"}, "caught_up": true})
		case strings.HasSuffix(r.URL.Path, "/feed"):
			n := calls.Add(1)
			status, body := feed(n)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock, &calls
}

func busy() (int, any) {
	return http.StatusServiceUnavailable, map[string]any{"error": "busy", "message": "recorder is catching up"}
}

func emptyPage() (int, any) {
	return http.StatusOK, map[string]any{"entries": []any{}, "next_cursor": "1:1"}
}

func newFakeReader(sock string) *facts.Reader {
	return facts.NewWith(client.Options{Socket: sock, Name: "ccpool-test", ConnectTimeout: time.Second})
}

// A feed that keeps answering busy for the whole window says nothing about
// whether the prompt was ingested: WaitMessage must NOT return (false, nil), the
// definitive "no record" the caller turns into a not-ingested verdict.
func TestWaitMessageBusyWholeWindowIsUnansweredNotAbsent(t *testing.T) {
	sock, calls := fakeRecorder(t, func(int32) (int, any) { return busy() })
	r := newFakeReader(sock)
	ok, err := r.WaitMessage(context.Background(), "/p/"+sess+".jsonl", "1:0", 1200*time.Millisecond)
	if ok || !errors.Is(err, facts.ErrFeedUnanswered) {
		t.Fatalf("WaitMessage = %v, %v; want false, ErrFeedUnanswered", ok, err)
	}
	if calls.Load() < 2 {
		t.Fatalf("feed asked %d times; want the busy answer retried inside the window", calls.Load())
	}
}

// A good empty page at the end of the window is the recorder saying nothing
// matched: the definitive (false, nil).
func TestWaitMessageGoodEmptyPageIsAbsent(t *testing.T) {
	sock, _ := fakeRecorder(t, func(int32) (int, any) { return emptyPage() })
	r := newFakeReader(sock)
	if ok, err := r.WaitMessage(context.Background(), "/p/"+sess+".jsonl", "1:0", 1200*time.Millisecond); ok || err != nil {
		t.Fatalf("WaitMessage = %v, %v; want false, nil", ok, err)
	}
}

// Busy early and then a record: the retry inside the window finds it.
func TestWaitMessageRecoversFromBusyAndFindsRecord(t *testing.T) {
	sock, _ := fakeRecorder(t, func(n int32) (int, any) {
		if n == 1 {
			return busy()
		}
		return http.StatusOK, map[string]any{"next_cursor": "1:2", "entries": []any{
			map[string]any{"seq": "1:2", "kind": "record", "record": map[string]any{"file": "f", "gen": 1, "offset": 0, "item": -1}},
		}}
	})
	r := newFakeReader(sock)
	if ok, err := r.WaitMessage(context.Background(), "/p/"+sess+".jsonl", "1:0", 5*time.Second); err != nil || !ok {
		t.Fatalf("WaitMessage = %v, %v; want true, nil", ok, err)
	}
}

// Good empty page first, then busy through the end of the window: the latest
// outcome is a failure, so the verdict is still "unknown".
func TestWaitMessageBusyAtEndAfterGoodPageIsUnanswered(t *testing.T) {
	sock, _ := fakeRecorder(t, func(n int32) (int, any) {
		if n == 1 {
			return http.StatusOK, map[string]any{"entries": []any{}, "next_cursor": "1:1"}
		}
		return busy()
	})
	r := newFakeReader(sock)
	ok, err := r.WaitMessage(context.Background(), "/p/"+sess+".jsonl", "1:0", 1500*time.Millisecond)
	if ok || !errors.Is(err, facts.ErrFeedUnanswered) {
		t.Fatalf("WaitMessage = %v, %v; want false, ErrFeedUnanswered", ok, err)
	}
}
