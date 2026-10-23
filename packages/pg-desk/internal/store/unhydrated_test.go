package store

import (
	"testing"
	"time"
)

// TestCountPRUnhydrated seeds one entity of every kind the set must include or
// exclude on a real schema-2 store, so each filter (type, active, version,
// hidden, closed or merged facts) is shown by a member that it removes.
func TestCountPRUnhydrated(t *testing.T) {
	s := OpenForTest(t)
	seed := func(typ, id, facts, asOf string) {
		t.Helper()
		if err := s.UpsertEntity(Entity{Repo: "o/r", EntityType: typ, EntityID: id, Facts: facts, AsOf: asOf}); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy rows, migrated to schema 2 by the cutover (version 0, active,
	// first_seen_at backfilled from as_of).
	seed("pr", "o/r#1", `{"pr_show":{"state":"open"}}`, "2026-10-01T00:00:00Z") // counted, oldest
	seed("pr", "o/r#2", `{}`, "2026-10-10T00:00:00Z")                           // counted: facts say nothing
	seed("pr", "o/r#3", `{"pr_show":{"state":"closed"}}`, "2026-09-01T00:00:00Z")
	seed("pr", "o/r#4", `{"pr_show":{"state":"open","merged":true}}`, "2026-09-01T00:00:00Z")
	seed("pr", "o/r#5", `{"pr_show":{"state":"open"}}`, "2026-09-01T00:00:00Z") // hidden below
	seed("pr", "o/r#6", `{"pr_show":{"state":"open"}}`, "2026-09-01T00:00:00Z") // hydrated below
	seed("pr", "o/r#7", `{"pr_show":{"state":"open"}}`, "2026-09-01T00:00:00Z") // made inactive below
	seed("pr", "o/r#8", `{"pr_show":{"state":"open"}}`, "2026-09-01T00:00:00Z") // hidden: false below, counted
	seed("issue", "i-1", `{}`, "2026-09-01T00:00:00Z")                          // not a pr
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.sql.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO annotation (repo, entity_type, entity_id, key, value, origin, set_by, set_at) VALUES ('o/r','pr','o/r#5','hidden','{"value":true,"reason":"x"}','pg-desk','t','2026-10-01T00:00:00Z')`)
	mustExec(`INSERT INTO annotation (repo, entity_type, entity_id, key, value, origin, set_by, set_at) VALUES ('o/r','pr','o/r#8','hidden','{"value":false,"reason":null}','pg-desk','t','2026-10-01T00:00:00Z')`)
	mustExec(`UPDATE entity SET version = 1, hydrated_at = '2026-10-02T00:00:00Z' WHERE entity_id = 'o/r#6'`)
	mustExec(`UPDATE entity SET active = 0 WHERE entity_id = 'o/r#7'`)

	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	got, err := s.CountPRUnhydrated(now)
	if err != nil {
		t.Fatalf("CountPRUnhydrated: %v", err)
	}
	// #1, #2 and #8 are the set; the oldest is #8 (first seen 2026-09-01).
	wantAge := int(now.Sub(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)).Seconds())
	if got.Count != 3 || got.OldestAgeSeconds != wantAge {
		t.Errorf("got %+v, want Count 3 and OldestAgeSeconds %d", got, wantAge)
	}
}

func TestCountPRUnhydratedEmptySetAndReadFailure(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	got, err := s.CountPRUnhydrated(time.Now())
	if err != nil || got.Count != 0 || got.OldestAgeSeconds != 0 {
		t.Errorf("empty store: got %+v, err %v; want a zero set", got, err)
	}
	// A schema-1 store has no version or active column: the read fails and
	// the failure is returned, never folded into a zero count.
	old := OpenForTest(t)
	if got, err := old.CountPRUnhydrated(time.Now()); err == nil {
		t.Errorf("schema-1 store: got %+v with no error; a failed read must be an error", got)
	}
}
