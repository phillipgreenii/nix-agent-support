package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Ladder rung 7, entity closed-at: entity.closed_at and the partial index
// entity_closed_at, written only by Store.SetClosedAt (HT-29, HT-30; INV-HOIST-11,
// INV-HOIST-20, INV-HOIST-21).

const closedAtVersion = 7

// rawEntities dumps every column of every entity row except closed_at as text,
// in key order, so a test compares what a rung left untouched byte for byte.
func rawEntities(t *testing.T, s *Store) []string {
	t.Helper()
	var cols []string
	for _, c := range columnNames(t, s, "entity") {
		if c != "closed_at" {
			cols = append(cols, "ifnull("+c+", 'NULL')")
		}
	}
	rows, err := s.DB().Query(`SELECT ` + strings.Join(cols, " || '|' || ") + ` FROM entity ORDER BY kind, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLadderRung7IsNamed(t *testing.T) {
	ladder := BuiltinMigrations()
	if len(ladder) < closedAtVersion || SchemaVersion < closedAtVersion {
		t.Fatalf("the ladder has %d rungs and SchemaVersion = %d, want at least %d of each", len(ladder), SchemaVersion, closedAtVersion)
	}
	if r := ladder[closedAtVersion-1]; r.Version != closedAtVersion || r.Name != "entity closed-at" {
		t.Errorf("rung 7 = %d %q, want %d %q", r.Version, r.Name, closedAtVersion, "entity closed-at")
	}
	for _, part := range []string{
		"ALTER TABLE entity ADD COLUMN closed_at INTEGER;",
		"CREATE INDEX entity_closed_at ON entity (kind, closed_at) WHERE closed_at IS NOT NULL;",
	} {
		if !strings.Contains(schemaV7, part) {
			t.Errorf("schemaV7 lacks %q", part)
		}
	}
}

// TestOpenFromOlderFixturesAddsClosedAtAndKeepsRows is HT-29: every fixture from
// version 3 to 6 migrates to the top with its entity, log and head byte-equal to
// what the same file reads as one rung below the new one, the new column NULL
// everywhere, and the partial index present.
func TestOpenFromOlderFixturesAddsClosedAtAndKeepsRows(t *testing.T) {
	ctx := context.Background()
	ran := 0
	for v, fx := range fixtures(t) {
		if v < 3 || v >= closedAtVersion {
			continue
		}
		ran++
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			path := dbPath(t)
			copyFile(t, fx, path)

			// What the file holds with the ladder cut one rung below the new one.
			pre, _, err := Open(path, Options{Migrations: BuiltinMigrations()[:closedAtVersion-1], Synchronous: "OFF"})
			if err != nil {
				t.Fatal(err)
			}
			entitiesBefore := rawEntities(t, pre)
			headBefore, logBefore := logSnapshot(t, ctx, pre)
			if err := pre.Close(); err != nil {
				t.Fatal(err)
			}

			s, res := mustOpen(t, path, fastOpts())
			if res.Outcome != OutcomeMigrated || res.ToVersion != SchemaVersion {
				t.Fatalf("result = %+v, want migrated to %d", res, SchemaVersion)
			}
			assertFixtureData(t, ctx, s)
			assertFixtureJobs(t, ctx, s, v)
			assertFixtureChanges(t, ctx, s, v)

			if got := rawEntities(t, s); !reflect.DeepEqual(got, entitiesBefore) {
				t.Errorf("entity rows = %q, want %q", got, entitiesBefore)
			}
			head, log := logSnapshot(t, ctx, s)
			if head != headBefore || !reflect.DeepEqual(log, logBefore) {
				t.Errorf("change head/log = %d %+v, want %d %+v", head, log, headBefore, logBefore)
			}
			var closed, idx int
			if err := s.DB().QueryRow(`SELECT count(*) FROM entity WHERE closed_at IS NOT NULL`).Scan(&closed); err != nil || closed != 0 {
				t.Errorf("closed entity count = %d err=%v, want 0", closed, err)
			}
			if err := s.DB().QueryRow(
				`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'entity_closed_at'`,
			).Scan(&idx); err != nil || idx != 1 {
				t.Errorf("entity_closed_at index count = %d err=%v, want 1", idx, err)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no fixture between version 3 and 6 was exercised")
	}
}

func TestVersion7FixtureCarriesTheClosedEntity(t *testing.T) {
	ctx := context.Background()
	fx, ok := fixtures(t)[closedAtVersion]
	if !ok {
		t.Fatalf("no version %d fixture", closedAtVersion)
	}
	path := dbPath(t)
	copyFile(t, fx, path)
	s, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeMigrated || res.FromVersion != closedAtVersion || res.ToVersion != SchemaVersion {
		t.Fatalf("result = %+v, want migrated %d -> %d", res, closedAtVersion, SchemaVersion)
	}
	var kind, id string
	if err := s.DB().QueryRow(`SELECT kind, id FROM entity WHERE closed_at IS NOT NULL`).Scan(&kind, &id); err != nil {
		t.Fatal(err)
	}
	e, ok, err := s.GetEntity(ctx, kind, id)
	if err != nil || !ok || e.ClosedAt.UnixMilli() != 1760000007000 {
		t.Errorf("entity = %+v ok=%v err=%v, want closed at 1760000007000", e, ok, err)
	}
}

func TestSetClosedAtSetsClearsAndTouchesNothingElse(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	row := EntityRow{
		Kind: "example-kind", ID: "example-1", Version: 4, LastAccess: time.UnixMilli(1760000000000).UTC(),
		KeepAlive: true,
	}
	if err := s.PutEntity(ctx, row); err != nil {
		t.Fatal(err)
	}
	headBefore, logBefore := logSnapshot(t, ctx, s)

	at := time.UnixMilli(1760000009000).UTC()
	if err := s.SetClosedAt(ctx, row.Kind, row.ID, at); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetEntity(ctx, row.Kind, row.ID)
	if err != nil || !ok {
		t.Fatalf("entity = %+v ok=%v err=%v", got, ok, err)
	}
	want := row
	want.ClosedAt = at
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after set: %+v, want %+v", got, want)
	}
	var stored int64
	if err := s.DB().QueryRow(`SELECT closed_at FROM entity WHERE kind = ? AND id = ?`, row.Kind, row.ID).Scan(&stored); err != nil || stored != 1760000009000 {
		t.Errorf("closed_at column = %d err=%v, want Unix milliseconds 1760000009000", stored, err)
	}

	if err := s.SetClosedAt(ctx, row.Kind, row.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := s.DB().QueryRow(`SELECT closed_at IS NULL FROM entity WHERE kind = ? AND id = ?`, row.Kind, row.ID).Scan(&isNull); err != nil || !isNull {
		t.Errorf("closed_at IS NULL = %v err=%v after a zero time, want true", isNull, err)
	}
	got, _, err = s.GetEntity(ctx, row.Kind, row.ID)
	if err != nil || !reflect.DeepEqual(got, row) {
		t.Errorf("after clear: %+v err=%v, want %+v", got, err, row)
	}

	head, log := logSnapshot(t, ctx, s)
	if head != headBefore || !reflect.DeepEqual(log, logBefore) {
		t.Errorf("change head/log moved: %d %+v, want %d %+v", head, log, headBefore, logBefore)
	}
}

func TestSetClosedAtOnAMissingRowUpdatesNothing(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if err := s.SetClosedAt(ctx, "example-kind", "example-absent", time.UnixMilli(1760000009000)); err != nil {
		t.Fatalf("SetClosedAt on a missing row = %v, want nil", err)
	}
	if _, ok, err := s.GetEntity(ctx, "example-kind", "example-absent"); err != nil || ok {
		t.Errorf("entity exists = %v err=%v, want it absent", ok, err)
	}
}

// TestPutEntityOfAnOldCopyKeepsClosedAt is HT-30: a copy of the row read before
// SetClosedAt and written back after it leaves closed_at as SetClosedAt set it.
func TestPutEntityOfAnOldCopyKeepsClosedAt(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if err := s.PutEntity(ctx, EntityRow{Kind: "example-kind", ID: "example-1", Version: 1}); err != nil {
		t.Fatal(err)
	}
	old, ok, err := s.GetEntity(ctx, "example-kind", "example-1")
	if err != nil || !ok {
		t.Fatalf("old copy = %+v ok=%v err=%v", old, ok, err)
	}
	at := time.UnixMilli(1760000009000).UTC()
	if err := s.SetClosedAt(ctx, "example-kind", "example-1", at); err != nil {
		t.Fatal(err)
	}
	old.Version = 2
	if err := s.PutEntity(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetEntity(ctx, "example-kind", "example-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ClosedAt.Equal(at) || got.Version != 2 {
		t.Errorf("entity = %+v, want closed at %v and version 2", got, at)
	}
}
