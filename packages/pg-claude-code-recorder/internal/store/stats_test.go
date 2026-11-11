package store

import (
	"context"
	"testing"
	"time"
)

func statRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Read().QueryRow(`SELECT count(*) FROM sqlite_stat1`).Scan(&n); err != nil {
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
	if !s.CollectStatsDue(now, StatsEvery) {
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
	if err := s.Read().QueryRow(`SELECT count(*) FROM sqlite_stat1 WHERE tbl='records' AND idx='records_time'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("no records_time row: n=%d err=%v", idx, err)
	}
	if s.CollectStatsDue(now.Add(time.Hour), StatsEvery) {
		t.Fatal("due again an hour after a collection")
	}
	if !s.CollectStatsDue(now.Add(StatsEvery+time.Second), StatsEvery) {
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
	if s.CollectStatsDue(time.Now(), StatsEvery) {
		t.Fatal("Close did not stamp the collection")
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
		if _, err := s.wdb.Exec(`INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns) VALUES(1,1,?,0,10,'s','t',1,'example-session',?)`, i, i); err != nil {
			t.Fatal(err)
		}
	}
}
