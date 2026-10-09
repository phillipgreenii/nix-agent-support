package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/config"
	"github.com/phillipgreenii/beads-exporter/internal/logx"
)

// fakeBD writes a bd stand-in that answers every subcommand the exporter uses
// with a canned envelope, using only shell builtins so the child PATH needs
// nothing. The one claimed bead is assigned to an actor no transcript names.
func fakeBD(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
case "$1" in
list)
  printf '%s\n' '{"data":[{"id":"alpha-1","status":"in_progress","issue_type":"task","priority":2,"assignee":"worker-gamma","created_at":"2026-01-02T03:04:05Z","started_at":"2026-01-02T03:04:05Z"}],"schema_version":1}' ;;
ready|blocked)
  printf '%s\n' '{"data":[],"schema_version":1}' ;;
count)
  printf '%s\n' '{"data":{"groups":[{"group":"in_progress","count":1}]},"schema_version":1}' ;;
statuses)
  printf '%s\n' '{"data":{"built_in_statuses":[{"name":"open"},{"name":"in_progress"},{"name":"closed"}],"custom_statuses":[]},"schema_version":1}' ;;
*)
  echo "unexpected: $*" >&2
  exit 3 ;;
esac
`
	path := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

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

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestServeRunsTheStrandedPassOnItsInterval proves the stranded pass is wired
// into the running exporter: it is scheduled, it reaches /metrics and it logs
// one line per claim with no live owner.
func TestServeRunsTheStrandedPassOnItsInterval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claude := t.TempDir()
	if err := os.MkdirAll(filepath.Join(claude, "projects", "-synthetic-slug"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	cfgPath := writeConfig(t, func(m map[string]any) {
		m["bdPath"] = fakeBD(t)
		m["childPath"] = "/nonexistent/bin"
		m["beadsDirs"] = map[string]string{"alpha": t.TempDir()}
		m["claudeDir"] = claude
		m["operatorNames"] = []string{}
		m["port"] = port
		m["pollIntervalSeconds"] = 3600
		m["strandedIntervalSeconds"] = 3600
	})
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, logx.New(&logs, slog.LevelInfo)) }()

	want := `beads_stranded_claims{db="alpha",status="in_progress"} 1`
	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	deadline := time.Now().Add(20 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(raw)
			if strings.Contains(body, want) {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("serve returned early: %v\nlogs: %s", err, logs.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !strings.Contains(body, want) {
		t.Fatalf("/metrics lacks %s:\n%s\nlogs: %s", want, body, logs.String())
	}
	if !strings.Contains(body, `beads_exporter_pass_last_success_timestamp_seconds{db="alpha",pass="stranded"}`) {
		t.Fatalf("/metrics lacks the stranded pass timestamp:\n%s", body)
	}

	var found bool
	for _, line := range strings.Split(logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["event"] == "stranded_claim" {
			found = rec["bead"] == "alpha-1" && rec["assignee"] == "worker-gamma" && rec["db"] == "alpha"
		}
	}
	if !found {
		t.Fatalf("no stranded_claim log line for the claimed bead:\n%s", logs.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}
