package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/sessionmeta"
)

func TestCollectPoolSessions_groupsByPoolAndExpandsMeta(t *testing.T) {
	ctx := context.Background()
	s, err := sessionmeta.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	mustSet(t, s, "pg-router-worker-zr-1-x", map[string]string{"pgrouter.pool": "pg-router", "pgrouter.bead": "zr-1", "pgrouter.role": "worker"})
	mustSet(t, s, "pg-router-feedback-zr-2-y", map[string]string{"pgrouter.pool": "pg-router", "pgrouter.bead": "zr-2", "pgrouter.role": "feedback"})
	mustSet(t, s, "other-tool-sess", map[string]string{"pgrouter.pool": "something-else", "pgrouter.bead": "zz-9"})

	rows, err := collectPoolSessions(ctx, s)
	if err != nil {
		t.Fatalf("collectPoolSessions: %v", err)
	}
	want := []sessionRow{
		{ExternalID: "pg-router-feedback-zr-2-y", Bead: "zr-2", Role: "feedback"},
		{ExternalID: "pg-router-worker-zr-1-x", Bead: "zr-1", Role: "worker"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v, want %v (sorted, foreign excluded)", rows, want)
	}
}

func mustSet(t *testing.T, s *sessionmeta.Store, ext string, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := s.Set(context.Background(), ext, k, v); err != nil {
			t.Fatalf("Set(%s,%s): %v", ext, k, err)
		}
	}
}

func TestRenderSessions_format(t *testing.T) {
	var b bytes.Buffer
	renderSessions(&b, []sessionRow{{ExternalID: "pg-router-worker-zr-1-x", Bead: "zr-1", Role: "worker"}})
	got := b.String()
	for _, want := range []string{"pool sessions (1):", "pg-router-worker-zr-1-x", "bead=zr-1", "role=worker"} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// TestRunSessions_resolvesPoolFromCCPOOLPOOL exercises the real runSessions command
// end-to-end: it seeds the pool that CCPOOL_POOL resolves to (the SAME resolution
// `ccpool new` uses) and asserts the command lists that session. This guards the
// OpenPool(os.Getenv("CCPOOL_POOL")) resolution — OpenPool("") would read a different
// (default-XDG) pool than the one ccpool wrote to.
func TestRunSessions_resolvesPoolFromCCPOOLPOOL(t *testing.T) {
	pool := t.TempDir()
	t.Setenv("CCPOOL_POOL", pool)
	s, err := sessionmeta.OpenPool(pool)
	if err != nil {
		t.Fatalf("OpenPool: %v", err)
	}
	mustSet(t, s, "pg-router-worker-zr-1-x", map[string]string{"pgrouter.pool": "pg-router", "pgrouter.bead": "zr-1", "pgrouter.role": "worker"})
	_ = s.Close()

	// Capture stdout from the real command (it writes to os.Stdout).
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := runSessions()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if code != exitOK {
		t.Fatalf("runSessions exit = %d, want %d; out=%s", code, exitOK, out)
	}
	got := string(out)
	for _, want := range []string{"pg-router-worker-zr-1-x", "bead=zr-1", "role=worker"} {
		if !strings.Contains(got, want) {
			t.Errorf("runSessions output missing %q:\n%s", want, got)
		}
	}
}
