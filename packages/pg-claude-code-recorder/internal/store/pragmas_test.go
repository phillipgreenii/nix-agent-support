package store

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite/sqliteopen"
)

// The recorder's three handles carry the shared sqliteopen helper's standard
// pragma set on a real SQLite file, with this store's own tuned numbers kept:
// temp_store DEFAULT, foreign keys off, the 1 GiB journal_size_limit on the writer and checkpoint connections and the
// 512 MiB memory map on the read pool.
func TestOpenEffectiveStandardPragmas(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	writer := sqliteopen.Pragmas{
		JournalMode:      "wal",
		Synchronous:      1, // NORMAL
		BusyTimeout:      busyTimeoutMS,
		CacheSize:        -sqliteopen.DefaultCacheKiB,
		MmapSize:         sqliteopen.DefaultMmapBytes,
		JournalSizeLimit: JournalSizeLimit,
		TempStore:        0,     // DEFAULT: index builds spill to disk (the helper's MEMORY is overridden)
		ForeignKeys:      false, // as before; the helper's ON is overridden
	}
	for _, c := range []struct {
		name string
		get  func() (sqliteopen.Pragmas, error)
		want sqliteopen.Pragmas
	}{
		{"writer", func() (sqliteopen.Pragmas, error) { return sqliteopen.Effective(ctx, s.wdb) }, writer},
		{"checkpoint", func() (sqliteopen.Pragmas, error) { return sqliteopen.Effective(ctx, s.cdb) }, writer},
	} {
		got, err := c.get()
		if err != nil {
			t.Fatalf("%s: Effective: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s pragmas = %+v, want %+v", c.name, got, c.want)
		}
	}

	r, err := sqliteopen.Effective(ctx, s.rdb)
	if err != nil {
		t.Fatalf("reader: Effective: %v", err)
	}
	if !r.QueryOnly || r.BusyTimeout != busyTimeoutMS || r.CacheSize != -DefaultReadCacheKiB ||
		r.MmapSize != DefaultReadMmapBytes || r.TempStore != 0 {
		t.Errorf("reader pragmas = %+v", r)
	}
}

// A pragma named by an override replaces the standard value in the DSN: the
// driver runs a DSN's pragmas in sorted order, so a duplicate would not reliably
// win.
func TestWithPragmasReplacesRatherThanDuplicates(t *testing.T) {
	dsn := "file:/x/index.db?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=temp_store(MEMORY)"
	got := withPragmas(dsn, "&_pragma=synchronous(OFF)&_pragma=cache_size(-9)")
	want := "file:/x/index.db?_pragma=journal_mode(WAL)&_pragma=temp_store(MEMORY)&_pragma=synchronous(OFF)&_pragma=cache_size(-9)"
	if got != want {
		t.Errorf("withPragmas = %q, want %q", got, want)
	}
	if got := dropPragmas(dsn, "journal_mode"); got != "file:/x/index.db?_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=temp_store(MEMORY)" {
		t.Errorf("dropPragmas(first) = %q", got)
	}
	if got := withPragmas(dsn, ""); got != dsn {
		t.Errorf("withPragmas(empty) = %q", got)
	}
}
