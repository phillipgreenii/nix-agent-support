package main

import (
	"net/http"
	"testing"
	"time"
)

// TestNewHTTPServer_HasTimeouts guards bead pg2-n6d8y item 3: a bare
// http.Server has no timeouts at all, so a stalled or abandoned client held a
// connection (and its goroutine) forever.
func TestNewHTTPServer_HasTimeouts(t *testing.T) {
	h := http.NewServeMux()
	srv := newHTTPServer("127.0.0.1:0", h)
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q", srv.Addr)
	}
	if srv.Handler != h {
		t.Error("Handler not set")
	}
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s = %v, want > 0", name, d)
		}
	}
	// The write deadline must outlast the /metrics scrape deadline, or the
	// scrape's own 503 could never be written.
	if srv.WriteTimeout <= 30*time.Second {
		t.Errorf("WriteTimeout = %v, must exceed the 30s scrape deadline", srv.WriteTimeout)
	}
}
