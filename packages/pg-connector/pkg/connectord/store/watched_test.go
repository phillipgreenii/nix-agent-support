package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The watched flag (ladder rung 5): the per-entity watched_at and watched_via
// columns, the log row's watched snapshot, and the store API around them.

func TestLadderRung5IsNamedAndAppendedAfterRung4(t *testing.T) {
	ladder := BuiltinMigrations()
	if len(ladder) < 5 {
		t.Fatalf("the ladder has %d rungs, want at least 5", len(ladder))
	}
	if ladder[4].Version != 5 || ladder[4].Name != "watched flag and log snapshot" {
		t.Errorf("rung at index 4 = %d %q, want 5 %q", ladder[4].Version, ladder[4].Name, "watched flag and log snapshot")
	}
}

// TestOpenFromOldFixturesAddsTheWatchedColumnsAndKeepsRows opens the version 3 and
// 4 fixtures (every fixture below 5) and checks the migrated store has the new
// columns and the partial index, every old row reads as unwatched, and the old
// rows are still there.
func TestOpenFromOldFixturesAddsTheWatchedColumnsAndKeepsRows(t *testing.T) {
	ctx := context.Background()
	ran := 0
	for v, fx := range fixtures(t) {
		if v < 3 || v >= watchedVersion {
			continue
		}
		ran++
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			path := dbPath(t)
			copyFile(t, fx, path)
			s, res := mustOpen(t, path, fastOpts())
			if res.Outcome != OutcomeMigrated || res.FromVersion != v || res.ToVersion != SchemaVersion {
				t.Fatalf("result = %+v, want migrated %d -> %d", res, v, SchemaVersion)
			}
			assertFixtureData(t, ctx, s)
			assertFixtureJobs(t, ctx, s, v)
			assertFixtureChanges(t, ctx, s, v)

			// The old fixtures hold one entity row; read its key from the table.
			var kind, id string
			if err := s.DB().QueryRow(`SELECT kind, id FROM entity`).Scan(&kind, &id); err != nil {
				t.Fatal(err)
			}
			e, ok, err := s.GetEntity(ctx, kind, id)
			if err != nil || !ok || !e.WatchedAt.IsZero() || e.WatchedVia != "" {
				t.Errorf("entity of an old fixture = %+v ok=%v err=%v, want unwatched", e, ok, err)
			}
			rows, err := s.ChangesAfter(ctx, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range rows {
				if c.Watched {
					t.Errorf("a row written before rung 5 reads Watched: %+v", c)
				}
			}
			var idx int
			if err := s.DB().QueryRow(
				`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'entity_watched'`,
			).Scan(&idx); err != nil || idx != 1 {
				t.Errorf("entity_watched index count = %d err=%v, want 1", idx, err)
			}
			if n, err := s.CountWatched(ctx, kind); err != nil || n != 0 {
				t.Errorf("CountWatched = %d err=%v, want 0", n, err)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no fixture below version 5 was exercised")
	}
}

func TestVersion5FixtureCarriesTheWatchedEntity(t *testing.T) {
	ctx := context.Background()
	fx, ok := fixtures(t)[watchedVersion]
	if !ok {
		t.Fatalf("no version %d fixture", watchedVersion)
	}
	path := dbPath(t)
	copyFile(t, fx, path)
	s, _ := mustOpen(t, path, fastOpts())
	var kind string
	if err := s.DB().QueryRow(`SELECT kind FROM entity WHERE watched_at IS NOT NULL`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListWatched(ctx, kind)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].WatchedVia != "direct" || got[0].WatchedAt.UnixMilli() != 1760000006000 {
		t.Errorf("ListWatched = %+v, want the sample entity watched via direct at 1760000006000", got)
	}
}

func TestSetWatchedFirstCauseWinsAndReportsNewlyOnce(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	first := time.UnixMilli(1760000000000).UTC()
	later := first.Add(time.Hour)

	var newly1, newly2 bool
	err := s.InTx(ctx, func(tx *Tx) error {
		var err error
		if newly1, err = tx.SetWatched(ctx, "example-kind", "example-1", "query:example", first); err != nil {
			return err
		}
		newly2, err = tx.SetWatched(ctx, "example-kind", "example-1", "direct", later)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !newly1 || newly2 {
		t.Errorf("newly = %v then %v, want true then false", newly1, newly2)
	}
	e, ok, err := s.GetEntity(ctx, "example-kind", "example-1")
	if err != nil || !ok {
		t.Fatalf("entity = %+v ok=%v err=%v", e, ok, err)
	}
	if e.WatchedVia != "query:example" || !e.WatchedAt.Equal(first) {
		t.Errorf("entity watched = %v via %q, want %v via %q (first cause wins)", e.WatchedAt, e.WatchedVia, first, "query:example")
	}
	if e.WatchedAt.UnixMilli() != first.UnixMilli() {
		t.Errorf("watched_at = %d, want Unix milliseconds %d", e.WatchedAt.UnixMilli(), first.UnixMilli())
	}
}

func TestSetWatchedCreatesAMissingEntityAndIsWatchedReadsTheFlag(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	err := s.InTx(ctx, func(tx *Tx) error {
		if w, err := tx.IsWatched(ctx, "example-kind", "example-missing"); err != nil || w {
			t.Errorf("IsWatched of a missing entity = %v err=%v, want false", w, err)
		}
		if _, err := tx.SetWatched(ctx, "example-kind", "example-missing", "adhoc:example", time.UnixMilli(5)); err != nil {
			return err
		}
		if w, err := tx.IsWatched(ctx, "example-kind", "example-missing"); err != nil || !w {
			t.Errorf("IsWatched after SetWatched = %v err=%v, want true", w, err)
		}
		if err := tx.EnsureEntity(ctx, "example-kind", "example-plain"); err != nil {
			return err
		}
		if w, err := tx.IsWatched(ctx, "example-kind", "example-plain"); err != nil || w {
			t.Errorf("IsWatched of an unwatched entity = %v err=%v, want false", w, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetEntity(ctx, "example-kind", "example-missing"); err != nil || !ok {
		t.Errorf("SetWatched did not create the entity row: ok=%v err=%v", ok, err)
	}
}

func TestListWatchedAndCountWatchedAreScopedToTheKindAndOrdered(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	base := time.UnixMilli(1760000000000).UTC()
	err := s.InTx(ctx, func(tx *Tx) error {
		for _, w := range []struct {
			kind, id string
			at       time.Time
		}{
			{"example-kind", "example-b", base.Add(time.Second)},
			{"example-kind", "example-c", base},
			{"example-kind", "example-a", base.Add(time.Second)},
			{"example-other", "example-b2", base},
		} {
			if _, err := tx.SetWatched(ctx, w.kind, w.id, "direct", w.at); err != nil {
				return err
			}
		}
		return tx.EnsureEntity(ctx, "example-kind", "example-unwatched")
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListWatched(ctx, "example-kind")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if want := []string{"example-c", "example-a", "example-b"}; !slices.Equal(ids, want) {
		t.Errorf("ListWatched(example-kind) = %v, want %v (by watched_at then id)", ids, want)
	}
	if n, err := s.CountWatched(ctx, "example-kind"); err != nil || n != 3 {
		t.Errorf("CountWatched(example-kind) = %d err=%v, want 3", n, err)
	}
	if n, err := s.CountWatched(ctx, "example-other"); err != nil || n != 1 {
		t.Errorf("CountWatched(other) = %d err=%v, want 1", n, err)
	}
	if n, err := s.CountWatched(ctx, "none"); err != nil || n != 0 {
		t.Errorf("CountWatched(none) = %d err=%v, want 0", n, err)
	}
	if rows, err := s.ListWatched(ctx, "none"); err != nil || len(rows) != 0 {
		t.Errorf("ListWatched(none) = %+v err=%v, want none", rows, err)
	}
}

// TestNoExportedMethodClearsTheWatchedFlag pins the stickiness by absence: the
// exported methods of *Store and *Tx whose names contain Watched are exactly the
// four of the API, and there is no clear or unwatch operation.
func TestNoExportedMethodClearsTheWatchedFlag(t *testing.T) {
	var got []string
	for _, typ := range []reflect.Type{reflect.TypeOf(&Store{}), reflect.TypeOf(&Tx{})} {
		for i := range typ.NumMethod() {
			if name := typ.Method(i).Name; strings.Contains(strings.ToLower(name), "watch") {
				got = append(got, typ.Elem().Name()+"."+name)
			}
		}
	}
	slices.Sort(got)
	want := []string{"Store.CountWatched", "Store.ListWatched", "Tx.IsWatched", "Tx.SetWatched"}
	if !slices.Equal(got, want) {
		t.Errorf("exported methods mentioning the watched flag = %v, want exactly %v", got, want)
	}
}

func TestPutEntityOfAnOldCopyLeavesTheWatchedFlagAndDeleteEntityRemovesIt(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	at := time.UnixMilli(1760000000000).UTC()

	if err := s.PutEntity(ctx, EntityRow{Kind: "example-kind", ID: "example-1", Version: 1}); err != nil {
		t.Fatal(err)
	}
	old, ok, err := s.GetEntity(ctx, "example-kind", "example-1") // the old copy: unwatched
	if err != nil || !ok {
		t.Fatalf("old copy = %+v ok=%v err=%v", old, ok, err)
	}
	if err := s.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.SetWatched(ctx, "example-kind", "example-1", "direct", at); err != nil {
			return err
		}
		_, err := tx.AppendChange(ctx, ChangeRow{Kind: "example-kind", ID: "example-1", At: at, Watched: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	old.Version = 2
	old.LastAccess = time.UnixMilli(7)
	if err := s.PutEntity(ctx, old); err != nil { // a read-modify-write of the old copy
		t.Fatal(err)
	}
	e, _, err := s.GetEntity(ctx, "example-kind", "example-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != 2 || e.LastAccess.UnixMilli() != 7 {
		t.Errorf("PutEntity did not write its own columns: %+v", e)
	}
	if !e.WatchedAt.Equal(at) || e.WatchedVia != "direct" {
		t.Errorf("PutEntity of an old copy changed the flag: watched_at %v via %q, want %v via direct", e.WatchedAt, e.WatchedVia, at)
	}
	// PutEntity of a copy carrying a (different) flag must not write it either.
	forged := old
	forged.WatchedAt, forged.WatchedVia = at.Add(time.Hour), "adhoc:other"
	if err := s.PutEntity(ctx, forged); err != nil {
		t.Fatal(err)
	}
	if e, _, _ := s.GetEntity(ctx, "example-kind", "example-1"); !e.WatchedAt.Equal(at) || e.WatchedVia != "direct" {
		t.Errorf("PutEntity wrote the flag columns: %+v", e)
	}

	if err := s.DeleteEntity(ctx, "example-kind", "example-1"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountWatched(ctx, "example-kind"); err != nil || n != 0 {
		t.Errorf("CountWatched after DeleteEntity = %d err=%v, want 0", n, err)
	}
	// A log row's snapshot survives the flag's deletion by eviction.
	rows, err := s.LastChanges(ctx, "example-kind", "example-1", 5)
	if err != nil || len(rows) != 1 || !rows[0].Watched {
		t.Errorf("log rows after DeleteEntity = %+v err=%v, want the one watched row", rows, err)
	}
}

func TestAppendChangeWritesTheWatchedSnapshotAndItReadsBack(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	at := time.UnixMilli(1760000000000).UTC()
	if err := s.InTx(ctx, func(tx *Tx) error {
		for i, watched := range []bool{true, false, false} {
			if _, err := tx.AppendChange(ctx, ChangeRow{
				Kind: "example-kind", ID: "example-1", Version: int64(i), At: at, Watched: watched,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := s.ChangesAfter(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []bool
	for _, c := range after {
		got = append(got, c.Watched)
	}
	if want := []bool{true, false, false}; !slices.Equal(got, want) {
		t.Errorf("ChangesAfter Watched = %v, want %v", got, want)
	}
	last, err := s.LastChanges(ctx, "example-kind", "example-1", 3) // newest first
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, c := range last {
		got = append(got, c.Watched)
	}
	if want := []bool{false, false, true}; !slices.Equal(got, want) {
		t.Errorf("LastChanges Watched = %v, want %v", got, want)
	}
}
