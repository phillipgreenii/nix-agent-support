package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const seenPath = "/c/projects/p/s.jsonl"

func seenRow(file int64) SeenRow {
	return SeenRow{Path: seenPath, File: file, Dev: 1, Ino: 2, BirthNS: 3, Size: 10, MTimeNS: 99}
}

func loadSeenT(t *testing.T, s *Store) []SeenRow {
	t.Helper()
	rows, err := s.SeenLoad()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A store written before the seen table existed gets it at the next open, and
// the schema version does not change.
func TestOpenCreatesTheSeenTableOnAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.wdb.ExecContext(context.Background(), `DROP TABLE seen`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer func() { _ = s.Close() }()
	if n, err := s.SeenCount(); err != nil || n != 0 {
		t.Fatalf("seen after open: %d, %v", n, err)
	}
	if s.SchemaVersion() != SchemaVersion || SchemaVersion != 4 {
		t.Fatalf("schema_version %d, constant %d, want 4", s.SchemaVersion(), SchemaVersion)
	}
}

func TestSeenRowsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	if reset, err := s.SeenPrepare(1); err != nil || !reset {
		t.Fatalf("first prepare: reset=%v err=%v, want a reset of the empty table", reset, err)
	}
	if err := s.SeenWrite([]SeenRow{seenRow(id)}, nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = open(t, dir)
	defer func() { _ = s.Close() }()
	if reset, err := s.SeenPrepare(1); err != nil || reset {
		t.Fatalf("second prepare: reset=%v err=%v, want the table kept", reset, err)
	}
	rows := loadSeenT(t, s)
	if len(rows) != 1 || rows[0] != seenRow(id) {
		t.Fatalf("rows after restart: %+v", rows)
	}
}

// Each way the store can move past a row drops it at load.
func TestSeenLoadDropsRowsTheStoreHasMovedPast(t *testing.T) {
	cases := []struct {
		name string
		do   func(id int64) []*Rec
	}{
		{"size changed (last_size differs)", func(id int64) []*Rec {
			return []*Rec{{T: TCommit, At: 20, File: id, Gen: 1, From: 10, To: 25, Size: 25, Seq: 2}}
		}},
		{"path closed (rename)", func(id int64) []*Rec {
			return []*Rec{{T: TPathClose, At: 20, File: id, Path: seenPath}}
		}},
		{"tombstone", func(id int64) []*Rec {
			return []*Rec{{T: TTomb, At: 20, File: id, Path: seenPath}}
		}},
		{"new generation", func(id int64) []*Rec {
			return []*Rec{{T: TGen, At: 20, File: id, Gen: 2, Method: "bytes", Reason: "reset"}}
		}},
		{"inode changed", func(id int64) []*Rec {
			return []*Rec{{T: TIdent, At: 20, File: id, Dev: 1, Ino: 5, Birth: 3}}
		}},
		{"birth changed", func(id int64) []*Rec {
			return []*Rec{{T: TIdent, At: 20, File: id, Dev: 1, Ino: 2, Birth: 4}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := open(t, t.TempDir())
			defer func() { _ = s.Close() }()
			id := seed(t, s)
			if err := s.SeenWrite([]SeenRow{seenRow(id)}, nil); err != nil {
				t.Fatal(err)
			}
			if rows := loadSeenT(t, s); len(rows) != 1 {
				t.Fatalf("row missing before the change: %+v", rows)
			}
			if err := s.Commit(c.do(id)...); err != nil {
				t.Fatal(err)
			}
			if rows := loadSeenT(t, s); len(rows) != 0 {
				t.Fatalf("row survived: %+v", rows)
			}
		})
	}
}

// A row whose recorded size no longer equals the file's last_size (a crash
// between the commit and the seen write is the other direction: no row at all).
func TestSeenLoadDropsAMismatchedSizeRow(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	id := seed(t, s)
	r := seenRow(id)
	r.Size = 9
	if err := s.SeenWrite([]SeenRow{r}, nil); err != nil {
		t.Fatal(err)
	}
	if rows := loadSeenT(t, s); len(rows) != 0 {
		t.Fatalf("mismatched row loaded: %+v", rows)
	}
}

func TestSeenWriteDeletesAndPruneKeepsOnlyWalkedPaths(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	id := seed(t, s)
	other := seenRow(id)
	other.Path = "/c/projects/p/other.jsonl"
	if err := s.SeenWrite([]SeenRow{seenRow(id), other}, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := s.SeenPrune(map[string]bool{seenPath: true}); err != nil || n != 1 {
		t.Fatalf("prune removed %d, %v, want 1", n, err)
	}
	if n, _ := s.SeenCount(); n != 1 {
		t.Fatalf("rows after prune: %d", n)
	}
	if err := s.SeenWrite(nil, []string{seenPath}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.SeenCount(); n != 0 {
		t.Fatalf("rows after delete: %d", n)
	}
}

func TestSeenPrepareTruncatesOnEpochChangeAndRestoredDatabase(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	put := func() {
		t.Helper()
		if err := s.SeenWrite([]SeenRow{seenRow(id)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SeenPrepare(1); err != nil {
		t.Fatal(err)
	}
	put()
	// A build with another epoch (downgrade then upgrade) starts over.
	if reset, err := s.SeenPrepare(2); err != nil || !reset {
		t.Fatalf("epoch change: reset=%v err=%v", reset, err)
	}
	if n, _ := s.SeenCount(); n != 0 {
		t.Fatalf("rows after an epoch change: %d", n)
	}
	// A restored database carries an older token than the sidecar file.
	put()
	if err := os.WriteFile(filepath.Join(dir, seenTokenFile), []byte("another-database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reset, err := s.SeenPrepare(2); err != nil || !reset {
		t.Fatalf("token mismatch: reset=%v err=%v", reset, err)
	}
	if n, _ := s.SeenCount(); n != 0 {
		t.Fatalf("rows after a restored database: %d", n)
	}
	_ = s.Close()
}

// serve --rebuild moves index.db aside and builds a new one, so no row of the
// old file ids can survive.
func TestRebuildStartsWithAnEmptySeenTable(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	if _, err := s.SeenPrepare(1); err != nil {
		t.Fatal(err)
	}
	if err := s.SeenWrite([]SeenRow{seenRow(id)}, nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n, _ := s.SeenCount(); n != 0 {
		t.Fatalf("rows after a rebuild: %d", n)
	}
	if reset, err := s.SeenPrepare(1); err != nil || !reset {
		t.Fatalf("prepare after a rebuild: reset=%v err=%v, want a reset", reset, err)
	}
}
