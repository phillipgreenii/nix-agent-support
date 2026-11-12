package serve

import (
	"context"
	"testing"
)

// BenchmarkComputeCoverageLazy measures one lazy coverage pass over a seeded
// fixture (the fixture bypasses the journal, so the status rows are rebuilt
// once and then served from the cache). Not part of the unit gate. Run:
//
//	go test -run '^$' -bench BenchmarkComputeCoverageLazy -benchmem ./internal/serve/
func BenchmarkComputeCoverageLazy(b *testing.B) {
	e := newEnv(b, false)
	d := e.open(b)
	seedFiles(b, d, e.claude, 20000)
	if err := d.Close(); err != nil {
		b.Fatal(err)
	}
	d = e.open(b) // the reopen seeds the lag state from the stored generations
	defer func() { _ = d.Close() }()
	ctx := context.Background()
	if _, err := d.computeCoverageFrom(ctx, &d.st.Reader, "summary", false, statLazy, 0); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.computeCoverageFrom(ctx, &d.st.Reader, "summary", false, statLazy, 0); err != nil {
			b.Fatal(err)
		}
	}
}
