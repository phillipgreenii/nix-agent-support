package serve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A status call that hits the cache must allocate a small constant number of
// objects, independent of how many files the store holds. A change that makes
// the call rebuild or copy a per-file structure fails here at once. The bound is
// a modest margin over the measured count (see the log line).
func TestCacheHitStatusAllocatesAConstantNumberOfObjects(t *testing.T) {
	const maxStatusAllocs = 40 // measured 31 for both sizes
	const maxRowsAllocs = 0    // a statusRows cache hit returns the cached slices
	count := func(files int) (status, rows float64) {
		e := newEnv(t, false)
		d := e.open(t)
		defer func() { _ = d.Close() }()
		commitFixture(t, d, filepath.Join(e.claude, "projects", "example-proj"), files)
		if err := d.refreshStatus(); err != nil {
			t.Fatal(err)
		}
		seq := d.st.CommitSeq()
		err := d.st.ReadTxCtx(context.Background(), func(rd *store.Reader) error {
			_, _, e := d.statusRows(rd, seq)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		status = testing.AllocsPerRun(20, func() {
			if _, werr := d.opStatus(context.Background(), nil); werr != nil {
				t.Fatal(werr)
			}
		})
		rows = testing.AllocsPerRun(20, func() {
			if _, _, err := d.statusRows(nil, seq); err != nil {
				t.Fatal(err)
			}
		})
		return status, rows
	}
	s1, r1 := count(10)
	s2, r2 := count(400)
	t.Logf("opStatus allocs: %.0f (10 files), %.0f (400 files); statusRows hit allocs: %.0f, %.0f", s1, s2, r1, r2)
	if s1 > maxStatusAllocs || s2 > maxStatusAllocs {
		t.Fatalf("a cache-hit status call allocated %.0f and %.0f objects, want at most %d", s1, s2, maxStatusAllocs)
	}
	if s2 > s1+2 {
		t.Fatalf("status allocations grew with the file count: %.0f for 10 files, %.0f for 400", s1, s2)
	}
	if r1 > maxRowsAllocs || r2 > maxRowsAllocs {
		t.Fatalf("a statusRows cache hit allocated %.0f and %.0f objects, want %d", r1, r2, maxRowsAllocs)
	}
}
