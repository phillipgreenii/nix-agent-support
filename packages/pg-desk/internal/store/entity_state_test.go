package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

const hydratedStamp = "2026-09-11T00:00:00Z"

func TestEntityReads_HydratedAtAndInactive(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.HydratedAt != "" || got.Inactive {
		t.Fatalf("unhydrated read = (%+v, %v, %v), want HydratedAt \"\" and active", got, found, err)
	}

	if _, err := s.WriteEntityStateWithLog(clEntity("b"), 0, hydratedStamp, false, []string{"removed"}, "sync", "t"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetEntity(clRepo, clType, clID)
	if got.HydratedAt != hydratedStamp || !got.Inactive || got.Version != 1 || got.Facts != "b" {
		t.Fatalf("after state write = %+v", got)
	}
	list, err := s.ListEntities()
	if err != nil || len(list) != 1 || list[0].HydratedAt != hydratedStamp || !list[0].Inactive {
		t.Fatalf("list = (%+v, %v)", list, err)
	}

	// UpsertEntity and WriteEntityWithLog must not touch the new columns.
	if err := s.UpsertEntity(clEntity("c")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteEntityWithLog(clEntity("d"), 1, []string{"facts_changed"}, "sync", "t2"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetEntity(clRepo, clType, clID)
	if got.HydratedAt != hydratedStamp || !got.Inactive || got.Facts != "d" {
		t.Fatalf("after legacy writers = %+v, want hydrated_at/active preserved", got)
	}
}

func TestEntityReads_DefaultsOnOldSchema(t *testing.T) {
	s := OpenForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.HydratedAt != "" || got.Inactive {
		t.Fatalf("old-schema read = (%+v, %v, %v)", got, found, err)
	}
	list, err := s.ListEntities()
	if err != nil || len(list) != 1 || list[0].HydratedAt != "" || list[0].Inactive {
		t.Fatalf("old-schema list = (%+v, %v)", list, err)
	}
}

func TestWriteEntityStateWithLog_WritesEverythingInOneTransaction(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityStateWithLog(clEntity("a"), 0, "2026-09-10T00:00:00Z", true, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("injected failure")
	s.SetBetweenBumpAndAppendHook(func() error { return boom })
	_, err := s.WriteEntityStateWithLog(clEntity("b"), 1, hydratedStamp, false, []string{"x"}, "sync", "t2")
	s.SetBetweenBumpAndAppendHook(nil)
	if !errors.Is(err, boom) || errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want injected failure and not a conflict", err)
	}
	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Version != 1 || got.Facts != "a" || got.HydratedAt != "2026-09-10T00:00:00Z" || got.Inactive {
		t.Fatalf("entity after failed write = %+v, want everything rolled back", got)
	}
	if n := changeCount(t, s); n != 1 {
		t.Fatalf("change_log rows = %d, want 1", n)
	}
	if s.ConflictCount() != 0 {
		t.Fatalf("ConflictCount = %d, want 0", s.ConflictCount())
	}

	// Hook cleared: the same write now succeeds and lands everything together.
	v, err := s.WriteEntityStateWithLog(clEntity("b"), 1, hydratedStamp, false, []string{"x"}, "sync", "t2")
	if err != nil || v != 2 {
		t.Fatalf("retry = (%d, %v)", v, err)
	}
	got, _, _ = s.GetEntity(clRepo, clType, clID)
	if got.Version != 2 || got.Facts != "b" || got.HydratedAt != hydratedStamp || !got.Inactive {
		t.Fatalf("entity after write = %+v", got)
	}
	recs, _ := s.ListEntityHistory(clRepo, clType, clID, 0)
	if len(recs) != 2 || recs[0].Version != 2 || recs[0].Origin != "sync" {
		t.Fatalf("history = %+v", recs)
	}
}

func TestWriteEntityStateWithLog_EmptyKindsUpdatesRowAppendsNothing(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityStateWithLog(clEntity("a"), 0, "2026-09-10T00:00:00Z", true, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}
	e := clEntity("a")
	e.AsOf = "2026-09-11T00:00:00Z"
	v, err := s.WriteEntityStateWithLog(e, 1, hydratedStamp, true, nil, "sync", "t2")
	if err != nil || v != 2 {
		t.Fatalf("rehydrate = (%d, %v), want (2, nil)", v, err)
	}
	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Version != 2 || got.HydratedAt != hydratedStamp || got.AsOf != "2026-09-11T00:00:00Z" {
		t.Fatalf("entity = %+v", got)
	}
	if n := changeCount(t, s); n != 1 {
		t.Fatalf("change_log rows = %d, want 1 (no row for empty kinds)", n)
	}

	// Empty-kinds first write (insert path) also appends nothing.
	other := Entity{Repo: clRepo, EntityType: clType, EntityID: "acme/widgets#8", Facts: "z"}
	if v, err := s.WriteEntityStateWithLog(other, 0, hydratedStamp, true, []string{}, "sync", "t3"); err != nil || v != 1 {
		t.Fatalf("empty-kinds insert = (%d, %v)", v, err)
	}
	if n := changeCount(t, s); n != 1 {
		t.Fatalf("change_log rows = %d, want 1", n)
	}
}

func TestWriteEntityStateWithLog_StaleVersionConflicts(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityStateWithLog(clEntity("a"), 0, hydratedStamp, true, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteEntityStateWithLog(clEntity("b"), 1, hydratedStamp, true, []string{"x"}, "sync", "t2"); err != nil {
		t.Fatal(err)
	}
	_, err := s.WriteEntityStateWithLog(clEntity("stale"), 1, "2020-01-01T00:00:00Z", false, []string{"x"}, "sync", "t3")
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if s.ConflictCount() != 1 {
		t.Fatalf("ConflictCount = %d, want 1", s.ConflictCount())
	}
	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Version != 2 || got.Facts != "b" || got.HydratedAt != hydratedStamp || got.Inactive {
		t.Fatalf("newer snapshot clobbered: %+v", got)
	}
	// A nonzero expected version on an absent row is also a conflict.
	absent := Entity{Repo: clRepo, EntityType: clType, EntityID: "nope"}
	if _, err := s.WriteEntityStateWithLog(absent, 3, hydratedStamp, true, []string{"x"}, "sync", "t"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("absent-row err = %v, want ErrVersionConflict", err)
	}
}

func TestWriteEntityStateWithLog_AdoptsCutoverRowAtVersionZero(t *testing.T) {
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
	if err != nil || !found || e.Version != 0 || e.HydratedAt != "" || e.Inactive {
		t.Fatalf("migrated entity = (%+v, %v, %v)", e, found, err)
	}
	e.Facts = `{"title":"changed"}`
	v, err := s.WriteEntityStateWithLog(e, 0, hydratedStamp, true, []string{"facts_changed"}, "sync", "t")
	if err != nil || v != 1 {
		t.Fatalf("write over migrated row = (%d, %v), want (1, nil)", v, err)
	}
	got, _, _ := s.GetEntity("acme/widgets", "pr", "acme/widgets#1")
	if got.HydratedAt != hydratedStamp || got.Inactive || got.Version != 1 {
		t.Fatalf("after adopt = %+v", got)
	}
}

func TestWriteEntityStateWithLog_RacingWritersExactlyOneWins(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityStateWithLog(clEntity("seed"), 0, hydratedStamp, true, []string{"created"}, "sync", "t0"); err != nil {
		t.Fatal(err)
	}
	handles := []*Store{s, openSecondHandle(t, s), openSecondHandle(t, s), openSecondHandle(t, s)}

	start := make(chan struct{})
	errs := make([]error, len(handles))
	var wg sync.WaitGroup
	for i, h := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Every goroutine writes from the same read version (1).
			_, errs[i] = h.WriteEntityStateWithLog(clEntity(fmt.Sprintf("w%d", i)), 1, fmt.Sprintf("2026-09-12T00:00:0%dZ", i), i%2 == 0, []string{"facts_changed"}, fmt.Sprintf("w%d", i), "t")
		}()
	}
	close(start)
	wg.Wait()

	winner := -1
	conflicts := 0
	for i, err := range errs {
		switch {
		case err == nil:
			if winner != -1 {
				t.Fatalf("two winners: %d and %d", winner, i)
			}
			winner = i
		case errors.Is(err, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winner == -1 || conflicts != len(handles)-1 {
		t.Fatalf("winner=%d conflicts=%d, want one winner and %d conflicts", winner, conflicts, len(handles)-1)
	}
	var counted int64
	for _, h := range handles {
		counted += h.ConflictCount()
	}
	if counted != int64(len(handles)-1) {
		t.Fatalf("ConflictCount total = %d, want %d", counted, len(handles)-1)
	}
	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Version != 2 || got.Facts != fmt.Sprintf("w%d", winner) || got.HydratedAt != fmt.Sprintf("2026-09-12T00:00:0%dZ", winner) || got.Inactive != (winner%2 != 0) {
		t.Fatalf("final entity = %+v, want winner %d's snapshot intact", got, winner)
	}
	if n := changeCount(t, s); n != 2 {
		t.Fatalf("change_log rows = %d, want 2", n)
	}
}

func TestWriteEntityStateWithLog_RefusesOldSchema(t *testing.T) {
	s := OpenForTest(t)
	_, err := s.WriteEntityStateWithLog(clEntity("a"), 0, hydratedStamp, true, []string{"x"}, "sync", "t")
	if !errors.Is(err, ErrOldSchema) {
		t.Fatalf("err = %v, want ErrOldSchema", err)
	}
}

func TestAppendEntityChange_AtCurrentVersionWithoutBump(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"created"}, "sync", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteEntityWithLog(clEntity("b"), 1, []string{"facts_changed"}, "sync", "t2"); err != nil {
		t.Fatal(err)
	}
	seq, err := s.AppendEntityChange(clRepo, clType, clID, []string{"link_added"}, "linker", "t3")
	if err != nil || seq == 0 {
		t.Fatalf("AppendEntityChange = (%d, %v)", seq, err)
	}
	got, _, _ := s.GetEntity(clRepo, clType, clID)
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2 (no bump)", got.Version)
	}
	recs, _ := s.ListEntityHistory(clRepo, clType, clID, 1)
	if len(recs) != 1 || recs[0].Seq != seq || recs[0].Version != 2 || recs[0].Origin != "linker" || len(recs[0].Kinds) != 1 || recs[0].Kinds[0] != "link_added" {
		t.Fatalf("newest record = %+v", recs)
	}
	if n := changeCount(t, s); n != 3 {
		t.Fatalf("change_log rows = %d, want 3", n)
	}
}

func TestAppendEntityChange_AbsentEntityAndOldSchema(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	_, err := s.AppendEntityChange(clRepo, clType, "missing", []string{"x"}, "o", "t")
	if !errors.Is(err, ErrNoEntity) {
		t.Fatalf("err = %v, want ErrNoEntity", err)
	}
	if n := changeCount(t, s); n != 0 {
		t.Fatalf("change_log rows = %d, want 0", n)
	}
	old := OpenForTest(t)
	if _, err := old.AppendEntityChange(clRepo, clType, clID, []string{"x"}, "o", "t"); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("old-schema err = %v, want ErrOldSchema", err)
	}
}
