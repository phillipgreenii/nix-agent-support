package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

const (
	clRepo = "acme/widgets"
	clType = "pr"
	clID   = "acme/widgets#7"
)

func clEntity(facts string) Entity {
	return Entity{Repo: clRepo, EntityType: clType, EntityID: clID, Facts: facts, AsOf: "2026-09-10T00:00:00Z", ContentHash: "h-" + facts}
}

func changeCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.sql.QueryRow(`SELECT COUNT(*) FROM change_log`).Scan(&n); err != nil {
		t.Fatalf("count change_log: %v", err)
	}
	return n
}

func TestWriteEntityWithLog_InsertThenUpdateBumpsVersion(t *testing.T) {
	s := OpenNewSchemaForTest(t)

	v, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"created"}, "sync", "2026-09-10T00:00:00Z")
	if err != nil || v != 1 {
		t.Fatalf("first write = (%d, %v), want (1, nil)", v, err)
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.Version != 1 || got.Facts != "a" {
		t.Fatalf("GetEntity = (%+v, %v, %v)", got, found, err)
	}

	v, err = s.WriteEntityWithLog(clEntity("b"), 1, []string{"facts_changed", "state_changed"}, "local", "2026-09-11T00:00:00Z")
	if err != nil || v != 2 {
		t.Fatalf("second write = (%d, %v), want (2, nil)", v, err)
	}

	hist, err := s.ListEntityHistory(clRepo, clType, clID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Version != 2 || hist[1].Version != 1 {
		t.Fatalf("history newest-first = %+v", hist)
	}
	if !reflect.DeepEqual(hist[0].Kinds, []string{"facts_changed", "state_changed"}) || hist[0].Origin != "local" || hist[0].At != "2026-09-11T00:00:00Z" {
		t.Fatalf("history[0] = %+v", hist[0])
	}
}

func TestWriteEntityWithLog_StaleVersionConflicts(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteEntityWithLog(clEntity("b"), 1, []string{"x"}, "sync", "t2"); err != nil {
		t.Fatal(err)
	}
	// A writer that read version 1 must not overwrite snapshot 2.
	_, err := s.WriteEntityWithLog(clEntity("stale"), 1, []string{"x"}, "sync", "t3")
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale write err = %v, want ErrVersionConflict", err)
	}
	// Absent entity with nonzero expected version conflicts too.
	e := clEntity("z")
	e.EntityID = "acme/widgets#absent"
	if _, err := s.WriteEntityWithLog(e, 3, []string{"x"}, "sync", "t4"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("absent+nonzero err = %v, want ErrVersionConflict", err)
	}
	if got := s.ConflictCount(); got != 2 {
		t.Fatalf("ConflictCount = %d, want 2", got)
	}
	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Facts != "b" || got.Version != 2 {
		t.Fatalf("entity after conflicts = %+v", got)
	}
	if n := changeCount(t, s); n != 2 {
		t.Fatalf("change_log rows = %d, want 2", n)
	}
}

func TestWriteEntityWithLog_AdoptsCutoverRowAtVersionZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre.db")
	buildSyntheticPreMigrationDB(t, path)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	e, found, err := s.GetEntity("acme/widgets", "pr", "acme/widgets#1")
	if err != nil || !found || e.Version != 0 {
		t.Fatalf("migrated entity = (%+v, %v, %v)", e, found, err)
	}
	e.Facts = `{"title":"changed"}`
	v, err := s.WriteEntityWithLog(e, 0, []string{"facts_changed"}, "sync", "t")
	if err != nil || v != 1 {
		t.Fatalf("write over migrated row = (%d, %v), want (1, nil)", v, err)
	}
}

func TestWriteEntityWithLog_RefusesOldSchema(t *testing.T) {
	s := OpenForTest(t)
	_, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"x"}, "sync", "t")
	if !errors.Is(err, ErrOldSchema) {
		t.Fatalf("err = %v, want ErrOldSchema", err)
	}
}

func TestGetEntity_VersionZeroOnOldSchema(t *testing.T) {
	s := OpenForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.Version != 0 {
		t.Fatalf("old-schema read = (%+v, %v, %v)", got, found, err)
	}
	list, err := s.ListEntities()
	if err != nil || len(list) != 1 || list[0].Version != 0 {
		t.Fatalf("old-schema list = (%+v, %v)", list, err)
	}
}

// TestLogAppendAndVersionBumpAreAtomic fails the write between the version
// bump and the log append, and asserts neither persisted.
func TestLogAppendAndVersionBumpAreAtomic(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("injected failure")
	s.betweenBumpAndAppend = func() error { return boom }
	_, err := s.WriteEntityWithLog(clEntity("b"), 1, []string{"x"}, "sync", "t2")
	s.betweenBumpAndAppend = nil
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if errors.Is(err, ErrVersionConflict) {
		t.Fatalf("an injected failure must not be reported as a conflict")
	}

	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Version != 1 || got.Facts != "a" {
		t.Fatalf("entity after failed write = %+v, want version 1 facts a", got)
	}
	if n := changeCount(t, s); n != 1 {
		t.Fatalf("change_log rows = %d, want 1 (the failed write's row must not persist)", n)
	}
	if got := s.ConflictCount(); got != 0 {
		t.Fatalf("ConflictCount = %d, want 0", got)
	}

	// The failed attempt left the store usable: the same write now succeeds.
	if v, err := s.WriteEntityWithLog(clEntity("b"), 1, []string{"x"}, "sync", "t2"); err != nil || v != 2 {
		t.Fatalf("retry = (%d, %v)", v, err)
	}
}

func openSecondHandle(t *testing.T, s *Store) *Store {
	t.Helper()
	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("second handle: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

// TestRefreshRacingHydrationNeverRegressesVersion races writers on separate
// connections to a real SQLite file through the optimistic-version path.
func TestRefreshRacingHydrationNeverRegressesVersion(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityWithLog(clEntity("seed"), 0, []string{"created"}, "sync", "t0"); err != nil {
		t.Fatal(err)
	}
	handles := []*Store{s, openSecondHandle(t, s), openSecondHandle(t, s)}

	const rounds = 30
	var wg sync.WaitGroup
	wins := make([]int, len(handles))
	for i, h := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				cur, found, err := h.GetEntity(clRepo, clType, clID)
				if err != nil || !found {
					t.Errorf("read: found=%v err=%v", found, err)
					return
				}
				v, err := h.WriteEntityWithLog(clEntity(fmt.Sprintf("w%d-%d", i, r)), cur.Version, []string{"facts_changed"}, fmt.Sprintf("w%d", i), "t")
				switch {
				case err == nil:
					if v != cur.Version+1 {
						t.Errorf("new version %d, want %d", v, cur.Version+1)
					}
					wins[i]++
				case errors.Is(err, ErrVersionConflict):
				default:
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	total := 0
	for _, w := range wins {
		total += w
	}
	final, _, _ := s.GetEntity(clRepo, clType, clID)
	if final.Version != int64(1+total) {
		t.Fatalf("final version %d, want %d (1 + %d wins)", final.Version, 1+total, total)
	}
	// The log names exactly the snapshots that exist: versions 1..final, once each, in order.
	recs, err := s.ListChangesAfter(clType, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != total+1 {
		t.Fatalf("log rows = %d, want %d", len(recs), total+1)
	}
	for i, r := range recs {
		if r.Version != int64(i+1) {
			t.Fatalf("log[%d].Version = %d, want %d (versions must never regress)", i, r.Version, i+1)
		}
	}
	var conflicts int64
	for _, h := range handles {
		conflicts += h.ConflictCount()
	}
	if conflicts != int64(len(handles)*rounds-total) {
		t.Fatalf("conflicts %d, want %d", conflicts, len(handles)*rounds-total)
	}
}

func TestConcurrentFirstWritersExactlyOneWins(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	handles := []*Store{s, openSecondHandle(t, s), openSecondHandle(t, s), openSecondHandle(t, s)}

	start := make(chan struct{})
	errs := make([]error, len(handles))
	var wg sync.WaitGroup
	for i, h := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = h.WriteEntityWithLog(clEntity(fmt.Sprintf("first%d", i)), 0, []string{"created"}, "sync", "t")
		}()
	}
	close(start)
	wg.Wait()

	wonCount, conflictCount := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wonCount++
		case errors.Is(err, ErrVersionConflict):
			conflictCount++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wonCount != 1 || conflictCount != len(handles)-1 {
		t.Fatalf("winners=%d conflicts=%d, want 1 and %d", wonCount, conflictCount, len(handles)-1)
	}
	var counted int64
	for _, h := range handles {
		counted += h.ConflictCount()
	}
	if counted != int64(len(handles)-1) {
		t.Fatalf("ConflictCount total = %d, want %d", counted, len(handles)-1)
	}
	if n := changeCount(t, s); n != 1 {
		t.Fatalf("change_log rows = %d, want 1", n)
	}
}

func TestListChangesAfter_AscendingLimitedAndTypeScoped(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	for i := 0; i < 3; i++ {
		e := clEntity(fmt.Sprintf("v%d", i))
		if _, err := s.WriteEntityWithLog(e, int64(i), []string{"k"}, "sync", "t"); err != nil {
			t.Fatal(err)
		}
	}
	other := Entity{Repo: clRepo, EntityType: "issue", EntityID: "acme/widgets#i1", Facts: "{}", AsOf: "t"}
	if _, err := s.WriteEntityWithLog(other, 0, []string{"created"}, "sync", "t"); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListChangesAfter(clType, 0, 100)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = (%+v, %v)", all, err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("not ascending: %+v", all)
		}
	}
	after, err := s.ListChangesAfter(clType, all[0].Seq, 1)
	if err != nil || len(after) != 1 || after[0].Seq != all[1].Seq {
		t.Fatalf("after/limit = (%+v, %v)", after, err)
	}
	none, err := s.ListChangesAfter(clType, all[2].Seq, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("past end = (%+v, %v)", none, err)
	}
}

func TestAppendChangeLogTx_UsableInCallerTransaction(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	tx, err := s.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.AppendChangeLogTx(tx, ChangeRecord{Repo: clRepo, EntityType: clType, EntityID: clID, Version: 1, Kinds: []string{"annotation_changed"}, Origin: "pg-desk", At: "t"})
	if err != nil || seq <= 0 {
		t.Fatalf("append = (%d, %v)", seq, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n := changeCount(t, s); n != 0 {
		t.Fatalf("rows after rollback = %d, want 0", n)
	}
}

func TestLatestChangeAt_PerEntityMaxTypeScopedAndAbsentWhenNoRows(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	mk := func(typ, id string) Entity {
		return Entity{Repo: clRepo, EntityType: typ, EntityID: id, Facts: "{}", AsOf: "x"}
	}
	var v int64
	var err error
	if v, err = s.WriteEntityWithLog(mk("pr", "a"), 0, []string{"created"}, "sync", "2026-09-10T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteEntityWithLog(mk("pr", "a"), v, []string{"reconcile"}, "sweep", "2026-09-12T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteEntityWithLog(mk("pr", "b"), 0, []string{"created"}, "sync", "2026-09-11T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteEntityWithLog(mk("issue", "c"), 0, []string{"created"}, "sync", "2026-09-13T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// An entity written with no kinds appends no change_log row.
	if _, err = s.WriteEntityStateWithLog(mk("pr", "quiet"), 0, "", true, nil, "sync", "2026-09-14T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	got, err := s.LatestChangeAt("pr")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "2026-09-12T00:00:00Z", "b": "2026-09-11T00:00:00Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LatestChangeAt(pr) = %v, want %v (type-scoped, max per entity, none for a row-less entity)", got, want)
	}
}

func TestLatestChangeAt_RefusesOldSchema(t *testing.T) {
	s := OpenForTest(t)
	if _, err := s.LatestChangeAt("pr"); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("err = %v, want ErrOldSchema", err)
	}
}
