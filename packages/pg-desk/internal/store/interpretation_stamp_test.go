package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These tests cover the interpretation stamp: INV-REINTERPRET-4 (a row carries
// the stamp of the handle that wrote it, and a row without one is behind) and
// INV-REINTERPRET-13 (a reader that does not migrate works on a store without
// the column), against a real store.

func hasInterpColumn(t *testing.T, s *Store) bool {
	t.Helper()
	cols, err := s.tableColumns("interpretation")
	if err != nil {
		t.Fatal(err)
	}
	return cols[interpStampColumn]
}

func stampRow(id string) Interpretation {
	return Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: id, Ownership: "mine", Enrichment: "{}",
		Urgency: "{}", Category: "c", Dispositions: "{}", Approvals: "{}", GateState: "g",
		MatchReasons: "[]", Panel: "p", AsOf: "2026-09-16T00:00:00Z",
	}
}

func execStamp(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.sql.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// dropStampColumn rebuilds the store as a pre-stamp store would be: the column
// is gone, as on a store no Open of the new binary has reached yet.
func dropStampColumn(t *testing.T, s *Store) {
	t.Helper()
	execStamp(t, s, `ALTER TABLE interpretation DROP COLUMN interp_version`)
	if hasInterpColumn(t, s) {
		t.Fatal("column still present after drop")
	}
}

func TestOpen_EnsuresInterpretationStamp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cutover bool
		version int
	}{{"version 1", false, 1}, {"version 2", true, 2}} {
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
			if !hasInterpColumn(t, s) {
				t.Fatal("Open did not add interpretation.interp_version")
			}
			if got := userVersion(t, s); got != tc.version {
				t.Fatalf("user_version = %d, want %d: the ensure step must not touch it", got, tc.version)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// A second Open is a no-op and the column survives.
			s2, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s2.Close() }()
			if !hasInterpColumn(t, s2) || userVersion(t, s2) != tc.version {
				t.Fatal("second Open changed the store")
			}
		})
	}
}

// A store that had the column dropped (as an old store lacks it) gets it back
// from Open and keeps its rows, which read as unstamped.
func TestOpen_AddsStampColumnToExistingStoreKeepingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertInterpretation(stampRow("o/r#1")); err != nil {
		t.Fatal(err)
	}
	dropStampColumn(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	got, found, err := s2.GetInterpretation("o/r", "pr", "o/r#1")
	if err != nil || !found {
		t.Fatalf("row lost: found=%v err=%v", found, err)
	}
	if got.InterpVersion != "" {
		t.Fatalf("InterpVersion = %q, want empty (NULL)", got.InterpVersion)
	}
}

func TestEnsureInterpretationStamp_Branches(t *testing.T) {
	t.Run("no interpretation table is left alone", func(t *testing.T) {
		s, err := OpenRaw(filepath.Join(t.TempDir(), "raw.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if err := ensureInterpretationStamp(s); err != nil {
			t.Fatal(err)
		}
		if hasInterpColumn(t, s) {
			t.Fatal("a column appeared on a store with no table")
		}
	})
	t.Run("column read failure is returned", func(t *testing.T) {
		s := OpenForTest(t)
		boom := errors.New("boom")
		s.faultTableColumns = func(string) error { return boom }
		if err := ensureInterpretationStamp(s); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
	t.Run("a failed ALTER with the column still absent is an error", func(t *testing.T) {
		s := OpenForTest(t)
		dropStampColumn(t, s)
		ro, err := OpenReadOnly(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ro.Close() }()
		err = ensureInterpretationStamp(ro)
		if err == nil || !strings.Contains(err.Error(), "add interpretation.interp_version") {
			t.Fatalf("err = %v, want the add-column error", err)
		}
	})
	t.Run("a concurrent opener that added the column first is success", func(t *testing.T) {
		s := OpenForTest(t)
		dropStampColumn(t, s)
		ro, err := OpenReadOnly(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ro.Close() }()
		calls := 0
		ro.faultTableColumns = func(string) error {
			calls++
			if calls == 2 { // the ALTER failed (read-only); a peer has added the column since
				execStamp(t, s, `ALTER TABLE interpretation ADD COLUMN interp_version TEXT`)
			}
			return nil
		}
		if err := ensureInterpretationStamp(ro); err != nil {
			t.Fatalf("err = %v, want success once the column is present", err)
		}
		if calls != 2 {
			t.Fatalf("tableColumns called %d times, want 2 (read, then re-read after the failed ALTER)", calls)
		}
	})
}

func TestInterpStamp_SetGetClear(t *testing.T) {
	s := OpenForTest(t)
	if got := s.InterpStamp(); got != "" {
		t.Fatalf("fresh handle stamp = %q, want empty", got)
	}
	s.SetInterpStamp("v1/abcd1234")
	if got := s.InterpStamp(); got != "v1/abcd1234" {
		t.Fatalf("stamp = %q", got)
	}
	s.SetInterpStamp("")
	if got := s.InterpStamp(); got != "" {
		t.Fatalf("cleared stamp = %q, want empty", got)
	}
}

// INV-REINTERPRET-4: the existing upsert writes the handle's stamp, on both
// schema versions; a handle with no stamp writes NULL.
func TestUpsertInterpretation_WritesHandleStamp(t *testing.T) {
	for name, open := range map[string]func(testing.TB) *Store{
		"version 1": func(tb testing.TB) *Store { return OpenForTest(tb.(*testing.T)) },
		"version 2": func(tb testing.TB) *Store { return OpenNewSchemaForTest(tb.(*testing.T)) },
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			read := func(id string) string {
				t.Helper()
				got, found, err := s.GetInterpretation("o/r", "pr", id)
				if err != nil || !found {
					t.Fatalf("found=%v err=%v", found, err)
				}
				return got.InterpVersion
			}
			if err := s.UpsertInterpretation(stampRow("o/r#1")); err != nil {
				t.Fatal(err)
			}
			if got := read("o/r#1"); got != "" {
				t.Fatalf("no stamp on the handle: InterpVersion = %q, want NULL", got)
			}
			s.SetInterpStamp("v1/aaaaaaaa")
			if err := s.UpsertInterpretation(stampRow("o/r#1")); err != nil {
				t.Fatal(err)
			}
			if got := read("o/r#1"); got != "v1/aaaaaaaa" {
				t.Fatalf("InterpVersion = %q, want the handle stamp", got)
			}
			s.SetInterpStamp("v2/bbbbbbbb")
			if err := s.UpsertInterpretation(stampRow("o/r#1")); err != nil {
				t.Fatal(err)
			}
			if got := read("o/r#1"); got != "v2/bbbbbbbb" {
				t.Fatalf("InterpVersion after restamp = %q", got)
			}
			// List sees the same stamp.
			all, err := s.ListInterpretations()
			if err != nil || len(all) != 1 || all[0].InterpVersion != "v2/bbbbbbbb" {
				t.Fatalf("ListInterpretations = %+v err=%v", all, err)
			}
			// A handle with no stamp rewrites the row with NULL.
			s.SetInterpStamp("")
			if err := s.UpsertInterpretation(stampRow("o/r#1")); err != nil {
				t.Fatal(err)
			}
			if got := read("o/r#1"); got != "" {
				t.Fatalf("unstamped rewrite: InterpVersion = %q, want NULL", got)
			}
		})
	}
}

func TestUpsertInterpretation_ShapeErrorIsReturned(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	boom := errors.New("boom")
	s.faultTableColumns = func(string) error { return boom }
	if err := s.UpsertInterpretation(stampRow("o/r#1")); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if _, _, err := s.GetInterpretation("o/r", "pr", "o/r#1"); !errors.Is(err, boom) {
		t.Fatalf("Get err = %v, want boom", err)
	}
	if _, err := s.ListInterpretations(); !errors.Is(err, boom) {
		t.Fatalf("List err = %v, want boom", err)
	}
	s.SetInterpStamp("x")
	if _, _, err := s.InterpretationsBehind("o/r", 0); !errors.Is(err, boom) {
		t.Fatalf("Behind err = %v, want boom", err)
	}
}

func TestInterpShape_SchemaVersionError(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.interpShape(); err == nil {
		t.Fatal("interpShape on a closed handle returned no error")
	}
}

// INV-REINTERPRET-13: readers that never ran Open work on a store without the
// column and read the stamp as absent; the behind query reports unknown.
func TestReadersWithoutStampColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	s.SetInterpStamp("v1/aaaaaaaa")
	if err := s.UpsertInterpretation(stampRow("o/r#1")); err != nil {
		t.Fatal(err)
	}
	dropStampColumn(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	opens := map[string]func(string) (*Store, error){"OpenReadOnly": OpenReadOnly, "OpenRaw": OpenRaw}
	for name, open := range opens {
		t.Run(name, func(t *testing.T) {
			r, err := open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			got, found, err := r.GetInterpretation("o/r", "pr", "o/r#1")
			if err != nil || !found || got.InterpVersion != "" || got.Ownership != "mine" {
				t.Fatalf("Get = %+v found=%v err=%v", got, found, err)
			}
			all, err := r.ListInterpretations()
			if err != nil || len(all) != 1 || all[0].InterpVersion != "" {
				t.Fatalf("List = %+v err=%v", all, err)
			}
			r.SetInterpStamp("v1/aaaaaaaa")
			keys, known, err := r.InterpretationsBehind("o/r", 0)
			if err != nil || known || keys != nil {
				t.Fatalf("Behind = %v known=%v err=%v, want unknown", keys, known, err)
			}
		})
	}

	// A raw (writable) handle that never ran the ensure step still writes the
	// row: the stamp is simply not written.
	raw, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	raw.SetInterpStamp("v1/aaaaaaaa")
	row := stampRow("o/r#1")
	row.Ownership = "theirs"
	if err := raw.UpsertInterpretation(row); err != nil {
		t.Fatalf("upsert through a raw handle without the column: %v", err)
	}
	got, _, _ := raw.GetInterpretation("o/r", "pr", "o/r#1")
	if got.Ownership != "theirs" || hasInterpColumn(t, raw) {
		t.Fatalf("got %+v; the upsert must write content and not add the column", got)
	}
}

func TestInterpretationsBehind_NeedsAStampAndSchema2(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, _, err := s.InterpretationsBehind("o/r", 0); !errors.Is(err, ErrNoInterpStamp) {
		t.Fatalf("err = %v, want ErrNoInterpStamp", err)
	}
	v1 := OpenSchema1ForTest(t)
	v1.SetInterpStamp("x")
	keys, known, err := v1.InterpretationsBehind("o/r", 0)
	if err != nil || known || keys != nil {
		t.Fatalf("schema 1: keys=%v known=%v err=%v, want unknown", keys, known, err)
	}
	closed := OpenNewSchemaForTest(t)
	closed.SetInterpStamp("x")
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := closed.InterpretationsBehind("o/r", 0); err == nil {
		t.Fatal("closed handle returned no error")
	}
}

// seedBehind seeds PR entities with the activity and hydration order the
// query sorts by, and rows under the stamps the query filters on.
func seedBehind(t *testing.T, s *Store, cur string) {
	t.Helper()
	type seed struct {
		repo, typ, id, stamp string // stamp "NULL" stores NULL
		active               int
		hydratedAt           string
		noEntity             bool
	}
	for _, sd := range []seed{
		{"o/r", "pr", "o/r#1", "old", 1, "2026-10-01T00:00:00Z", false},                     // behind, active, older
		{"o/r", "pr", "o/r#2", "NULL", 1, "2026-10-05T00:00:00Z", false},                    // behind (NULL), active, newer
		{"o/r", "pr", "o/r#3", cur, 1, "2026-10-06T00:00:00Z", false},                       // current: excluded
		{"o/r", "pr", "o/r#4", cur + FailedStampSuffix, 1, "2026-10-07T00:00:00Z", false},   // failed under current: excluded
		{"o/r", "pr", "o/r#5", "old" + FailedStampSuffix, 1, "2026-10-02T00:00:00Z", false}, // failed under old: behind
		{"o/r", "pr", "o/r#6", "old", 0, "2026-10-09T00:00:00Z", false},                     // behind, inactive: after the active ones
		{"o/r", "pr", "o/r#7", "old", 1, "", true},                                          // entity gone: still listed
		{"o/r", "issue", "i-1", "old", 1, "2026-10-09T00:00:00Z", false},                    // not a pr
		{"x/y", "pr", "x/y#1", "old", 1, "2026-10-09T00:00:00Z", false},                     // other repo
	} {
		if !sd.noEntity {
			execStamp(t, s, `INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, content_hash, active, hydrated_at)
				VALUES (?, ?, ?, '{}', '2026-09-16T00:00:00Z', 'h', ?, NULLIF(?, ''))`, sd.repo, sd.typ, sd.id, sd.active, sd.hydratedAt)
		}
		row := stampRow(sd.id)
		row.Repo, row.EntityType = sd.repo, sd.typ
		if err := s.UpsertInterpretation(row); err != nil {
			t.Fatal(err)
		}
		if sd.stamp == "NULL" {
			execStamp(t, s, `UPDATE interpretation SET interp_version = NULL WHERE entity_id = ?`, sd.id)
		} else {
			execStamp(t, s, `UPDATE interpretation SET interp_version = ? WHERE entity_id = ?`, sd.stamp, sd.id)
		}
	}
}

func ids(keys []InterpretationKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.EntityID
	}
	return out
}

func TestInterpretationsBehind_SelectsOrdersAndBounds(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	const cur = "v9/cccccccc"
	seedBehind(t, s, cur)
	s.SetInterpStamp(cur)

	keys, known, err := s.InterpretationsBehind("o/r", 0)
	if err != nil || !known {
		t.Fatalf("known=%v err=%v", known, err)
	}
	// Active rows first (newest hydration first), then inactive, then the row
	// whose entity is gone. Current (#3) and failed-under-current (#4) are
	// out, as are the issue and the other repo.
	want := []string{"o/r#2", "o/r#5", "o/r#1", "o/r#6", "o/r#7"}
	if got := ids(keys); !reflect.DeepEqual(got, want) {
		t.Fatalf("behind = %v, want %v", got, want)
	}
	for _, k := range keys {
		if k.Repo != "o/r" || k.EntityType != "pr" {
			t.Fatalf("key %+v is not a pr of o/r", k)
		}
	}

	limited, known, err := s.InterpretationsBehind("o/r", 2)
	if err != nil || !known || !reflect.DeepEqual(ids(limited), want[:2]) {
		t.Fatalf("limit 2 = %v known=%v err=%v, want %v", ids(limited), known, err, want[:2])
	}

	// A change of stamp makes the failed-under-current row eligible again and
	// the rows stamped current behind.
	s.SetInterpStamp("v10/dddddddd")
	keys, _, err = s.InterpretationsBehind("o/r", 0)
	if err != nil || len(keys) != 7 {
		t.Fatalf("after a stamp change: %v err=%v, want all 7 prs of o/r", ids(keys), err)
	}

	// Steady state: once every row carries the running stamp nothing is behind.
	execStamp(t, s, `UPDATE interpretation SET interp_version = ?`, "v10/dddddddd")
	keys, known, err = s.InterpretationsBehind("o/r", 0)
	if err != nil || !known || len(keys) != 0 {
		t.Fatalf("steady state: %v known=%v err=%v", ids(keys), known, err)
	}
}

// The behind query must not read entity facts: it still answers when an
// entity's facts are not valid JSON and when they are huge.
func TestInterpretationsBehind_NeverTouchesFacts(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	seedBehind(t, s, "cur")
	execStamp(t, s, `UPDATE entity SET facts = 'not json at all {'`)
	s.SetInterpStamp("cur")
	keys, known, err := s.InterpretationsBehind("o/r", 0)
	if err != nil || !known || len(keys) != 5 {
		t.Fatalf("keys=%v known=%v err=%v", ids(keys), known, err)
	}
}

func TestInterpretationsBehind_QueryFailureIsReturned(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	s.SetInterpStamp("cur")
	execStamp(t, s, `DROP TABLE entity`)
	if _, known, err := s.InterpretationsBehind("o/r", 0); err == nil || known {
		t.Fatalf("known=%v err=%v, want an error", known, err)
	}
}
