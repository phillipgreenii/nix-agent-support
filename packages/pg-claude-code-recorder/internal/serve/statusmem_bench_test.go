package serve

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// benchFiles is the size of the benchmark fixture: about what the live daemon
// holds (roughly 100k files and as many current paths).
const benchFiles = 100_000

// commitFixture adds n append files, each with one current path, one generation
// and a copy offset, through the store's journal (Commit), so the commit
// counters and the lag state move as they do in the daemon. Direct SQL seeding
// would bypass them and measure only a full rebuild.
func commitFixture(tb testing.TB, d *Daemon, dir string, n int) {
	tb.Helper()
	const batch = 500
	recs := make([]*store.Rec, 0, batch*4)
	flush := func() {
		if len(recs) == 0 {
			return
		}
		if err := d.st.Commit(recs...); err != nil {
			tb.Fatal(err)
		}
		recs = recs[:0]
	}
	for i := 1; i <= n; i++ {
		id := int64(200000 + i)
		recs = append(
			recs,
			&store.Rec{T: store.TFile, At: 1, File: id, Source: "transcript", Mode: "append", Size: 100},
			&store.Rec{T: store.TPath, At: 1, File: id, Path: filepath.Join(dir, fmt.Sprintf("f%d.jsonl", i))},
			&store.Rec{T: store.TGen, At: 1, File: id, Gen: 1},
			&store.Rec{T: store.TCommit, At: 2, File: id, Gen: 1, From: 0, To: 100, Size: 100},
		)
		if len(recs) >= batch*4 {
			flush()
		}
	}
	flush()
}

// benchDaemon opens a daemon over a fixture of n files committed through the
// store and fills the status caches once.
func benchDaemon(b *testing.B, n int) *Daemon {
	b.Helper()
	e := newEnv(b, false)
	d := e.open(b)
	b.Cleanup(func() { _ = d.Close() })
	commitFixture(b, d, filepath.Join(e.claude, "projects", "example-proj"), n)
	if err := d.refreshStatus(); err != nil {
		b.Fatal(err)
	}
	return d
}

// churn commits one copy offset advance, which moves the commit counter the way
// the steady stream of copy commits does and invalidates the file-row cache.
func churn(b *testing.B, d *Daemon, i int) {
	b.Helper()
	to := int64(101 + i)
	if err := d.st.Commit(&store.Rec{T: store.TCommit, At: int64(10 + i), File: 200001, Gen: 1, From: to - 1, To: to, Size: to}); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkStatusRows measures the file-row read the status loop does after a
// commit moved the store's commit counter. Run: go test -run '^$' -bench
// StatusRows -benchmem ./internal/serve
func BenchmarkStatusRows(b *testing.B) {
	d := benchDaemon(b, benchFiles)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		churn(b, d, i)
		seq := d.st.CommitSeq()
		b.StartTimer()
		err := d.st.ReadTxCtx(context.Background(), func(rd *store.Reader) error {
			_, _, e := d.statusRows(rd, seq)
			return e
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkComputeCoverageLazyAfterCommit measures one status-loop coverage
// pass (lazy stat mode) after a commit. Unlike BenchmarkComputeCoverageLazy (a
// 20k seeded fixture served from the cache), it runs over the ~100k-file
// fixture committed through the store and pays the post-commit rebuild on every
// iteration.
func BenchmarkComputeCoverageLazyAfterCommit(b *testing.B) {
	d := benchDaemon(b, benchFiles)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		churn(b, d, i)
		b.StartTimer()
		if _, err := d.computeCoverageMode("summary", false, statLazy); err != nil {
			b.Fatal(err)
		}
	}
}
