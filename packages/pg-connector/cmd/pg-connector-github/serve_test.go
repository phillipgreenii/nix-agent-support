package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

// shortTempDir returns a fresh directory with a short absolute path: a unix
// socket path is capped near 104 bytes, and t.TempDir can exceed that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcg")
	if err != nil { // a sandbox without a writable /tmp: fall back to TMPDIR
		d, err = os.MkdirTemp("", "pcg")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// writeServeConfig renders a config file for serve with the given state
// directory and (when non-empty) socket path, and returns its path.
func writeServeConfig(t *testing.T, stateDir, socketPath string) string {
	t.Helper()
	body := map[string]any{"state_dir": stateDir}
	if socketPath != "" {
		body["socket_path"] = socketPath
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(shortTempDir(t), "config.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServeDrainsOnContextCancelInProcess(t *testing.T) {
	state := filepath.Join(shortTempDir(t), "state")
	cfgPath := writeServeConfig(t, state, "")
	cfg, err := config.Load(cfgPath, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() { done <- serveWith(ctx, cfg, &stderr) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, err := net.Dial("unix", cfg.SocketPath); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never listened; stderr: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("serve exit = %d, want 0; stderr: %s", code, stderr.String())
	}
}

// syncBuffer is a bytes.Buffer safe for a test to read while serve writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
