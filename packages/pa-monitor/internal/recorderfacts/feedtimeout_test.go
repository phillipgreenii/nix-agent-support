package recorderfacts

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// feedStall serves hello and process.list at once and stalls every feed and
// snapshot request until the test ends, like a recorder whose registry feed
// read is slow. It counts the requests it saw per path.
func feedStall(t *testing.T) (sock string, count func(suffix string) int) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pamon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "r.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	done := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/hello":
			_, _ = w.Write([]byte(`{"protocol_version":1}`))
		case strings.HasSuffix(r.URL.Path, "feed"), strings.HasSuffix(r.URL.Path, "snapshot"):
			select {
			case <-done:
			case <-r.Context().Done():
			}
		default:
			_, _ = w.Write([]byte(`{"processes":[]}`))
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(done); _ = srv.Close() })
	return sock, func(suffix string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for p, c := range seen {
			if strings.HasSuffix(p, suffix) {
				n += c
			}
		}
		return n
	}
}

// A registry feed read that stalls is bounded by its own short timeout, so the
// discovery that follows still has its context and succeeds, and once there is
// a snapshot a later stalled feed read is not mistaken for a registry change.
func TestStalledRegistryFeedDoesNotStarveDiscovery(t *testing.T) {
	sock, count := feedStall(t)
	f := New(client.Options{Socket: sock})
	f.feedTimeout = 150 * time.Millisecond
	f.SetDiscoveryHeartbeat(time.Hour)
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatalf("first call: discovery was starved by the feed read: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("first call took %v, want about the feed timeout", d)
	}
	if n := count("process.list"); n == 0 {
		t.Fatal("first call sent no process.list")
	}
	if f.regCursor != "" {
		t.Errorf("cursor = %q after a stalled seed, want it left empty", f.regCursor)
	}

	// A cursor is held and the feed stalls: a timeout is not a change.
	f.regCursor = "1:2"
	before := count("process.list")
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := count("process.list"); got != before {
		t.Errorf("a feed timeout triggered discovery: process.list %d -> %d", before, got)
	}
	if f.regCursor != "1:2" {
		t.Errorf("cursor = %q, want it kept across a timeout", f.regCursor)
	}
}
