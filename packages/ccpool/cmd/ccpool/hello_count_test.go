package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// fakeRecorder serves only GET /v1/hello on a unix socket and counts the hellos.
func fakeRecorder(t *testing.T) (socket string, hellos *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service": "fake", "protocol_version": client.ProtocolVersion, "caught_up": true,
		})
	})
	dir, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "r.sock")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket, &n
}

// TestCapacity_sendsNoRecorderHello: capacity reads only ccpool's own store and
// tmux, so a multi-pool capacity run sends the recorder no request at all,
// however many pools there are.
func TestCapacity_sendsNoRecorderHello(t *testing.T) {
	socket, hellos := fakeRecorder(t)
	old := factReader
	factReader = facts.NewWith(client.Options{Socket: socket, Name: "ccpool-test", ConnectTimeout: time.Second})
	t.Cleanup(func() { factReader = old })

	svc, st, code := buildService()
	if code != 0 {
		t.Fatalf("buildService = %d", code)
	}
	t.Cleanup(func() { _ = st.Close() })
	const pools = 6
	dirs := make([]string, pools)
	for i := range dirs {
		dirs[i] = fmt.Sprintf("/pool-%d", i)
	}
	rc := emitCapacityForPools(dirs, func(string) error {
		_, err := svc.Capacity(context.Background(), 6)
		return err
	})
	if rc != 0 {
		t.Fatalf("emitCapacityForPools = %d, want 0", rc)
	}
	if got := hellos.Load(); got != 0 {
		t.Fatalf("recorder saw %d hellos for %d pools, want 0", got, pools)
	}
}

// TestCapacity_succeedsWithRecorderUnreachable: the production wiring with the
// recorder's socket absent still reports capacity.
func TestCapacity_succeedsWithRecorderUnreachable(t *testing.T) {
	old := factReader
	factReader = facts.NewWith(client.Options{Socket: filepath.Join(t.TempDir(), "none.sock"), Name: "ccpool-test", ConnectTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { factReader = old })

	svc, st, code := buildService()
	if code != 0 {
		t.Fatalf("buildService = %d", code)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := svc.Capacity(context.Background(), 6)
	if err != nil {
		t.Fatalf("Capacity with the recorder unreachable: %v", err)
	}
	if c.Free != 6 {
		t.Fatalf("capacity = %+v; want Free 6", c)
	}
}
