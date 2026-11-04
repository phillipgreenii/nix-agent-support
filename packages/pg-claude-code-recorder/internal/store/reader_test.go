package store

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

var readerDriverSeq int

func openCounting(t *testing.T, opt Options) (*Store, *countdriver.Counter) {
	t.Helper()
	readerDriverSeq++
	name := fmt.Sprintf("store-reader-test-%d", readerDriverSeq)
	c := countdriver.Register(name)
	opt.DriverName = name
	s, err := Open(t.TempDir(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, c
}

// snapshotOf reads the same facts through any Reader.
type readerFacts struct {
	Files  []File
	Gens   []Generation
	Paths  map[int64][]string
	Stages []StageRow
}

func factsOf(t *testing.T, r *Reader) readerFacts {
	t.Helper()
	var f readerFacts
	var err error
	if f.Files, err = r.AllFiles(); err != nil {
		t.Fatal(err)
	}
	if f.Gens, err = r.AllGenerations(); err != nil {
		t.Fatal(err)
	}
	if f.Paths, err = r.AllCurrentPaths(); err != nil {
		t.Fatal(err)
	}
	if f.Stages, err = r.Stages(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReadTxGivesTheSameAnswersAsPerStatementReads(t *testing.T) {
	s, _ := openCounting(t, Options{})
	for i := 0; i < 5; i++ {
		seed(t, s)
	}
	perStatement := factsOf(t, &s.Reader)
	var inTx readerFacts
	if err := s.ReadTx(func(r *Reader) error { inTx = factsOf(t, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(perStatement.Files) != 5 {
		t.Fatalf("fixture has %d files", len(perStatement.Files))
	}
	if !reflect.DeepEqual(perStatement, inTx) {
		t.Fatalf("read transaction changed the answers:\nper statement %+v\nin tx         %+v", perStatement, inTx)
	}
}

func TestReadTxSeesOneSnapshotWhileAWriterCommits(t *testing.T) {
	s, _ := openCounting(t, Options{})
	seed(t, s)
	err := s.ReadTx(func(r *Reader) error {
		before, _ := r.AllFiles()
		seed(t, s) // a commit while the read transaction is open
		after, _ := r.AllFiles()
		if len(before) != 1 || len(after) != 1 {
			t.Errorf("snapshot moved inside the transaction: %d then %d files", len(before), len(after))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files, _ := s.AllFiles(); len(files) != 2 {
		t.Fatalf("after the transaction %d files, want 2", len(files))
	}
}

func TestReadTxTakesOneTransactionForManyStatements(t *testing.T) {
	s, c := openCounting(t, Options{})
	for i := 0; i < 5; i++ {
		seed(t, s)
	}
	const rounds = 20
	c.Reset()
	for i := 0; i < rounds; i++ {
		factsOf(t, &s.Reader)
	}
	perStmtTx, perStmtQ := c.Transactions(), c.Queries()
	c.Reset()
	_ = s.ReadTx(func(r *Reader) error {
		for i := 0; i < rounds; i++ {
			factsOf(t, r)
		}
		return nil
	})
	batchedTx, batchedQ := c.Transactions(), c.Queries()
	t.Logf("%d rounds of 4 reads: per statement %d transactions for %d statements; one read transaction %d for %d",
		rounds, perStmtTx, perStmtQ, batchedTx, batchedQ)
	if perStmtQ != batchedQ {
		t.Fatalf("statement counts differ: %d vs %d", perStmtQ, batchedQ)
	}
	if perStmtTx != perStmtQ || batchedTx != 1 || c.Begins() != 1 {
		t.Fatalf("transactions: per statement %d (want %d), batched %d (want 1)", perStmtTx, perStmtQ, batchedTx)
	}
}

func TestReadConnectionPragmasAreConfigurable(t *testing.T) {
	pragma := func(s *Store, name string) int64 {
		var v int64
		if err := s.Read().QueryRow("PRAGMA " + name).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	s, _ := openCounting(t, Options{})
	if got := pragma(s, "cache_size"); got != -DefaultReadCacheKiB {
		t.Errorf("default cache_size %d, want %d", got, -DefaultReadCacheKiB)
	}
	if got := pragma(s, "mmap_size"); got != DefaultReadMmapBytes {
		t.Errorf("default mmap_size %d, want %d", got, DefaultReadMmapBytes)
	}
	s2, _ := openCounting(t, Options{ReadCacheKiB: 1024, ReadMmapBytes: -1})
	if got := pragma(s2, "cache_size"); got != -1024 {
		t.Errorf("cache_size %d, want -1024", got)
	}
	if got := pragma(s2, "mmap_size"); got != 0 {
		t.Errorf("mmap_size %d, want 0 (disabled)", got)
	}
	if pragma(s2, "query_only") != 1 {
		t.Error("read connection is not query_only")
	}
}

func TestReadConnectionRecyclingKeepsTheCacheLongEnough(t *testing.T) {
	if readConnMaxLifetime < time.Hour || readConnMaxIdleTime < 10*time.Minute || readMaxIdleConns < 2 {
		t.Fatalf("read pool recycles too eagerly: lifetime %v idle %v maxIdle %d", readConnMaxLifetime, readConnMaxIdleTime, readMaxIdleConns)
	}
}

func TestPoolStatsReportBothPools(t *testing.T) {
	s, _ := openCounting(t, Options{})
	seed(t, s)
	_, _ = s.AllFiles()
	ps := s.PoolStats()
	if ps.Read.OpenConnections < 1 || ps.Write.OpenConnections < 1 {
		t.Fatalf("pool stats %+v", ps)
	}
}

func TestBulkReadsMatchTheirPointReads(t *testing.T) {
	s, _ := openCounting(t, Options{})
	for i := 0; i < 4; i++ {
		seed(t, s)
	}
	gens, err := s.AllGenerations()
	if err != nil || len(gens) == 0 {
		t.Fatalf("fixture has %d generations, err %v", len(gens), err)
	}
	offs, err := s.CopyOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if len(offs) != len(gens) {
		t.Fatalf("CopyOffsets has %d entries, generations %d", len(offs), len(gens))
	}
	for _, g := range gens {
		if got := offs[[2]int64{g.File, int64(g.Gen)}]; got != g.CopyOffset {
			t.Errorf("file %d gen %d: bulk %d, row %d", g.File, g.Gen, got, g.CopyOffset)
		}
	}
	all, err := s.AllSettled()
	if err != nil {
		t.Fatal(err)
	}
	for session, got := range all {
		want, ok, err := s.SettledOf(session)
		if err != nil || !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: bulk %+v, point %+v (ok %v, err %v)", session, got, want, ok, err)
		}
	}
}

func TestOffsetAggregatesMatchTheGenerationRows(t *testing.T) {
	s, _ := openCounting(t, Options{})
	var ids []int64
	for i := 0; i < 4; i++ {
		ids = append(ids, seed(t, s))
	}
	if err := s.SetParseOffset(ids[0], 1, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParseOffset(ids[1], 1, 10); err != nil {
		t.Fatal(err)
	}
	gens, err := s.AllGenerations()
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]OffsetTotals{}
	var wantLag []Generation
	for _, g := range gens {
		t := want[g.File]
		t.Copy += g.CopyOffset
		t.Parse += g.ParseOffset
		want[g.File] = t
		if g.CopyOffset > g.ParseOffset {
			wantLag = append(wantLag, g)
		}
	}
	got, err := s.OffsetTotalsByFile()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("totals %+v, want %+v (err %v)", got, want, err)
	}
	lag, err := s.LaggingGenerations()
	if err != nil || len(lag) != 3 || !reflect.DeepEqual(lag, wantLag) {
		t.Fatalf("lagging %+v, want %+v (err %v)", lag, wantLag, err)
	}
}
