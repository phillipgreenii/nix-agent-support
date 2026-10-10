package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// Tests of the focus access layer (focus.go): period get-or-create, plan
// reads, the annotation function, the single stored draft and its
// transactional re-checks, the lock transaction, run records and statistics,
// and SetAnnotationSeq.

const focusRepo = "acme/widgets"

func focusSeed(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		e := Entity{Repo: focusRepo, EntityType: "pr", EntityID: id, Facts: `{"n":"` + id + `"}`, AsOf: "2026-09-10T00:00:00Z", ContentHash: "h-" + id}
		if _, err := s.WriteEntityWithLog(e, 0, []string{"created"}, "sync", "2026-09-10T00:00:00Z"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func focusRow(periodID int64, id string, pos int) FocusSelection {
	return FocusSelection{FocusPeriodID: periodID, Repo: focusRepo, EntityType: "pr", EntityID: id, SelectedAt: "2026-09-23T08:00:00Z", RankPosition: pos}
}

func focusLock(t *testing.T, s *Store, fn func(*FocusTx) error) {
	t.Helper()
	if err := s.FocusLockTx(fn); err != nil {
		t.Fatalf("FocusLockTx: %v", err)
	}
}

// focusPlan locks rows (id, position) into the period (periodType day, key).
func focusPlan(t *testing.T, s *Store, key string, ids ...string) FocusPeriod {
	t.Helper()
	var p FocusPeriod
	focusLock(t, s, func(tx *FocusTx) error {
		var err error
		if p, err = tx.GetOrCreatePeriod("day", key); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.InsertSelection(focusRow(p.ID, id, i+1)); err != nil {
				return err
			}
		}
		return nil
	})
	return p
}

func tableCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.sql.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestFocusPeriodGetOrCreate(t *testing.T) {
	s := OpenNewSchemaForTest(t)

	a, err := s.GetOrCreateFocusPeriod("day", "2026-9-3")
	if err != nil {
		t.Fatal(err)
	}
	if a.PeriodType != "day" || a.PeriodKey != "2026-09-03" || a.Cap != nil || a.ClosedAt != "" || a.CloseNote != "" {
		t.Fatalf("created period = %+v; want day 2026-09-03 with cap/closed_at/close_note empty", a)
	}
	b, err := s.GetOrCreateFocusPeriod("day", "2026-09-03")
	if err != nil || b != a {
		t.Fatalf("second call = %+v, %v; want the same period %+v", b, err, a)
	}
	if n := tableCount(t, s, "focus_period"); n != 1 {
		t.Fatalf("2026-9-3 and 2026-09-03 made %d periods, want 1", n)
	}

	for _, bad := range []struct{ typ, key string }{
		{"day", "not-a-date"},
		{"day", "2026-02-30"},
		{"day", ""},
		{"day", "26-9-3"},
		{"month", "2026-09"},
		{"", "2026-09-03"},
		{"week", ""},
	} {
		if _, err := s.GetOrCreateFocusPeriod(bad.typ, bad.key); err == nil {
			t.Errorf("GetOrCreateFocusPeriod(%q,%q) succeeded, want an error", bad.typ, bad.key)
		}
	}
	if n := tableCount(t, s, "focus_period"); n != 1 {
		t.Fatalf("a failed call wrote a period: %d rows", n)
	}

	// Read-only: FocusPeriodGet never creates, and normalizes the key.
	got, ok, err := s.FocusPeriodGet("day", "2026-9-3")
	if err != nil || !ok || got != a {
		t.Fatalf("FocusPeriodGet = %+v, %v, %v; want %+v", got, ok, err, a)
	}
	if _, ok, err := s.FocusPeriodGet("day", "2026-09-04"); err != nil || ok {
		t.Fatalf("FocusPeriodGet of an absent period = %v, %v; want not found", ok, err)
	}
	if n := tableCount(t, s, "focus_period"); n != 1 {
		t.Fatalf("FocusPeriodGet created a row: %d rows", n)
	}

	if _, err := s.GetOrCreateFocusPeriod("week", "2026-W39"); err != nil {
		t.Fatalf("week period: %v", err)
	}
	ps, err := s.FocusPeriods()
	if err != nil || len(ps) != 2 {
		t.Fatalf("FocusPeriods = %+v, %v; want 2", ps, err)
	}
}

func TestFocusUniqueConstraints(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1")
	if _, err := s.sql.Exec(`INSERT INTO focus_period (period_type, period_key) VALUES ('day', '2026-09-23')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sql.Exec(`INSERT INTO focus_period (period_type, period_key) VALUES ('day', '2026-09-23')`); err == nil {
		t.Errorf("a second focus_period (day, 2026-09-23) was accepted; want the UNIQUE constraint to refuse it")
	}
	ins := `INSERT INTO focus_selection (focus_period_id, repo, entity_type, entity_id, selected_at, rank_position) VALUES (1, ?, 'pr', '1', 't', 1)`
	if _, err := s.sql.Exec(ins, focusRepo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sql.Exec(ins, focusRepo); err == nil {
		t.Errorf("a second focus_selection for the same (period, entity) was accepted; want the UNIQUE constraint to refuse it")
	}
}

func TestFocusSelectionForeignKeyRejectsUnknownEntity(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	err := s.FocusLockTx(func(tx *FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-09-23")
		if err != nil {
			return err
		}
		_, err = tx.InsertSelection(focusRow(p.ID, "no-such-entity", 1))
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("insert of an unknown entity: err = %v, want a FOREIGN KEY failure", err)
	}
	if n := tableCount(t, s, "focus_selection") + tableCount(t, s, "focus_period"); n != 0 {
		t.Fatalf("the failed lock left %d rows, want none (rolled back)", n)
	}
}

func TestFocusSelectionStrikeDeletesRows(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1", "2")
	// A restored backup can hold rows in two periods; a strike deletes both.
	p1 := focusPlan(t, s, "2026-09-22", "1", "2")
	p2 := focusPlan(t, s, "2026-09-23", "1")

	rows, err := s.FocusRowsOfEntity(focusRepo, "pr", "1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows of entity 1 = %+v, %v; want 2", rows, err)
	}
	var n int
	focusLock(t, s, func(tx *FocusTx) error {
		var err error
		n, err = tx.DeleteEntityRows(focusRepo, "pr", "1")
		return err
	})
	if n != 2 {
		t.Fatalf("DeleteEntityRows removed %d rows, want 2", n)
	}
	if rows, _ := s.FocusRowsOfEntity(focusRepo, "pr", "1"); len(rows) != 0 {
		t.Fatalf("rows of entity 1 after the strike = %+v", rows)
	}
	left, err := s.FocusRowsAllPeriods()
	if err != nil || len(left) != 1 || left[0].EntityID != "2" || left[0].FocusPeriodID != p1.ID {
		t.Fatalf("remaining rows = %+v, %v; want only entity 2 in period %d", left, err, p1.ID)
	}
	// The focus_period rows stay.
	if got := tableCount(t, s, "focus_period"); got != 2 {
		t.Fatalf("focus_period rows = %d, want 2 (a strike removes selection rows only)", got)
	}
	_ = p2
}

func TestFocusPlanRowsAreOrderedByRankPositionWithTier(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1", "2", "3")
	tierO, tierS := "overdue", "started"
	var p FocusPeriod
	focusLock(t, s, func(tx *FocusTx) error {
		var err error
		if p, err = tx.GetOrCreatePeriod("day", "2026-09-23"); err != nil {
			return err
		}
		for _, r := range []FocusSelection{
			{FocusPeriodID: p.ID, Repo: focusRepo, EntityType: "pr", EntityID: "3", SelectedAt: "t", RankPosition: 3, Tier: &tierS},
			{FocusPeriodID: p.ID, Repo: focusRepo, EntityType: "pr", EntityID: "1", SelectedAt: "t", RankPosition: 1, Tier: &tierO},
			{FocusPeriodID: p.ID, Repo: focusRepo, EntityType: "pr", EntityID: "2", SelectedAt: "t", RankPosition: 2}, // hand-add: NULL tier
		} {
			if _, err := tx.InsertSelection(r); err != nil {
				return err
			}
		}
		return nil
	})
	rows, err := s.FocusPlanRows(p.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("plan rows = %+v, %v", rows, err)
	}
	if rows[0].EntityID != "1" || rows[1].EntityID != "2" || rows[2].EntityID != "3" {
		t.Fatalf("plan order = %s,%s,%s; want 1,2,3", rows[0].EntityID, rows[1].EntityID, rows[2].EntityID)
	}
	if rows[0].Tier == nil || *rows[0].Tier != "overdue" || rows[1].Tier != nil || *rows[2].Tier != "started" {
		t.Fatalf("tiers = %v,%v,%v; want overdue,nil,started", rows[0].Tier, rows[1].Tier, rows[2].Tier)
	}
	// An out-of-vocabulary tier is refused by the CHECK constraint.
	bad := "urgent"
	err = s.FocusLockTx(func(tx *FocusTx) error {
		r := focusRow(p.ID, "1", 9)
		r.Tier = &bad
		_, err := tx.InsertSelection(r)
		return err
	})
	if err == nil {
		t.Fatalf("a tier outside the vocabulary was accepted")
	}
}

func TestAnnotationIsMaxPeriodAcrossPeriods(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1", "2")

	val := func(id string) string {
		t.Helper()
		v, err := s.FocusAnnotationValue(focusRepo, "pr", id)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := val("1"); got != "none" {
		t.Fatalf("no rows: value = %q, want none", got)
	}
	p1 := focusPlan(t, s, "2026-09-21", "1", "2")
	if got := val("1"); got != "2026-09-21" {
		t.Fatalf("one row: value = %q", got)
	}
	focusPlan(t, s, "2026-09-23", "1")
	if got := val("1"); got != "2026-09-23" {
		t.Fatalf("two rows: value = %q, want the maximum 2026-09-23", got)
	}
	if got := val("2"); got != "2026-09-21" {
		t.Fatalf("entity 2: value = %q, want 2026-09-21", got)
	}

	// A deletion in the OLDER period while a newer one has a row: the maximum
	// is unchanged.
	focusLock(t, s, func(tx *FocusTx) error {
		ok, err := tx.DeleteSelection(p1.ID, focusRepo, "pr", "1")
		if !ok {
			t.Errorf("DeleteSelection found no row")
		}
		return err
	})
	if got := val("1"); got != "2026-09-23" {
		t.Fatalf("after deleting the older row: value = %q, want 2026-09-23", got)
	}
	// Deleting the newest leaves none.
	focusLock(t, s, func(tx *FocusTx) error {
		_, err := tx.DeleteEntityRows(focusRepo, "pr", "1")
		return err
	})
	if got := val("1"); got != "none" {
		t.Fatalf("after the strike: value = %q, want none", got)
	}
	// The transactional read agrees with the committed read.
	focusLock(t, s, func(tx *FocusTx) error {
		got, err := tx.AnnotationValue(focusRepo, "pr", "2")
		if got != "2026-09-21" {
			t.Errorf("tx annotation value = %q, want 2026-09-21", got)
		}
		return err
	})
}

func TestFocusDraftRejectsIdOtherThanOneAndASecondRow(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	ins := `INSERT INTO focus_draft (id, period_type, period_key, cap, made_at, body_json) VALUES (?, 'day', '2026-09-23', 5, 't', '{}')`
	if _, err := s.sql.Exec(ins, 2); err == nil {
		t.Errorf("a draft with id 2 was accepted; want the CHECK (id = 1) to refuse it")
	}
	if _, err := s.sql.Exec(ins, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sql.Exec(ins, 1); err == nil {
		t.Errorf("a second draft row was accepted; want the primary key to refuse it")
	}
	// The layer itself never writes a second row.
	for _, key := range []string{"2026-09-24", "2026-09-25"} {
		d := FocusDraft{PeriodType: "day", PeriodKey: key, Cap: 5, MadeAt: "t", BodyJSON: "{}"}
		if _, err := s.FocusDraftStore(d, key); err != nil {
			t.Fatal(err)
		}
	}
	if n := tableCount(t, s, "focus_draft"); n != 1 {
		t.Fatalf("focus_draft rows = %d, want 1", n)
	}
}

func draftOf(key, body string) FocusDraft {
	return FocusDraft{PeriodType: "day", PeriodKey: key, Cap: 5, MadeAt: "2026-09-23T07:00:00Z", BodyJSON: body}
}

func TestFocusDraftStoreCases(t *testing.T) {
	t.Run("free row", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		if _, ok, err := s.FocusDraftGet(); err != nil || ok {
			t.Fatalf("FocusDraftGet on a free row = %v, %v", ok, err)
		}
		stored, err := s.FocusDraftStore(draftOf("2026-9-23", `{"a":1}`), "2026-09-23")
		if err != nil || !stored {
			t.Fatalf("store on a free row = %v, %v; want stored", stored, err)
		}
		d, ok, err := s.FocusDraftGet()
		if err != nil || !ok || d != draftOf("2026-09-23", `{"a":1}`) {
			t.Fatalf("stored draft = %+v, %v, %v (key must be normalized)", d, ok, err)
		}
	})
	t.Run("same-period draft untouched", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		if _, err := s.FocusDraftStore(draftOf("2026-09-23", `{"first":true}`), "2026-09-23"); err != nil {
			t.Fatal(err)
		}
		stored, err := s.FocusDraftStore(draftOf("2026-09-23", `{"second":true}`), "2026-09-23")
		if err != nil || stored {
			t.Fatalf("same-period store = %v, %v; want stored=false", stored, err)
		}
		if d, _, _ := s.FocusDraftGet(); d.BodyJSON != `{"first":true}` {
			t.Fatalf("the same-period draft was overwritten: %+v", d)
		}
	})
	t.Run("earlier stored period replaced", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		if _, err := s.FocusDraftStore(draftOf("2026-09-22", `{"old":true}`), "2026-09-22"); err != nil {
			t.Fatal(err)
		}
		stored, err := s.FocusDraftStore(draftOf("2026-09-23", `{"new":true}`), "2026-09-23")
		if err != nil || !stored {
			t.Fatalf("store over an earlier draft = %v, %v; want stored", stored, err)
		}
		if d, _, _ := s.FocusDraftGet(); d.PeriodKey != "2026-09-23" || d.BodyJSON != `{"new":true}` {
			t.Fatalf("draft = %+v, want the 2026-09-23 draft", d)
		}
	})
	t.Run("later stored period untouched", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		if _, err := s.FocusDraftStore(draftOf("2026-09-24", `{"later":true}`), "2026-09-24"); err != nil {
			t.Fatal(err)
		}
		stored, err := s.FocusDraftStore(draftOf("2026-09-23", `{"earlier":true}`), "2026-09-23")
		if err != nil || stored {
			t.Fatalf("store under a later draft = %v, %v; want stored=false", stored, err)
		}
		if d, _, _ := s.FocusDraftGet(); d.PeriodKey != "2026-09-24" {
			t.Fatalf("the later draft was replaced: %+v", d)
		}
	})
	t.Run("plan row appearing between compute and insert stores nothing", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		focusSeed(t, s, "1")
		focusPlan(t, s, "2026-09-23", "1") // the lock committed after show computed
		stored, err := s.FocusDraftStore(draftOf("2026-09-23", `{}`), "2026-09-23")
		if err != nil || stored {
			t.Fatalf("store after a plan appeared = %v, %v; want stored=false", stored, err)
		}
		if n := tableCount(t, s, "focus_draft"); n != 0 {
			t.Fatalf("a draft was stored over a plan: %d rows", n)
		}
		// A plan for ANOTHER period does not block it.
		stored, err = s.FocusDraftStore(draftOf("2026-09-24", `{}`), "2026-09-24")
		if err != nil || !stored {
			t.Fatalf("store for a period with no rows = %v, %v; want stored", stored, err)
		}
	})
	t.Run("a period row with no selection rows does not block", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		if _, err := s.GetOrCreateFocusPeriod("day", "2026-09-23"); err != nil {
			t.Fatal(err)
		}
		if stored, err := s.FocusDraftStore(draftOf("2026-09-23", `{}`), "2026-09-23"); err != nil || !stored {
			t.Fatalf("store = %v, %v; want stored (the check counts rows)", stored, err)
		}
	})
	t.Run("invalid keys", func(t *testing.T) {
		s := OpenNewSchemaForTest(t)
		if _, err := s.FocusDraftStore(draftOf("bad", `{}`), "2026-09-23"); err == nil {
			t.Errorf("a bad draft key was accepted")
		}
		if _, err := s.FocusDraftStore(draftOf("2026-09-23", `{}`), "bad"); err == nil {
			t.Errorf("a bad addressed key was accepted")
		}
	})
}

func TestFocusDraftReplace(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1")
	if _, err := s.FocusDraftStore(draftOf("2026-09-23", `{"v":1}`), "2026-09-23"); err != nil {
		t.Fatal(err)
	}
	// A same-period draft is replaced (the show verb's unreadable-draft path).
	if now, err := s.FocusDraftReplace(draftOf("2026-09-23", `{"v":2}`)); err != nil || now {
		t.Fatalf("replace = %v, %v; want a write", now, err)
	}
	if d, _, _ := s.FocusDraftGet(); d.BodyJSON != `{"v":2}` {
		t.Fatalf("draft after replace = %+v", d)
	}
	// Replace also takes a free row and a different period.
	if now, err := s.FocusDraftReplace(draftOf("2026-09-22", `{"v":3}`)); err != nil || now {
		t.Fatalf("replace to another period = %v, %v", now, err)
	}
	if d, _, _ := s.FocusDraftGet(); d.PeriodKey != "2026-09-22" {
		t.Fatalf("draft after replace = %+v", d)
	}
	// A plan appearing for the period: nothing is written, planNowExists.
	focusPlan(t, s, "2026-09-23", "1")
	now, err := s.FocusDraftReplace(draftOf("2026-09-23", `{"v":4}`))
	if err != nil || !now {
		t.Fatalf("replace over a plan = %v, %v; want planNowExists", now, err)
	}
	if d, _, _ := s.FocusDraftGet(); d.BodyJSON != `{"v":3}` {
		t.Fatalf("a draft was written despite the plan: %+v", d)
	}
}

// TestLockDeletesTheDraftInTheSameTransaction pins the atomic unit of the
// lock: the focus_period row, the plan rows, the draft deletion and the run
// row commit together or not at all.
func TestLockDeletesTheDraftInTheSameTransaction(t *testing.T) {
	counts := func(s *Store) [4]int {
		return [4]int{tableCount(t, s, "focus_period"), tableCount(t, s, "focus_selection"), tableCount(t, s, "focus_draft"), tableCount(t, s, "focus_run")}
	}
	lock := func(tx *FocusTx, crash func() error) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-09-23")
		if err != nil {
			return err
		}
		if err := tx.SetPeriodCap(p.ID, 5); err != nil {
			return err
		}
		d, found, err := tx.ReadDraft()
		if err != nil || !found || d.PeriodKey != "2026-09-23" {
			t.Errorf("draft inside the transaction = %+v, %v, %v", d, found, err)
		}
		if _, err := tx.InsertSelection(focusRow(p.ID, "1", 1)); err != nil {
			return err
		}
		if _, err := tx.InsertRun(FocusRun{RunID: "01RUN", Verb: "select", FocusPeriodID: &p.ID, StartedAt: "t", Actor: "op"}); err != nil {
			return err
		}
		if err := crash(); err != nil { // between the plan write and the draft delete
			return err
		}
		if deleted, err := tx.DeleteDraftAtOrBelow("day", "2026-09-23"); err != nil || !deleted {
			t.Errorf("DeleteDraftAtOrBelow = %v, %v", deleted, err)
		}
		return nil
	}
	setup := func() *Store {
		s := OpenNewSchemaForTest(t)
		focusSeed(t, s, "1")
		if _, err := s.FocusDraftStore(draftOf("2026-09-23", `{}`), "2026-09-23"); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("an error between the writes leaves neither", func(t *testing.T) {
		s := setup()
		before := counts(s)
		boom := errors.New("injected crash")
		err := s.FocusLockTx(func(tx *FocusTx) error { return lock(tx, func() error { return boom }) })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the injected crash", err)
		}
		if after := counts(s); after != before {
			t.Fatalf("rows (period, selection, draft, run) = %v after the crash, want %v unchanged", after, before)
		}
	})
	t.Run("a panic between the writes leaves neither", func(t *testing.T) {
		s := setup()
		before := counts(s)
		func() {
			defer func() { _ = recover() }()
			_ = s.FocusLockTx(func(tx *FocusTx) error { return lock(tx, func() error { panic("injected panic") }) })
		}()
		if after := counts(s); after != before {
			t.Fatalf("rows = %v after the panic, want %v unchanged", after, before)
		}
		// The connection is released: the store still works.
		if _, _, err := s.FocusDraftGet(); err != nil {
			t.Fatalf("store unusable after a panicking lock: %v", err)
		}
	})
	t.Run("success commits all of it", func(t *testing.T) {
		s := setup()
		if err := s.FocusLockTx(func(tx *FocusTx) error { return lock(tx, func() error { return nil }) }); err != nil {
			t.Fatal(err)
		}
		if got, want := counts(s), [4]int{1, 1, 0, 1}; got != want {
			t.Fatalf("rows = %v, want %v (period, plan row, no draft, run row)", got, want)
		}
		p, _, _ := s.FocusPeriodGet("day", "2026-09-23")
		if p.Cap == nil || *p.Cap != 5 {
			t.Fatalf("period cap = %v, want 5", p.Cap)
		}
	})
}

func TestFirstLockCountsRowsNotThePeriodRow(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1")
	focusLock(t, s, func(tx *FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-09-23") // a period row, no selection row
		if err != nil {
			return err
		}
		if n, err := tx.CountRows(p.ID); err != nil || n != 0 {
			t.Errorf("CountRows with only a period row = %d, %v; want 0", n, err)
		}
		if _, err := tx.InsertSelection(focusRow(p.ID, "1", 1)); err != nil {
			return err
		}
		if n, err := tx.CountRows(p.ID); err != nil || n != 1 {
			t.Errorf("CountRows after an insert = %d, %v; want 1 (read inside the transaction)", n, err)
		}
		return nil
	})
}

func TestFocusTxOtherPeriodQueries(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1", "2", "3")
	focusPlan(t, s, "2026-09-21", "1", "2")
	focusPlan(t, s, "2026-09-22", "3")
	empty, err := s.GetOrCreateFocusPeriod("day", "2026-09-25") // no rows: never counts
	if err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetOrCreateFocusPeriod("day", "2026-09-23")
	if err != nil {
		t.Fatal(err)
	}

	focusLock(t, s, func(tx *FocusTx) error {
		if held, err := tx.OtherPeriodsHoldRows(cur.ID); err != nil || !held {
			t.Errorf("OtherPeriodsHoldRows = %v, %v; want true", held, err)
		}
		if k, err := tx.MaxOtherPeriodKey(cur.ID); err != nil || k != "2026-09-22" {
			t.Errorf("MaxOtherPeriodKey = %q, %v; want 2026-09-22 (a period with no rows does not count)", k, err)
		}
		if k, err := tx.MaxOtherPeriodKey(empty.ID); err != nil || k != "2026-09-22" {
			t.Errorf("MaxOtherPeriodKey(empty) = %q, %v", k, err)
		}
		if m, err := tx.MaxRankPosition(cur.ID); err != nil || m != 0 {
			t.Errorf("MaxRankPosition of an empty period = %d, %v; want 0", m, err)
		}
		return nil
	})
	p21, _, _ := s.FocusPeriodGet("day", "2026-09-21")
	focusLock(t, s, func(tx *FocusTx) error {
		if m, err := tx.MaxRankPosition(p21.ID); err != nil || m != 2 {
			t.Errorf("MaxRankPosition = %d, %v; want 2", m, err)
		}
		rows, err := tx.PlanRows(p21.ID)
		if err != nil || len(rows) != 2 {
			t.Errorf("PlanRows = %+v, %v", rows, err)
		}
		return nil
	})

	// The first lock of a new period deletes every other period's rows and
	// keeps their focus_period rows.
	focusLock(t, s, func(tx *FocusTx) error {
		if _, err := tx.InsertSelection(focusRow(cur.ID, "1", 1)); err != nil {
			return err
		}
		n, err := tx.DeleteRowsOfOtherPeriods(cur.ID)
		if n != 3 {
			t.Errorf("DeleteRowsOfOtherPeriods = %d, want 3", n)
		}
		return err
	})
	all, _ := s.FocusRowsAllPeriods()
	if len(all) != 1 || all[0].FocusPeriodID != cur.ID {
		t.Fatalf("rows after the replacement = %+v", all)
	}
	if n := tableCount(t, s, "focus_period"); n != 4 {
		t.Fatalf("focus_period rows = %d, want 4 (period rows stay)", n)
	}
}

func TestFocusTxDraftDeletes(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	store := func(key string) {
		t.Helper()
		if _, err := s.FocusDraftReplace(draftOf(key, `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	store("2026-09-22")
	focusLock(t, s, func(tx *FocusTx) error {
		if del, err := tx.DeleteDraftAtOrBelow("day", "2026-09-21"); err != nil || del {
			t.Errorf("deleted a later draft: %v, %v", del, err)
		}
		if del, err := tx.DeleteDraftAtOrBelow("day", "2026-09-23"); err != nil || !del {
			t.Errorf("did not delete a stale earlier draft: %v, %v", del, err)
		}
		return nil
	})
	store("2026-09-23")
	focusLock(t, s, func(tx *FocusTx) error {
		if err := tx.DeleteDraftOfPeriod("day", "2026-09-22"); err != nil { // another period's: untouched
			return err
		}
		if _, ok, _ := tx.ReadDraft(); !ok {
			t.Errorf("DeleteDraftOfPeriod removed another period's draft")
		}
		return tx.DeleteDraftOfPeriod("day", "2026-9-23")
	})
	if _, ok, _ := s.FocusDraftGet(); ok {
		t.Fatalf("the period's draft survived DeleteDraftOfPeriod")
	}
}

func TestFocusRunInsertAndFinalize(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	p, _ := s.GetOrCreateFocusPeriod("day", "2026-09-23")
	id, err := s.FocusRunInsert(FocusRun{RunID: "01A", Verb: "select", FocusPeriodID: &p.ID, StartedAt: "2026-09-23T08:00:00Z", Actor: "op"})
	if err != nil || id == 0 {
		t.Fatalf("FocusRunInsert = %d, %v", id, err)
	}
	var exit *int
	var counts string
	read := func() {
		t.Helper()
		var e *int
		if err := s.sql.QueryRow(`SELECT exit_code, counts_json FROM focus_run WHERE run_id = '01A'`).Scan(&e, &counts); err != nil {
			t.Fatal(err)
		}
		exit = e
	}
	read()
	if exit != nil || counts != "{}" {
		t.Fatalf("new run: exit_code=%v counts=%q; want NULL and {}", exit, counts)
	}
	if err := s.FocusRunFinalize("01A", 2, `{"selected":3}`); err != nil {
		t.Fatal(err)
	}
	read()
	if exit == nil || *exit != 2 || counts != `{"selected":3}` {
		t.Fatalf("finalized run: exit_code=%v counts=%q", exit, counts)
	}
	if err := s.FocusRunFinalize("missing", 0, "{}"); err == nil {
		t.Errorf("finalizing a missing run succeeded")
	}
	if _, err := s.FocusRunInsert(FocusRun{RunID: "01A", Verb: "pull", StartedAt: "t", Actor: "op"}); err == nil {
		t.Errorf("a duplicate run_id was accepted")
	}
	// A failure before locking: no period, no counts.
	if _, err := s.FocusRunInsert(FocusRun{RunID: "01B", Verb: "close", StartedAt: "t", Actor: "op"}); err != nil {
		t.Fatal(err)
	}
}

func TestFocusRunStats(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	i := func(n int) *int { return &n }
	runs := []FocusRun{
		{RunID: "r1", Verb: "select", StartedAt: "2026-09-21T08:00:00Z", ExitCode: i(0), CountsJSON: `{"selected":3,"removed":{"operator":0,"cap":0}}`},
		{RunID: "r2", Verb: "select", StartedAt: "2026-09-22T08:00:00Z", ExitCode: i(0), CountsJSON: `{"selected":0,"removed":{"operator":0,"cap":0,"dropped":0,"new_period":0}}`}, // empty
		{RunID: "r3", Verb: "select", StartedAt: "2026-09-22T09:00:00Z", ExitCode: i(0), CountsJSON: `{"selected":0,"removed":{"operator":1}}`},                                    // a strike: ok
		{RunID: "r4", Verb: "select", StartedAt: "2026-09-23T08:00:00Z", ExitCode: i(2), CountsJSON: `{"selected":2,"draft_age_seconds":7200,"drift_rows":4}`},                     // partial; newest select
		{RunID: "r5", Verb: "pull", StartedAt: "2026-09-23T09:00:00Z", ExitCode: i(0), CountsJSON: `{"selected":0,"removed":{}}`},                                                  // not a select: ok
		{RunID: "r6", Verb: "close", StartedAt: "2026-09-23T10:00:00Z", ExitCode: i(6), CountsJSON: `{}`},                                                                          // period_closed
		{RunID: "r7", Verb: "pull", StartedAt: "2026-09-23T11:00:00Z", ExitCode: i(1), CountsJSON: `{}`},                                                                           // usage
		{RunID: "r8", Verb: "repair", StartedAt: "2026-09-23T12:00:00Z", ExitCode: i(3), CountsJSON: `{}`},                                                                         // total
		{RunID: "r9", Verb: "repair", StartedAt: "2026-09-23T13:00:00Z", ExitCode: nil, CountsJSON: `{}`},                                                                          // crashed: total
		{RunID: "r10", Verb: "select", StartedAt: "2026-09-20T08:00:00Z", ExitCode: i(0), CountsJSON: `not json`},                                                                  // undecodable: ok
	}
	for _, r := range runs {
		r.Actor = "op"
		if _, err := s.FocusRunInsert(r); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.FocusRunStats()
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]string]int{
		{"select", "ok"}: 3, {"select", "empty"}: 1, {"select", "partial"}: 1,
		{"pull", "ok"}: 1, {"pull", "usage"}: 1, {"close", "period_closed"}: 1,
		{"repair", "total"}: 2,
	}
	if len(st.ByVerbOutcome) != len(want) {
		t.Errorf("ByVerbOutcome = %v, want %v", st.ByVerbOutcome, want)
	}
	for k, n := range want {
		if st.ByVerbOutcome[k] != n {
			t.Errorf("ByVerbOutcome[%v] = %d, want %d", k, st.ByVerbOutcome[k], n)
		}
	}
	if st.LastSelectAt != "2026-09-23T08:00:00Z" {
		t.Errorf("LastSelectAt = %q, want the newest select (a pull or close does not count)", st.LastSelectAt)
	}
	if st.LastLockDraftAgeSeconds == nil || *st.LastLockDraftAgeSeconds != 7200 || st.LastLockDriftRows == nil || *st.LastLockDriftRows != 4 {
		t.Errorf("last-lock values = %v, %v; want 7200 and 4", st.LastLockDraftAgeSeconds, st.LastLockDriftRows)
	}

	// No runs: empty stats, nil last-lock values.
	empty, err := OpenNewSchemaForTest(t).FocusRunStats()
	if err != nil || len(empty.ByVerbOutcome) != 0 || empty.LastSelectAt != "" || empty.LastLockDraftAgeSeconds != nil || empty.LastLockDriftRows != nil {
		t.Errorf("stats of a store with no runs = %+v, %v", empty, err)
	}
}

func TestSetAnnotationSeq(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	kvSeedEntity(t, s)
	logBefore := changeCount(t, s)

	a := kvAnn(AnnotationFocusSelected, "2026-09-23")
	seq, changed, err := s.SetAnnotationSeq(a)
	if err != nil || !changed || seq == 0 {
		t.Fatalf("first write = %d, %v, %v; want a sequence and changed", seq, changed, err)
	}
	hist, _ := s.ListEntityHistory(kvRepo, kvType, kvID, 1)
	if len(hist) != 1 || hist[0].Seq != seq || hist[0].Kinds[0] != ChangeKindAnnotationChanged {
		t.Fatalf("history = %+v, want the annotation_changed record with seq %d", hist, seq)
	}
	if changeCount(t, s) != logBefore+1 {
		t.Fatalf("log rows = %d, want %d", changeCount(t, s), logBefore+1)
	}

	// An identical rewrite (even by another actor at another time) appends
	// nothing and leaves the row as it was.
	again := a
	again.SetBy, again.SetAt, again.Origin = "agent", "2026-09-24T00:00:00Z", "other"
	seq2, changed2, err := s.SetAnnotationSeq(again)
	if err != nil || changed2 || seq2 != 0 {
		t.Fatalf("identical rewrite = %d, %v, %v; want 0, unchanged", seq2, changed2, err)
	}
	if changeCount(t, s) != logBefore+1 {
		t.Fatalf("the identical rewrite appended a record")
	}
	if got, _, _ := s.GetKVAnnotation(kvRepo, kvType, kvID, AnnotationFocusSelected); got != a {
		t.Fatalf("the identical rewrite changed the row: %+v", got)
	}

	// A different value writes and returns a later sequence.
	a.Value = "none"
	seq3, changed3, err := s.SetAnnotationSeq(a)
	if err != nil || !changed3 || seq3 <= seq {
		t.Fatalf("changed value = %d, %v, %v; want a later sequence", seq3, changed3, err)
	}

	// A fault between the write and the append rolls both back and returns no seq.
	boom := errors.New("injected failure")
	s.betweenAnnotationAndAppend = func() error { return boom }
	a.Value = "2026-09-25"
	seq4, changed4, err := s.SetAnnotationSeq(a)
	s.betweenAnnotationAndAppend = nil
	if !errors.Is(err, boom) || seq4 != 0 || changed4 {
		t.Fatalf("faulted write = %d, %v, %v", seq4, changed4, err)
	}
	if got, _, _ := s.GetKVAnnotation(kvRepo, kvType, kvID, AnnotationFocusSelected); got.Value != "none" {
		t.Fatalf("a faulted write persisted: %+v", got)
	}

	// An entity with no row errors and writes nothing.
	ghost := a
	ghost.EntityID = "ghost"
	if _, _, err := s.SetAnnotationSeq(ghost); !errors.Is(err, ErrNoEntity) {
		t.Fatalf("unknown entity: err = %v, want ErrNoEntity", err)
	}
	// An old-schema store refuses.
	if _, _, err := OpenForTest(t).SetAnnotationSeq(a); err == nil {
		t.Fatalf("SetAnnotationSeq on an old-schema store succeeded")
	}
}

// TestFocusLockTxSerializesConcurrentLocks pins the BEGIN IMMEDIATE choice:
// two locks that both read "no rows" must not both write; the second sees
// the first's rows.
func TestFocusLockTxSerializesConcurrentLocks(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	focusSeed(t, s, "1", "2")
	var wg sync.WaitGroup
	firstLocks := make(chan string, 2)
	for _, id := range []string{"1", "2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.FocusLockTx(func(tx *FocusTx) error {
				p, err := tx.GetOrCreatePeriod("day", "2026-09-23")
				if err != nil {
					return err
				}
				n, err := tx.CountRows(p.ID)
				if err != nil {
					return err
				}
				if n == 0 {
					firstLocks <- id
				}
				_, err = tx.InsertSelection(focusRow(p.ID, id, n+1))
				return err
			})
			if err != nil {
				t.Errorf("lock %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	close(firstLocks)
	if n := len(firstLocks); n != 1 {
		t.Fatalf("%d locks saw an empty period, want exactly 1 first lock", n)
	}
	rows, _ := s.FocusRowsAllPeriods()
	if len(rows) != 2 || rows[0].RankPosition == rows[1].RankPosition {
		t.Fatalf("rows = %+v, want two with distinct positions", rows)
	}
}
