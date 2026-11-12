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

	"github.com/phillipgreenii/ccpool/internal/config"
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

// TestMultiPoolEmit_recorderHellosDoNotScaleWithPools: a multi-pool emit run
// builds one service per pool and each consults its Gate before reporting
// capacity. The recorder must see at most 2 hellos (Dial's, plus at most one
// ping) however many pools there are.
func TestMultiPoolEmit_recorderHellosDoNotScaleWithPools(t *testing.T) {
	socket, hellos := fakeRecorder(t)
	old := factReader
	factReader = facts.NewWith(client.Options{Socket: socket, Name: "ccpool-test", ConnectTimeout: time.Second})
	t.Cleanup(func() { factReader = old })

	const pools = 6
	dirs := make([]string, pools)
	for i := range dirs {
		dirs[i] = fmt.Sprintf("/pool-%d", i)
	}
	rc := emitCapacityForPools(dirs, func(string) error {
		// what Service.Capacity does per pool, with the production wiring
		return newSessionDeps(config.Config{}, nil, nil).Gate.Ping(context.Background())
	})
	if rc != 0 {
		t.Fatalf("emitCapacityForPools = %d, want 0", rc)
	}
	if got := hellos.Load(); got < 1 || got > 2 {
		t.Fatalf("recorder saw %d hellos for %d pools, want 1..2", got, pools)
	}
}
