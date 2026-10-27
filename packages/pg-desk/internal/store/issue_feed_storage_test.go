package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func userVersion(t *testing.T, s *Store) int {
	t.Helper()
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	return v
}

func hasEntityColumn(t *testing.T, s *Store, name string) bool {
	t.Helper()
	cols, err := s.tableColumns("entity")
	if err != nil {
		t.Fatal(err)
	}
	return cols[name]
}

func hasIngestFailedTable(t *testing.T, s *Store) bool {
	t.Helper()
	cols, err := s.tableColumns("ingest_failed")
	if err != nil {
		t.Fatal(err)
	}
	return len(cols) > 0
}

// TestOpen_EnsuresIssueFeedStorage covers a version-1 store and a cut-over
// (version-2) store opened through Open: the columns and the table exist and
// user_version is unchanged by the ensure.
func TestOpen_EnsuresIssueFeedStorage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cutover bool
		version int
	}{
		{"version 1", false, 1},
		{"version 2", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.cutover {
				if err := s.Cutover(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// Reopen through Open: the ensure runs on the existing store.
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			for _, col := range []string{"backend", "watched"} {
				if !hasEntityColumn(t, s, col) {
					t.Errorf("entity.%s missing after Open", col)
				}
			}
			if !hasIngestFailedTable(t, s) {
				t.Error("ingest_failed missing after Open")
			}
			if got := userVersion(t, s); got != tc.version {
				t.Errorf("user_version = %d, want %d", got, tc.version)
			}
		})
	}
}

// TestEnsureIssueFeedStorage_LeavesUserVersionAlone opens a store file that
// has neither the columns nor the table, runs the ensure, and compares
// user_version before and after.
func TestEnsureIssueFeedStorage_LeavesUserVersionAlone(t *testing.T) {
	path, before := legacyCutoverStore(t)
	s, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if hasEntityColumn(t, s, "backend") || hasIngestFailedTable(t, s) {
		t.Fatal("fixture unexpectedly has the additions")
	}
	if err := ensureIssueFeedStorage(s); err != nil {
		t.Fatal(err)
	}
	if !hasEntityColumn(t, s, "backend") || !hasEntityColumn(t, s, "watched") || !hasIngestFailedTable(t, s) {
		t.Fatal("ensure did not add the columns and table")
	}
	if got := userVersion(t, s); got != before {
		t.Errorf("user_version = %d after ensure, want %d", got, before)
	}
}

func TestEnsureIssueFeedStorage_SecondCallIsNoOp(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	b, w := "jira-main", true
	if err := s.SetEntityColumns(clRepo, clType, clID, &b, &w); err != nil {
		t.Fatal(err)
	}
	cols := func() int {
		m, err := s.tableColumns("entity")
		if err != nil {
			t.Fatal(err)
		}
		return len(m)
	}
	before, v := cols(), userVersion(t, s)
	if err := ensureIssueFeedStorage(s); err != nil {
		t.Fatal(err)
	}
	if err := ensureIssueFeedStorage(s); err != nil {
		t.Fatal(err)
	}
	if cols() != before || userVersion(t, s) != v {
		t.Errorf("second ensure changed the schema: columns %d->%d, user_version %d->%d", before, cols(), v, userVersion(t, s))
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.Backend != b || got.Watched == nil || !*got.Watched {
		t.Errorf("ensure lost the column values: (%+v, %v, %v)", got, found, err)
	}
}

// TestOlderBinaryOpenStillWorks simulates a binary built before the
// additions: it opens the store with the pre-existing open path (openDB then
// migrate, no ensure) and via OpenRaw, and reads and writes entities.
func TestOlderBinaryOpenStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	before := userVersion(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	old, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(old); err != nil {
		t.Fatalf("ensure-less migrate: %v", err)
	}
	if err := old.UpsertEntity(clEntity("a")); err != nil {
		t.Fatalf("ensure-less write: %v", err)
	}
	_ = old.Close()

	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if got := userVersion(t, raw); got != before {
		t.Errorf("user_version = %d, want %d", got, before)
	}
	list, err := raw.ListEntities()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListEntities = (%v, %v), want one row", list, err)
	}
}

// legacyCutoverStore builds a cut-over store file that has neither the
// backend and watched columns nor the ingest_failed table (what a store
// written by a binary built before the additions looks like), with one
// entity row, and returns its path and user_version.
func legacyCutoverStore(t *testing.T) (string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE ingest_failed`,
		`ALTER TABLE entity DROP COLUMN backend`,
		`ALTER TABLE entity DROP COLUMN watched`,
	} {
		if _, err := s.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return path, userVersion(t, s)
}

func TestOpenReadOnly_StoreWithoutIssueFeedStorage(t *testing.T) {
	path, _ := legacyCutoverStore(t)
	s, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	list, err := s.ListEntities()
	if err != nil || len(list) != 1 || list[0].Backend != "" || list[0].Watched != nil {
		t.Fatalf("ListEntities = (%+v, %v), want one row with Backend \"\" and Watched nil", list, err)
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.Backend != "" || got.Watched != nil {
		t.Fatalf("GetEntity = (%+v, %v, %v)", got, found, err)
	}
	// The failed-ingest reads tolerate the absent table.
	if n, err := s.CountIngestFailures(""); err != nil || n != 0 {
		t.Errorf("CountIngestFailures = (%d, %v), want (0, nil)", n, err)
	}
	if l, err := s.ListIngestFailures(""); err != nil || len(l) != 0 {
		t.Errorf("ListIngestFailures = (%v, %v), want empty", l, err)
	}
	if _, ok, err := s.OldestIngestFailureAt(); err != nil || ok {
		t.Errorf("OldestIngestFailureAt ok=%v err=%v, want false, nil", ok, err)
	}
	// And the handle really is read-only: nothing was (or can be) added.
	if hasEntityColumn(t, s, "backend") || hasIngestFailedTable(t, s) {
		t.Error("read-only open added storage")
	}
	b := "x"
	if err := s.SetEntityColumns(clRepo, clType, clID, &b, nil); err == nil {
		t.Error("SetEntityColumns succeeded on a read-only handle")
	}
}

func TestSetEntityColumns(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"created"}, "sync", "2026-09-10T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	read := func() Entity {
		t.Helper()
		e, found, err := s.GetEntity(clRepo, clType, clID)
		if err != nil || !found {
			t.Fatalf("GetEntity = (%v, %v)", found, err)
		}
		return e
	}
	base := read()
	if base.Backend != "" || base.Watched != nil {
		t.Fatalf("fresh row Backend=%q Watched=%v, want NULLs", base.Backend, base.Watched)
	}
	logBefore := changeCount(t, s)

	b, w := "jira-main", true
	if err := s.SetEntityColumns(clRepo, clType, clID, &b, &w); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got.Backend != b || got.Watched == nil || !*got.Watched {
		t.Fatalf("after set: Backend=%q Watched=%v", got.Backend, got.Watched)
	}
	if got.Version != base.Version || got.HydratedAt != base.HydratedAt || got.AsOf != base.AsOf ||
		got.Facts != base.Facts || got.ListFP != base.ListFP || got.Inactive != base.Inactive {
		t.Errorf("column-only write touched other columns: before %+v after %+v", base, got)
	}
	if n := changeCount(t, s); n != logBefore {
		t.Errorf("change_log rows %d -> %d, want unchanged", logBefore, n)
	}

	// Only non-nil arguments change.
	f := false
	if err := s.SetEntityColumns(clRepo, clType, clID, nil, &f); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.Backend != b || got.Watched == nil || *got.Watched {
		t.Errorf("watched-only write: Backend=%q Watched=%v, want %q and false", got.Backend, got.Watched, b)
	}
	other := "jira-other"
	if err := s.SetEntityColumns(clRepo, clType, clID, &other, nil); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.Backend != other || got.Watched == nil || *got.Watched {
		t.Errorf("backend-only write: Backend=%q Watched=%v", got.Backend, got.Watched)
	}

	// A pointer to "" writes NULL; both nil is a no-op.
	empty := ""
	if err := s.SetEntityColumns(clRepo, clType, clID, &empty, nil); err != nil {
		t.Fatal(err)
	}
	if got = read(); got.Backend != "" || got.Watched == nil {
		t.Errorf("empty-backend write: Backend=%q Watched=%v, want \"\" and unchanged", got.Backend, got.Watched)
	}
	if err := s.SetEntityColumns(clRepo, clType, clID, nil, nil); err != nil {
		t.Errorf("both nil: %v, want nil", err)
	}
	var isNull bool
	if err := s.sql.QueryRow(`SELECT backend IS NULL FROM entity WHERE entity_id = ?`, clID).Scan(&isNull); err != nil || !isNull {
		t.Errorf("backend not NULL after empty write (isNull=%v, err=%v)", isNull, err)
	}
	if v := read().Version; v != base.Version {
		t.Errorf("version %d -> %d, want unchanged", base.Version, v)
	}
}

func TestSetEntityColumns_AbsentRow(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	b := "jira-main"
	if err := s.SetEntityColumns(clRepo, clType, "nope", &b, nil); !errors.Is(err, ErrNoEntity) {
		t.Fatalf("err = %v, want ErrNoEntity", err)
	}
}

// TestEntityReads_IssueFeedColumnsOnVersion1 checks that a version-1 store
// (which the ensure also covers) reads the new columns too.
func TestEntityReads_IssueFeedColumnsOnVersion1(t *testing.T) {
	s := OpenForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	b, w := "beads", true
	if err := s.SetEntityColumns(clRepo, clType, clID, &b, &w); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListEntities()
	if err != nil || len(list) != 1 || list[0].Backend != b || list[0].Watched == nil || !*list[0].Watched {
		t.Fatalf("ListEntities = (%+v, %v)", list, err)
	}
}
