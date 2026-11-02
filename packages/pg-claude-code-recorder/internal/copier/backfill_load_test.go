//go:build loadtest

package copier

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// TestBackfillRate measures first-copy files/s on a throwaway fixture tree with
// the real clone policy, real SQLite and the real file system. It is a load
// test: run it with -tags loadtest (it is not part of the unit gate, V-4).
// RECORDER_LOAD_FILES sets the file count (default 3000); RECORDER_LOAD_BATCH
// sets the IngestBatch size (0 measures one Ingest call per file).
func TestBackfillRate(t *testing.T) {
	n := 3000
	if v, err := strconv.Atoi(os.Getenv("RECORDER_LOAD_FILES")); err == nil && v > 0 {
		n = v
	}
	batch := 0
	if v, err := strconv.Atoi(os.Getenv("RECORDER_LOAD_BATCH")); err == nil {
		batch = v
	}
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	proj := filepath.Join(claude, "projects", "example-project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("{\"n\":1,\"pad\":\"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"}\n", 50)
	paths := make([]string, n)
	for i := range paths {
		paths[i] = filepath.Join(proj, fmt.Sprintf("session-%06d.jsonl", i))
		if err := os.WriteFile(paths[i], []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(root, "state"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cp, err := New(Config{Store: st, Scope: scope.New(claude, filepath.Join(root, "ends"))})
	if err != nil {
		t.Fatal(err)
	}
	m, why := cp.Method()
	start := time.Now()
	ingestAll(t, cp, paths, batch)
	el := time.Since(start)
	t.Logf("method=%s (%s) files=%d batch=%d elapsed=%s rate=%.1f files/s", m, why, n, batch, el.Round(time.Millisecond), float64(n)/el.Seconds())
	if m != "clone" {
		t.Fatalf("clone method required for the measurement, got %s", m)
	}
}

func ingestAll(t *testing.T, cp *Copier, paths []string, batch int) {
	t.Helper()
	if batch <= 1 {
		for _, p := range paths {
			if _, err := cp.Ingest(p); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for i := 0; i < len(paths); i += batch {
		end := min(i+batch, len(paths))
		for _, r := range cp.IngestBatch(paths[i:end]) {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
	}
}
