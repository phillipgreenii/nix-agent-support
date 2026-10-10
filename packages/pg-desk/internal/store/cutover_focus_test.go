package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// The focus tables and entity.first_seen_at join the cutover block (docket
// pg2-2j5ac.44, packet 1). These tests pin that: the objects exist after
// Cutover, the cutover is still one all-or-nothing transaction with the
// version stamps last, the enforced constraints hold, and first_seen_at is
// stamped once by every insert path and never by an update.

var focusTables = []string{"focus_period", "focus_selection", "focus_draft", "focus_run"}

func TestCutoverCreatesFocusTablesAndFirstSeenAt(t *testing.T) {
	s := OpenNewSchemaForTest(t)

	for _, table := range focusTables {
		if !tableExists(t, s, table) {
			t.Errorf("table %s missing after cutover", table)
		}
		var n int
		if err := s.sql.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q`, table)).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows = %d, err=%v; want an empty table", table, n, err)
		}
	}
	if tableExists(t, s, "ledger") {
		t.Errorf("ledger must be gone after cutover")
	}

	wantCols := map[string][]string{
		"focus_period":    {"id", "period_type", "period_key", "cap", "closed_at", "close_note"},
		"focus_selection": {"id", "focus_period_id", "repo", "entity_type", "entity_id", "selected_at", "rank_position", "tier"},
		"focus_draft":     {"id", "period_type", "period_key", "cap", "made_at", "body_json"},
		"focus_run":       {"id", "run_id", "verb", "focus_period_id", "started_at", "actor", "exit_code", "counts_json"},
	}
	for table, want := range wantCols {
		if got := tableColumns(t, s, table); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}

	c, ok := column(t, s, "entity", "first_seen_at")
	if !ok || c.Type != "TEXT" || c.NotNull {
		t.Errorf("entity.first_seen_at = %+v (present=%v), want nullable TEXT", c, ok)
	}

	// A second cutover is a no-op.
	before := dumpDB(t, s)
	if err := s.Cutover(); err != nil {
		t.Fatalf("second Cutover: %v", err)
	}
	if after := dumpDB(t, s); after != before {
		t.Errorf("second Cutover changed the store")
	}
}

// TestCutoverVersionStampsStayLast keeps the all-or-nothing invariant: the
// two version stamps are the last two statements, after every focus object.
func TestCutoverVersionStampsStayLast(t *testing.T) {
	n := len(cutoverStatements)
	if !strings.Contains(cutoverStatements[n-2], "schema_version") || !strings.Contains(cutoverStatements[n-1], "user_version = 2") {
		t.Fatalf("the last two cutover statements are not the version stamps:\n%s\n%s", cutoverStatements[n-2], cutoverStatements[n-1])
	}
	last := -1
	for i, st := range cutoverStatements {
		if strings.Contains(st, "focus_") || strings.Contains(st, "first_seen_at") {
			last = i
		}
	}
	if last < 0 || last >= n-2 {
		t.Fatalf("focus statements must exist and precede the version stamps; last focus statement at %d of %d", last, n)
	}
}

// TestCutoverFocusStatementFailureLeavesVersion1 injects a failure right
// after each new statement and asserts the store is still version 1 with
// none of the new objects (the broader all-positions property is
// TestMigrateFailureLeavesStoreUnchanged).
func TestCutoverFocusStatementFailureLeavesVersion1(t *testing.T) {
	seen := 0
	for pos, st := range cutoverStatements {
		if !strings.Contains(st, "focus_") && !strings.Contains(st, "first_seen_at") {
			continue
		}
		seen++
		t.Run(fmt.Sprintf("fail_after_step_%02d", pos), func(t *testing.T) {
			s, _ := openSyntheticCopy(t)
			broken := append([]string(nil), cutoverStatements[:pos+1]...)
			broken = append(broken, "INSERT INTO table_that_does_not_exist VALUES (1)")
			broken = append(broken, cutoverStatements[pos+1:]...)
			if err := s.cutoverWith(broken); err == nil {
				t.Fatalf("cutoverWith succeeded despite an injected failure after step %d", pos)
			}
			if v, _ := s.SchemaVersion(); v != 1 {
				t.Errorf("user_version = %d, want 1", v)
			}
			for _, table := range focusTables {
				if tableExists(t, s, table) {
					t.Errorf("table %s exists after a failed cutover", table)
				}
			}
			if _, ok := column(t, s, "entity", "first_seen_at"); ok {
				t.Errorf("entity.first_seen_at exists after a failed cutover")
			}
		})
	}
	if seen < 5 {
		t.Fatalf("found only %d focus/first_seen_at statements; the block is missing them", seen)
	}
}

func TestCutoverBackfillsFirstSeenAtFromAsOf(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	entities, err := s.ListEntities()
	if err != nil || len(entities) != 3 {
		t.Fatalf("ListEntities = %d rows, err=%v; want 3", len(entities), err)
	}
	for _, e := range entities {
		if e.FirstSeenAt == "" || e.FirstSeenAt != e.AsOf {
			t.Errorf("entity %s FirstSeenAt = %q, want as_of %q", e.EntityID, e.FirstSeenAt, e.AsOf)
		}
		got, found, err := s.GetEntity(e.Repo, e.EntityType, e.EntityID)
		if err != nil || !found || got.FirstSeenAt != e.AsOf {
			t.Errorf("GetEntity(%s) FirstSeenAt = %q (found=%v, err=%v), want %q", e.EntityID, got.FirstSeenAt, found, err, e.AsOf)
		}
	}
}

func TestFocusConstraintsAreEnforced(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.sql.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(what, q string, args ...any) {
		t.Helper()
		if _, err := s.sql.Exec(q, args...); err == nil {
			t.Errorf("%s: statement succeeded, want a constraint failure", what)
		}
	}

	mustExec(`INSERT INTO focus_period (period_type, period_key) VALUES ('day', '2026-09-23')`)
	mustFail("duplicate period", `INSERT INTO focus_period (period_type, period_key) VALUES ('day', '2026-09-23')`)
	mustFail("period_type CHECK", `INSERT INTO focus_period (period_type, period_key) VALUES ('month', '2026-09')`)

	const insSel = `INSERT INTO focus_selection (focus_period_id, repo, entity_type, entity_id, selected_at, rank_position, tier) VALUES (?, ?, ?, ?, ?, ?, ?)`
	mustExec(insSel, 1, clRepo, clType, clID, "2026-09-23T00:00:00Z", 1, "started")
	mustFail("duplicate selection", insSel, 1, clRepo, clType, clID, "2026-09-23T00:00:00Z", 2, "started")
	mustFail("entity foreign key", insSel, 1, clRepo, clType, "acme/widgets#999", "2026-09-23T00:00:00Z", 2, nil)
	mustFail("period foreign key", insSel, 99, clRepo, clType, clID, "2026-09-23T00:00:00Z", 2, nil)
	mustFail("tier CHECK", `INSERT INTO focus_selection (focus_period_id, repo, entity_type, entity_id, selected_at, rank_position, tier)
VALUES (1, 'acme/widgets', 'pr', 'acme/widgets#7', 'x', 1, 'bogus')`)
	// A hand-add carries a NULL tier.
	mustExec(`DELETE FROM focus_selection`)
	mustExec(insSel, 1, clRepo, clType, clID, "2026-09-23T00:00:00Z", 1, nil)

	const insDraft = `INSERT INTO focus_draft (id, period_type, period_key, cap, made_at, body_json) VALUES (?, 'day', '2026-09-23', 5, 'now', '{}')`
	mustExec(insDraft, 1)
	mustFail("second draft row (id 1 again)", insDraft, 1)
	mustFail("draft CHECK (id = 1)", insDraft, 2)

	const insRun = `INSERT INTO focus_run (run_id, verb, focus_period_id, started_at, actor, counts_json) VALUES (?, 'select', ?, 'now', 'a', '{}')`
	mustExec(insRun, "01RUN", 1)
	mustFail("duplicate run_id", insRun, "01RUN", 1)
	mustFail("run period foreign key", insRun, "01RUN2", 99)
	mustExec(insRun, "01RUN3", nil)
}

func TestVersion1StoreHasNoFocusObjects(t *testing.T) {
	s := OpenForTest(t)
	for _, table := range focusTables {
		if tableExists(t, s, table) {
			t.Errorf("version-1 store has %s", table)
		}
	}
	if _, ok := column(t, s, "entity", "first_seen_at"); ok {
		t.Errorf("version-1 entity has first_seen_at")
	}
	if err := s.UpsertEntity(clEntity("a")); err != nil {
		t.Fatalf("UpsertEntity on version 1: %v", err)
	}
	got, found, err := s.GetEntity(clRepo, clType, clID)
	if err != nil || !found || got.FirstSeenAt != "" {
		t.Fatalf("GetEntity on version 1 = %+v, found=%v, err=%v; want FirstSeenAt empty", got, found, err)
	}
	list, err := s.ListEntities()
	if err != nil || len(list) != 1 || list[0].FirstSeenAt != "" {
		t.Fatalf("ListEntities on version 1 = %+v, %v", list, err)
	}
}

func rawFirstSeen(t *testing.T, s *Store) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.sql.QueryRow(`SELECT first_seen_at FROM entity WHERE repo = ? AND entity_type = ? AND entity_id = ?`, clRepo, clType, clID).Scan(&v); err != nil {
		t.Fatalf("read first_seen_at: %v", err)
	}
	return v
}

// TestFirstSeenAtStampedOnceByEveryWriter: a first insert through each of
// the four writers stamps first_seen_at; a later update through each leaves
// it unchanged, even with a later as_of and a later write time.
func TestFirstSeenAtStampedOnceByEveryWriter(t *testing.T) {
	const (
		firstAt = "2026-09-10T00:00:00Z" // the insert's write time / as_of
		laterAt = "2026-09-20T00:00:00Z"
	)
	fp := "fp1"
	writers := []struct {
		name string
		// insert writes the row for the first time at the given time and
		// returns the version it produced; update writes it again.
		insert func(s *Store, e Entity, at string) error
		update func(s *Store, e Entity, at string, version int64) error
		// want is the value first_seen_at must hold after the insert.
		want string
	}{
		{
			name:   "UpsertEntity",
			insert: func(s *Store, e Entity, at string) error { return s.UpsertEntity(e) },
			update: func(s *Store, e Entity, at string, _ int64) error { return s.UpsertEntity(e) },
			want:   firstAt, // the row's as_of: UpsertEntity has no write time
		},
		{
			name: "WriteEntityWithLog",
			insert: func(s *Store, e Entity, at string) error {
				_, err := s.WriteEntityWithLog(e, 0, []string{"facts_changed"}, "sync", at)
				return err
			},
			update: func(s *Store, e Entity, at string, v int64) error {
				_, err := s.WriteEntityWithLog(e, v, []string{"facts_changed"}, "sync", at)
				return err
			},
			want: firstAt,
		},
		{
			name: "WriteEntityStateWithLog",
			insert: func(s *Store, e Entity, at string) error {
				_, err := s.WriteEntityStateWithLog(e, 0, at, true, []string{"facts_changed"}, "sync", at)
				return err
			},
			update: func(s *Store, e Entity, at string, v int64) error {
				_, err := s.WriteEntityStateWithLog(e, v, at, true, []string{"facts_changed"}, "sync", at)
				return err
			},
			want: firstAt,
		},
		{
			name: "WriteEntityStateWithLogFP",
			insert: func(s *Store, e Entity, at string) error {
				_, err := s.WriteEntityStateWithLogFP(e, 0, at, true, &fp, []string{"facts_changed"}, "sync", at)
				return err
			},
			update: func(s *Store, e Entity, at string, v int64) error {
				_, err := s.WriteEntityStateWithLogFP(e, v, at, true, &fp, []string{"facts_changed"}, "sync", at)
				return err
			},
			want: firstAt,
		},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			s := OpenNewSchemaForTest(t)
			e := clEntity("a")
			e.AsOf = firstAt
			if err := w.insert(s, e, firstAt); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if got := rawFirstSeen(t, s); !got.Valid || got.String != w.want {
				t.Fatalf("first_seen_at after insert = %+v, want %q", got, w.want)
			}
			got, _, _ := s.GetEntity(clRepo, clType, clID)
			if got.FirstSeenAt != w.want {
				t.Fatalf("GetEntity FirstSeenAt = %q, want %q", got.FirstSeenAt, w.want)
			}

			e2 := clEntity("b")
			e2.AsOf = laterAt
			if err := w.update(s, e2, laterAt, got.Version); err != nil {
				t.Fatalf("update: %v", err)
			}
			after, _, _ := s.GetEntity(clRepo, clType, clID)
			if after.Facts != "b" || after.AsOf != laterAt {
				t.Fatalf("update did not land: %+v", after)
			}
			if after.FirstSeenAt != w.want {
				t.Fatalf("first_seen_at after update = %q, want it unchanged at %q", after.FirstSeenAt, w.want)
			}
		})
	}
}

// TestFirstSeenAtOfARowTheCutoverLeftAtVersion0 covers the in-place update
// of a migrated row: the backfilled first_seen_at survives the first
// versioned write.
func TestFirstSeenAtOfARowTheCutoverLeftAtVersion0(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	const id = "acme/widgets#1"
	before, found, err := s.GetEntity("acme/widgets", "pr", id)
	if err != nil || !found || before.Version != 0 {
		t.Fatalf("GetEntity = %+v, found=%v, err=%v", before, found, err)
	}
	e := before
	e.Facts, e.AsOf = `{"title":"changed"}`, "2026-10-01T00:00:00Z"
	if _, err := s.WriteEntityWithLog(e, 0, []string{"facts_changed"}, "sync", "2026-10-01T00:00:00Z"); err != nil {
		t.Fatalf("WriteEntityWithLog: %v", err)
	}
	after, _, _ := s.GetEntity("acme/widgets", "pr", id)
	if after.FirstSeenAt != before.AsOf || after.AsOf != "2026-10-01T00:00:00Z" {
		t.Fatalf("after = %+v; want first_seen_at %q kept and as_of updated", after, before.AsOf)
	}
}
