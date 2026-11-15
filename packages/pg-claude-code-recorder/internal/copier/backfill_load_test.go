//go:build loadtest

package copier

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// loadBatchSize is the IngestBatch size the batched measurement uses (the
// production backfill batch size).
const loadBatchSize = 64

// countingFS wraps a state-directory file system and counts directory syncs.
type countingFS struct {
	fsx.FS
	dirSyncs atomic.Int64
}

func (c *countingFS) SyncDir(dir string) error {
	c.dirSyncs.Add(1)
	return c.FS.SyncDir(dir)
}

// backfillStats is what one measured backfill did: the durability operations it
// issued (deterministic) and its rate (informational only).
type backfillStats struct {
	rate     float64
	commits  uint64 // journal commits (one fsync and one SQLite transaction each)
	dirSyncs int64  // directory fsyncs
}

// TestBackfillRate backfills a throwaway fixture tree with the real clone
// policy, real SQLite and the real file system, once with one Ingest call per
// file and once with IngestBatch(64). It asserts the DETERMINISTIC effect of
// batching: the journal commits and directory syncs (the operations that
// dominate cost) drop from one per file to one per batch. Wall-clock rates are
// logged but never compared, because two timings taken at different moments
// differ with host load. It is a load test: run it with -tags loadtest (it is
// not part of the unit gate, V-4); the flake check
// pg-claude-code-recorder-load-tests runs it. RECORDER_LOAD_FILES sets the file
// count (default 3000).
func TestBackfillRate(t *testing.T) {
	n := 3000
	if v, err := strconv.Atoi(os.Getenv("RECORDER_LOAD_FILES")); err == nil && v > 0 {
		n = v
	}
	single := measureBackfill(t, n, 0)
	batched := measureBackfill(t, n, loadBatchSize)
	t.Logf("SUMMARY files=%d single=%.1f files/s commits=%d dirSyncs=%d; batched(%d)=%.1f files/s commits=%d dirSyncs=%d (rates informational)",
		n, single.rate, single.commits, single.dirSyncs, loadBatchSize, batched.rate, batched.commits, batched.dirSyncs)
	if single.commits != uint64(n) || single.dirSyncs != int64(n) {
		t.Fatalf("single-file ingest should commit and dir-sync once per file (%d), got commits=%d dirSyncs=%d", n, single.commits, single.dirSyncs)
	}
	maxOps := (n + loadBatchSize - 1) / loadBatchSize
	if batched.commits > uint64(maxOps) || batched.dirSyncs > int64(maxOps) {
		t.Fatalf("batched ingest should commit and dir-sync at most once per batch (%d), got commits=%d dirSyncs=%d", maxOps, batched.commits, batched.dirSyncs)
	}
}

// measureBackfill ingests n fresh fixture files (batch <= 1 means one Ingest
// call per file) and returns what it did.
func measureBackfill(t *testing.T, n, batch int) backfillStats {
	t.Helper()
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
	cfs := &countingFS{FS: fsx.OS{}}
	st, err := store.Open(filepath.Join(root, "state"), store.Options{FS: cfs})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cp, err := New(Config{Store: st, Scope: scope.New(claude, filepath.Join(root, "ends"))})
	if err != nil {
		t.Fatal(err)
	}
	m, why := cp.Method()
	seq0, sync0 := st.CommitSeq(), cfs.dirSyncs.Load()
	start := time.Now()
	ingestAll(t, cp, paths, batch)
	el := time.Since(start)
	out := backfillStats{rate: float64(n) / el.Seconds(), commits: st.CommitSeq() - seq0, dirSyncs: cfs.dirSyncs.Load() - sync0}
	t.Logf("method=%s (%s) files=%d batch=%d elapsed=%s rate=%.1f files/s commits=%d dirSyncs=%d", m, why, n, batch, el.Round(time.Millisecond), out.rate, out.commits, out.dirSyncs)
	if m != "clone" {
		t.Fatalf("clone method required for the measurement, got %s", m)
	}
	return out
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
