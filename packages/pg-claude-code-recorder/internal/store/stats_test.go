package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func statRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Read().QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_stat1`).Scan(&n); err != nil {
		return 0 // the table does not exist yet
	}
	return n
}

// A real store: CollectStats creates sqlite_stat1 with rows for the records
// indexes, stamps the day gate, and the next call finds nothing due.
func TestCollectStatsCreatesStat1AndGates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if statRows(t, s) != 0 {
		t.Fatal("a fresh store already has statistics")
	}
	seedRecords(t, s)
	now := time.Now()
	if !s.CollectStatsDue(t.Context(), now, StatsEvery) {
		t.Fatal("never collected, yet not due")
	}
	n, err := s.CollectStats(context.Background(), 400, now)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || statRows(t, s) == 0 {
		t.Fatalf("statements=%d rows=%d", n, statRows(t, s))
	}
	var idx int
	if err := s.Read().QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_stat1 WHERE tbl='records' AND idx='records_time'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("no records_time row: n=%d err=%v", idx, err)
	}
	if s.CollectStatsDue(t.Context(), now.Add(time.Hour), StatsEvery) {
		t.Fatal("due again an hour after a collection")
	}
	if !s.CollectStatsDue(t.Context(), now.Add(StatsEvery+time.Second), StatsEvery) {
		t.Fatal("not due after the interval")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A store opened and closed by the daemon path (no explicit call) ends with
// sqlite_stat1 present: Close collects when due.
func TestCloseCollectsStats(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if statRows(t, s) == 0 {
		t.Fatal("sqlite_stat1 is missing or empty after Close")
	}
	if s.CollectStatsDue(t.Context(), time.Now(), StatsEvery) {
		t.Fatal("Close did not stamp the collection")
	}
}

// NoCloseStats: a store that would otherwise collect on Close (the same flow
// as TestCloseCollectsStats) leaves no statistics and no stamp behind.
func TestNoCloseStatsSkipsTheStatisticsPass(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{NoCloseStats: true})
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n := statRows(t, s); n != 0 {
		t.Fatalf("sqlite_stat1 has %d rows after a NoCloseStats Close", n)
	}
	if !s.CollectStatsDue(t.Context(), time.Now(), StatsEvery) {
		t.Fatal("a NoCloseStats Close stamped the collection")
	}
}

func TestCollectStatsAfterClose(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := s.CollectStats(context.Background(), 0, time.Now()); err == nil {
		t.Fatal("want an error after Close")
	}
}

func seedRecords(t *testing.T, s *Store) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if _, err := s.wdb.ExecContext(context.Background(), `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns) VALUES(1,1,?,0,10,'s','t',1,'example-session',?)`, i, i); err != nil {
			t.Fatal(err)
		}
	}
}

// A step that cannot finish inside the step budget is interrupted (releasing the
// write lock), retried smaller, and finally skipped: the pass reports
// ErrStatsSkipped and does not stamp the day gate, so the next check retries.
func TestCollectStatsSkipsSlowStepWithoutStamping(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seedRecords(t, s)
	old := statsStepTimeout
	statsStepTimeout = time.Nanosecond
	defer func() { statsStepTimeout = old }()
	now := time.Now()
	_, err = s.CollectStats(context.Background(), 400, now)
	if !errors.Is(err, ErrStatsSkipped) {
		t.Fatalf("err=%v, want ErrStatsSkipped", err)
	}
	if !s.CollectStatsDue(t.Context(), now, StatsEvery) {
		t.Fatal("a pass that skipped steps stamped the gate")
	}
	statsStepTimeout = old
	if _, err := s.CollectStats(context.Background(), 400, now); err != nil {
		t.Fatalf("retry with a normal budget: %v", err)
	}
	if s.CollectStatsDue(t.Context(), now, StatsEvery) {
		t.Fatal("completed retry did not stamp")
	}
}

// A collection samples each index instead of scanning it whole: with
// analysis_limit=MinAnalysisLimit, the planner statistics of the records_type
// index on 2,000 rows record MinAnalysisLimit+1 rows per key (the sampled prefix
// of the index), where a full ANALYZE records 2,000. This is the guard for the
// sampled-PRAGMA half of analyzeStep (the per-step timeout half is guarded by
// TestCollectStatsSkipsSlowStepWithoutStamping); it is deterministic and does not
// depend on how fast the host is.
func TestCollectStatsSamplesIndexesInsteadOfScanningWhole(t *testing.T) {
	const rows = 2000
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.wdb.ExecContext(context.Background(), `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns)
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		SELECT 1,1,i,0,10,'s','t',1,'sess'||(i%50),i FROM n`, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectStats(context.Background(), MinAnalysisLimit, time.Now()); err != nil {
		t.Fatal(err)
	}
	var stat string
	if err := s.Read().QueryRowContext(context.Background(), `SELECT stat FROM sqlite_stat1 WHERE tbl='records' AND idx='records_type'`).Scan(&stat); err != nil {
		t.Fatal(err)
	}
	// A full scan of this index gives "2000 2000"; a sampled one gives "2000 51".
	if want := "2000 51"; stat != want {
		t.Fatalf("records_type stat=%q, want %q (a sampled analysis)", stat, want)
	}
}

// A first collection on a store without sqlite_stat1, with the writer running
// concurrently: no write fails and no single write waits near the busy_timeout.
// This is a smoke test of the plumbing only. On a fast host a full ANALYZE of
// 1,000 rows (and of 30,000 or 1,000,000, measured) finishes inside the limit,
// so it does NOT fail when the sampling or the per-step timeout in analyzeStep is
// removed; those are guarded by TestCollectStatsSamplesIndexesInsteadOfScanningWhole
// and TestCollectStatsSkipsSlowStepWithoutStamping. The scaled version is
// TestSoakFirstCollectionDoesNotBlockWriterNearBusyTimeout behind the soak tag.
func TestFirstCollectionDoesNotBlockWriterNearBusyTimeout(t *testing.T) {
	firstCollectionWriterWait(t, 1000)
}

func firstCollectionWriterWait(t *testing.T, rows int) {
	t.Helper()
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.wdb.ExecContext(context.Background(), `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns)
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		SELECT 1,1,i,0,10,'s','t',1,'sess'||(i%50),i FROM n`, rows); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var worst time.Duration
	var failures int
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			t0 := time.Now()
			_, err := s.wdb.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES('probe',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, i)
			if err != nil {
				failures++
			}
			if d := time.Since(t0); d > worst {
				worst = d
			}
			time.Sleep(time.Millisecond)
		}
	}()
	_, cerr := s.CollectStats(context.Background(), 0, time.Now())
	close(stop)
	<-done
	if cerr != nil {
		t.Fatal(cerr)
	}
	if failures != 0 {
		t.Fatalf("%d writes failed during the collection", failures)
	}
	if worst > statsStepTimeout+time.Second {
		t.Fatalf("longest write wait %v, want well under the busy_timeout", worst)
	}
}
