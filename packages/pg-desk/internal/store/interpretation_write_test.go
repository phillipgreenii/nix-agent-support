package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests cover WriteInterpretation, StampInterpretation and
// MarkInterpretationFailed (INV-REINTERPRET-6, 7, 8 and 14) against a real
// store, never a mock: the facts-identity guard, the lock-first transaction on
// the one-connection handle, the in-transaction old-row read, the change
// record and the fault-injected rollbacks.

const (
	wRepo  = "o/r"
	wStamp = "v1/abc"
)

func wEntity(id, hash string) Entity {
	return Entity{
		Repo: wRepo, EntityType: "pr", EntityID: id, Facts: "{}", AsOf: "2026-09-16T00:00:00Z",
		ContentHash: hash,
	}
}

func wRow(id, waitingOn, panel string) Interpretation {
	r := stampRow(id)
	r.Repo = wRepo
	r.Approvals = fmt.Sprintf(`{"waiting_on":%q}`, waitingOn)
	r.Panel = panel
	return r
}

// wStore is a cut-over store whose handle carries wStamp, with one `pr`
// entity "1" (hash h1) and, when seeded, its stale interpretation row.
func wStore(t *testing.T, seed bool) *Store {
	t.Helper()
	s := OpenNewSchemaForTest(t)
	s.SetInterpStamp(wStamp)
	s.SeedEntityForTest(t, wEntity("1", "h1"))
	if seed {
		s.SetInterpStamp("old-stamp")
		s.SeedInterpretationForTest(t, wRow("1", "me", "mine"))
		s.SetInterpStamp(wStamp)
	}
	return s
}

func wGet(t *testing.T, s *Store, id string) Interpretation {
	t.Helper()
	got, found, err := s.GetInterpretation(wRepo, "pr", id)
	if err != nil || !found {
		t.Fatalf("GetInterpretation(%s) found=%v err=%v", id, found, err)
	}
	return got
}

func wChanges(t *testing.T, s *Store, entityType string) []ChangeRecord {
	t.Helper()
	rs, err := s.ListChangesAfter(entityType, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func wVersion(t *testing.T, s *Store, id string) int64 {
	t.Helper()
	e, found, err := s.GetEntity(wRepo, "pr", id)
	if err != nil || !found {
		t.Fatalf("GetEntity found=%v err=%v", found, err)
	}
	return e.Version
}

func TestWriteInterpretation_WritesWithTheHandleStampAndRecordsAChangedPair(t *testing.T) {
	s := wStore(t, true)
	vBefore := wVersion(t, s, "1")
	res, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{
		ExpectedHash: "h1", RecordChange: true, RequireStamp: true, At: "2026-09-16T01:00:00Z", Origin: "pg-desk",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written || res.Skipped || !res.DecisionChanged || !res.Recorded {
		t.Fatalf("result = %+v, want written, changed and recorded", res)
	}
	got := wGet(t, s, "1")
	if got.Panel != "theirs" || got.InterpVersion != wStamp {
		t.Fatalf("row = %+v, want the new content under %q", got, wStamp)
	}
	recs := wChanges(t, s, "pr")
	if len(recs) != 1 || !reflect.DeepEqual(recs[0].Kinds, []string{ChangeKindInterpretationChanged}) ||
		recs[0].Version != vBefore || recs[0].Origin != "pg-desk" || recs[0].At != "2026-09-16T01:00:00Z" {
		t.Fatalf("change log = %+v, want one interpretation_changed at version %d", recs, vBefore)
	}
	if v := wVersion(t, s, "1"); v != vBefore {
		t.Fatalf("entity version %d -> %d: the record must not bump it", vBefore, v)
	}
}

// INV-REINTERPRET-7: either half of the pair counts; neither means no record.
func TestWriteInterpretation_ChangedPairIsWaitingOnOrPanelAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name        string
		waitingOn   string
		panel       string
		category    string
		wantChanged bool
	}{
		{"waiting_on only", "author", "mine", "c", true},
		{"panel only", "me", "other", "c", true},
		{"neither, other column differs", "me", "mine", "other-category", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := wStore(t, true)
			row := wRow("1", tc.waitingOn, tc.panel)
			row.Category = tc.category
			res, err := s.WriteInterpretation(row, InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Written || res.DecisionChanged != tc.wantChanged || res.Recorded != tc.wantChanged {
				t.Fatalf("result = %+v, want changed=%v", res, tc.wantChanged)
			}
			if n := len(wChanges(t, s, "pr")); (n == 1) != tc.wantChanged || n > 1 {
				t.Fatalf("%d change records, want changed=%v", n, tc.wantChanged)
			}
			if got := wGet(t, s, "1"); got.Category != tc.category {
				t.Fatalf("category = %q: a changed non-decision column must still be written", got.Category)
			}
		})
	}
}

func TestWriteInterpretation_RecordIsOnlyAskedForAndOnlyForAPR(t *testing.T) {
	t.Run("not asked", func(t *testing.T) {
		s := wStore(t, true)
		res, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1"})
		if err != nil || !res.Written || !res.DecisionChanged || res.Recorded {
			t.Fatalf("result = %+v err=%v, want written, changed and not recorded", res, err)
		}
		if n := len(wChanges(t, s, "pr")); n != 0 {
			t.Fatalf("%d change records, want none", n)
		}
	})
	for _, typ := range []string{"issue", "thread"} {
		t.Run("not for "+typ, func(t *testing.T) {
			s := wStore(t, false)
			e := wEntity("x", "hx")
			e.EntityType = typ
			s.SeedEntityForTest(t, e)
			old := wRow("x", "me", "mine")
			old.EntityType = typ
			s.SeedInterpretationForTest(t, old)
			row := wRow("x", "author", "theirs")
			row.EntityType = typ
			res, err := s.WriteInterpretation(row, InterpretationWriteOptions{ExpectedHash: "hx", RecordChange: true})
			if err != nil || !res.Written || !res.DecisionChanged || res.Recorded {
				t.Fatalf("result = %+v err=%v, want written, changed and not recorded", res, err)
			}
			if n := len(wChanges(t, s, typ)); n != 0 {
				t.Fatalf("%d change records for %s, want none", n, typ)
			}
		})
	}
}

func TestWriteInterpretation_FirstRowHasNoPriorDecisionAndRecordsNothing(t *testing.T) {
	s := wStore(t, false)
	res, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true})
	if err != nil || !res.Written || res.DecisionChanged || res.Recorded {
		t.Fatalf("result = %+v err=%v, want written with no decision change", res, err)
	}
	if got := wGet(t, s, "1"); got.InterpVersion != wStamp {
		t.Fatalf("stamp = %q", got.InterpVersion)
	}
}

// INV-REINTERPRET-6: skipped if and only if the stored facts differ.
func TestWriteInterpretation_SkipsOnDifferentFactsAndWritesOnTheSameFactsAtAHigherVersion(t *testing.T) {
	t.Run("different facts: skipped, row and log untouched", func(t *testing.T) {
		s := wStore(t, true)
		before := dumpDB(t, s)
		res, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "stale-hash", RecordChange: true})
		if err != nil || res.Written || !res.Skipped {
			t.Fatalf("result = %+v err=%v, want skipped", res, err)
		}
		if after := dumpDB(t, s); after != before {
			t.Fatal("a skipped write changed the store")
		}
	})
	t.Run("same facts, higher version: written", func(t *testing.T) {
		s := wStore(t, true)
		// A version bump that keeps the facts (the deactivation, reconcile and
		// removed-row writers): hash stays h1.
		for i := 0; i < 3; i++ {
			e, _, _ := s.GetEntity(wRepo, "pr", "1")
			if _, err := s.WriteEntityStateWithLog(Entity{
				Repo: wRepo, EntityType: "pr", EntityID: "1", Facts: e.Facts, AsOf: e.AsOf, ContentHash: "h1",
			}, e.Version, "2026-09-16T02:00:00Z", false, nil, "pg-desk", "2026-09-16T02:00:00Z"); err != nil {
				t.Fatal(err)
			}
		}
		if v := wVersion(t, s, "1"); v < 3 {
			t.Fatalf("precondition: version did not move (%d)", v)
		}
		res, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1"})
		if err != nil || !res.Written || res.Skipped {
			t.Fatalf("result = %+v err=%v, want written: a version bump alone must not skip", res, err)
		}
		if got := wGet(t, s, "1"); got.Panel != "theirs" {
			t.Fatalf("row = %+v, want the new content", got)
		}
	})
}

func TestWriteInterpretation_MissingEntityIsErrNoEntityAndWritesNothing(t *testing.T) {
	s := wStore(t, true)
	before := dumpDB(t, s)
	_, err := s.WriteInterpretation(wRow("gone", "me", "mine"), InterpretationWriteOptions{ExpectedHash: "h1"})
	if !errors.Is(err, ErrNoEntity) {
		t.Fatalf("err = %v, want ErrNoEntity", err)
	}
	if dumpDB(t, s) != before {
		t.Fatal("a refused write changed the store")
	}
}

func TestWriteInterpretation_StampRules(t *testing.T) {
	t.Run("required and absent", func(t *testing.T) {
		s := wStore(t, true)
		s.SetInterpStamp("")
		before := dumpDB(t, s)
		_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1", RequireStamp: true})
		if !errors.Is(err, ErrStampRequired) {
			t.Fatalf("err = %v, want ErrStampRequired", err)
		}
		if dumpDB(t, s) != before {
			t.Fatal("a refused write changed the store")
		}
	})
	t.Run("not required and absent writes NULL", func(t *testing.T) {
		s := wStore(t, true)
		s.SetInterpStamp("")
		if _, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1"}); err != nil {
			t.Fatal(err)
		}
		if got := wGet(t, s, "1"); got.InterpVersion != "" {
			t.Fatalf("stamp = %q, want NULL (read back empty)", got.InterpVersion)
		}
	})
	t.Run("column absent on the table", func(t *testing.T) {
		s := wStore(t, false)
		dropStampColumn(t, s)
		if _, err := s.WriteInterpretation(wRow("1", "me", "mine"), InterpretationWriteOptions{ExpectedHash: "h1"}); err != nil {
			t.Fatalf("write without the stamp column: %v", err)
		}
	})
}

func TestWriteInterpretation_SchemaOneIsRefusedBeforeBeginAndNothingIsWritten(t *testing.T) {
	s := OpenSchema1ForTest(t) // version 1
	s.SetInterpStamp(wStamp)
	begun := false
	s.SetInterpretationWriteHook(func(InterpretationWriteStep) error { begun = true; return nil })
	_, err := s.WriteInterpretation(wRow("1", "me", "mine"), InterpretationWriteOptions{ExpectedHash: "h1"})
	if !errors.Is(err, ErrOldSchema) || !strings.Contains(err.Error(), "migrate --cutover") {
		t.Fatalf("err = %v, want the cutover refusal", err)
	}
	if begun {
		t.Fatal("the schema check ran after Begin")
	}
	for name, call := range map[string]func() error{
		"StampInterpretation": func() error {
			_, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "1"}, "h1")
			return err
		},
		"MarkInterpretationFailed": func() error { return s.MarkInterpretationFailed(InterpretationKey{wRepo, "pr", "1"}, wStamp) },
	} {
		if err := call(); !errors.Is(err, ErrOldSchema) {
			t.Errorf("%s on schema 1: %v, want ErrOldSchema", name, err)
		}
	}
	if begun {
		t.Fatal("a refused call began a transaction")
	}
}

// The pool holds ONE connection: reading the old row through the handle
// inside the transaction would deadlock. The write must finish, with an old
// row present and a record appended.
func TestWriteInterpretation_FinishesOnTheOneConnectionHandle(t *testing.T) {
	s := wStore(t, true)
	done := make(chan error, 1)
	go func() {
		_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WriteInterpretation did not finish: a read through the handle inside the transaction deadlocks the one-connection pool")
	}
}

// Fault injection: an error at any step leaves the store exactly as it was
// (nothing half-written), and the same call succeeds once the fault is gone.
func TestWriteInterpretation_FaultAtAnyStepRollsBackEverything(t *testing.T) {
	steps := []InterpretationWriteStep{StepBegin, StepLock, StepReadOld, StepUpsert, StepAppend, StepCommit}
	for _, step := range steps {
		t.Run(string(step), func(t *testing.T) {
			s := wStore(t, true)
			before := dumpDB(t, s)
			injected := errors.New("injected " + string(step))
			s.SetInterpretationWriteHook(func(got InterpretationWriteStep) error {
				if got == step {
					return injected
				}
				return nil
			})
			_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true})
			if !errors.Is(err, injected) {
				t.Fatalf("err = %v, want the injected fault", err)
			}
			if dumpDB(t, s) != before {
				t.Fatal("a fault left the store half-written")
			}
			s.SetInterpretationWriteHook(nil)
			if res, err := s.WriteInterpretation(wRow("1", "author", "theirs"), InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true}); err != nil || !res.Written {
				t.Fatalf("retry after the fault: %+v %v", res, err)
			}
		})
	}
}

// A real second handle commits different facts between this handle's read of
// the hash and its write (injected at the read-old step, which runs inside the
// transaction after the hash check): the lock-first step means the second
// writer waits its turn, never SQLITE_BUSY, and a LATER write with the old hash
// is skipped.
func TestWriteInterpretation_SecondHandleNeverSeesBusyAndAnOlderHashIsSkipped(t *testing.T) {
	s := wStore(t, true)
	other, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := s
			if i == 1 {
				h = other
				h.SetInterpStamp(wStamp)
			}
			for n := 0; n < 20; n++ {
				if _, err := h.WriteInterpretation(wRow("1", "author", fmt.Sprintf("p%d-%d", i, n)), InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true}); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write: %v", err)
	}
	// Different facts committed by the second handle: the first handle's write
	// with the old hash is now skipped and the newer row stays.
	e, _, _ := other.GetEntity(wRepo, "pr", "1")
	if _, err := other.WriteEntityStateWithLog(Entity{
		Repo: wRepo, EntityType: "pr", EntityID: "1", Facts: `{"n":2}`, AsOf: e.AsOf, ContentHash: "h2",
	}, e.Version, "2026-09-16T03:00:00Z", true, nil, "pg-desk", "2026-09-16T03:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.WriteInterpretation(wRow("1", "newer", "newer-panel"), InterpretationWriteOptions{ExpectedHash: "h2"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.WriteInterpretation(wRow("1", "older", "older-panel"), InterpretationWriteOptions{ExpectedHash: "h1"})
	if err != nil || !res.Skipped {
		t.Fatalf("late write: %+v %v, want skipped", res, err)
	}
	if got := wGet(t, other, "1"); got.Panel != "newer-panel" {
		t.Fatalf("panel = %q, want the newer row kept", got.Panel)
	}
}

func TestStampInterpretation_UpdatesOnlyTheStamp(t *testing.T) {
	s := wStore(t, true)
	before := wGet(t, s, "1")
	ok, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "1"}, "h1")
	if err != nil || !ok {
		t.Fatalf("stamped=%v err=%v", ok, err)
	}
	after := wGet(t, s, "1")
	want := before
	want.InterpVersion = wStamp
	if after != want {
		t.Fatalf("row = %+v, want only the stamp changed from %+v", after, before)
	}
	if n := len(wChanges(t, s, "pr")); n != 0 {
		t.Fatalf("%d change records, want none", n)
	}
}

func TestStampInterpretation_SkipsDifferentFactsAndRefusesBadCalls(t *testing.T) {
	s := wStore(t, true)
	ok, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "1"}, "other-hash")
	if err != nil || ok {
		t.Fatalf("stamped=%v err=%v, want a clean skip", ok, err)
	}
	if got := wGet(t, s, "1"); got.InterpVersion != "old-stamp" {
		t.Fatalf("stamp = %q, want the skipped row untouched", got.InterpVersion)
	}
	if _, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "gone"}, "h1"); !errors.Is(err, ErrNoEntity) {
		t.Fatalf("missing entity: %v, want ErrNoEntity", err)
	}
	s.SeedEntityForTest(t, wEntity("2", "h2")) // entity without an interpretation row
	if _, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "2"}, "h2"); !errors.Is(err, ErrNoInterpretation) {
		t.Fatalf("missing row: %v, want ErrNoInterpretation", err)
	}
	s.SetInterpStamp("")
	if _, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "1"}, "h1"); !errors.Is(err, ErrStampRequired) {
		t.Fatalf("no stamp: %v, want ErrStampRequired", err)
	}
}

func TestStampInterpretation_FaultRollsBack(t *testing.T) {
	for _, step := range []InterpretationWriteStep{StepBegin, StepLock, StepUpsert, StepCommit} {
		t.Run(string(step), func(t *testing.T) {
			s := wStore(t, true)
			before := dumpDB(t, s)
			injected := errors.New("injected")
			s.SetInterpretationWriteHook(func(got InterpretationWriteStep) error {
				if got == step {
					return injected
				}
				return nil
			})
			if _, err := s.StampInterpretation(InterpretationKey{wRepo, "pr", "1"}, "h1"); !errors.Is(err, injected) {
				t.Fatalf("err = %v", err)
			}
			if dumpDB(t, s) != before {
				t.Fatal("a fault left the store half-written")
			}
		})
	}
}

// INV-REINTERPRET-8: the marker works with no entity, keeps content, and the
// behind query then skips the row under that stamp.
func TestMarkInterpretationFailed_NeedsNoEntityKeepsContentAndHidesTheRowFromTheBehindQuery(t *testing.T) {
	s := wStore(t, true)
	execStamp(t, s, `DELETE FROM entity WHERE entity_id = '1'`)
	before := wGet(t, s, "1")
	if err := s.MarkInterpretationFailed(InterpretationKey{wRepo, "pr", "1"}, wStamp); err != nil {
		t.Fatal(err)
	}
	after := wGet(t, s, "1")
	want := before
	want.InterpVersion = wStamp + FailedStampSuffix
	if after != want {
		t.Fatalf("row = %+v, want only the failed marker changed from %+v", after, before)
	}
	keys, known, err := s.InterpretationsBehind(wRepo, 0)
	if err != nil || !known || len(keys) != 0 {
		t.Fatalf("behind = %v known=%v err=%v, want the failed row skipped", keys, known, err)
	}
	// A new stamp makes it eligible again.
	s.SetInterpStamp("v2/def")
	if keys, _, _ := s.InterpretationsBehind(wRepo, 0); len(keys) != 1 {
		t.Fatalf("behind under a new stamp = %v, want the row eligible again", keys)
	}
}

func TestMarkInterpretationFailed_RefusesBadCallsAndRollsBackOnFault(t *testing.T) {
	s := wStore(t, true)
	if err := s.MarkInterpretationFailed(InterpretationKey{wRepo, "pr", "nope"}, wStamp); !errors.Is(err, ErrNoInterpretation) {
		t.Fatalf("missing row: %v, want ErrNoInterpretation", err)
	}
	if err := s.MarkInterpretationFailed(InterpretationKey{wRepo, "pr", "1"}, ""); !errors.Is(err, ErrStampRequired) {
		t.Fatalf("empty stamp: %v, want ErrStampRequired", err)
	}
	for _, step := range []InterpretationWriteStep{StepBegin, StepUpsert, StepCommit} {
		before := dumpDB(t, s)
		injected := errors.New("injected")
		s.SetInterpretationWriteHook(func(got InterpretationWriteStep) error {
			if got == step {
				return injected
			}
			return nil
		})
		if err := s.MarkInterpretationFailed(InterpretationKey{wRepo, "pr", "1"}, wStamp); !errors.Is(err, injected) {
			t.Fatalf("%s: err = %v", step, err)
		}
		if dumpDB(t, s) != before {
			t.Fatalf("%s: a fault left the store half-written", step)
		}
	}
}

func TestSeedHelpersRefuseASchemaOneStore(t *testing.T) {
	s := OpenSchema1ForTest(t)
	ft := &fakeSeedT{}
	s.SeedEntityForTest(ft, wEntity("1", "h"))
	s.SeedInterpretationForTest(ft, wRow("1", "me", "mine"))
	if ft.fatals != 2 {
		t.Fatalf("fatals = %d, want both seeds refused on schema 1", ft.fatals)
	}
}

type fakeSeedT struct{ fatals int }

func (f *fakeSeedT) Helper()               {}
func (f *fakeSeedT) Fatalf(string, ...any) { f.fatals++ }

// Real SQL failures (a trigger that aborts the statement) at each statement of
// the write: the error is wrapped and returned, and nothing stays half-written.
func TestInterpretationWrites_StatementFailuresRollBackAndAreReturned(t *testing.T) {
	opts := InterpretationWriteOptions{ExpectedHash: "h1", RecordChange: true}
	key := InterpretationKey{wRepo, "pr", "1"}
	abort := func(ev, table string) string {
		return `CREATE TRIGGER inject_fail BEFORE ` + ev + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'injected sql failure'); END`
	}
	cases := []struct {
		name string
		ddl  string
		call func(s *Store) error
	}{
		{"write: lock", abort("UPDATE", "entity"), func(s *Store) error {
			_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), opts)
			return err
		}},
		{"write: upsert", abort("INSERT", "interpretation"), func(s *Store) error {
			_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), opts)
			return err
		}},
		{"write: append", abort("INSERT", "change_log"), func(s *Store) error {
			_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), opts)
			return err
		}},
		{"write: read old row", `ALTER TABLE interpretation RENAME COLUMN approvals TO approvals_x`, func(s *Store) error {
			_, err := s.WriteInterpretation(wRow("1", "author", "theirs"), opts)
			return err
		}},
		{"stamp: lock", abort("UPDATE", "entity"), func(s *Store) error {
			_, err := s.StampInterpretation(key, "h1")
			return err
		}},
		{"stamp: update", abort("UPDATE", "interpretation"), func(s *Store) error {
			_, err := s.StampInterpretation(key, "h1")
			return err
		}},
		{"mark: update", abort("UPDATE", "interpretation"), func(s *Store) error {
			return s.MarkInterpretationFailed(key, wStamp)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := wStore(t, true)
			execStamp(t, s, tc.ddl)
			before := dumpDB(t, s)
			if err := tc.call(s); err == nil {
				t.Fatal("err = nil, want the statement failure")
			}
			if dumpDB(t, s) != before {
				t.Fatal("a failed statement left the store half-written")
			}
		})
	}
}

func TestWriteInterpretation_ColumnProbeFailureIsReturned(t *testing.T) {
	s := wStore(t, true)
	s.faultTableColumns = func(string) error { return errors.New("injected probe failure") }
	if _, err := s.WriteInterpretation(wRow("1", "me", "mine"), InterpretationWriteOptions{ExpectedHash: "h1"}); err == nil {
		t.Fatal("err = nil, want the probe failure")
	}
}

func TestSeedHelpers_SeedBothRowsAndFailTheTestOnAStoreError(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	s.SeedEntityAndInterpretationForTest(t, wEntity("5", "h5"), wRow("5", "me", "mine"))
	if got := wGet(t, s, "5"); got.Panel != "mine" {
		t.Fatalf("row = %+v", got)
	}
	execStamp(t, s, `CREATE TRIGGER inject_fail BEFORE INSERT ON entity BEGIN SELECT RAISE(ABORT, 'x'); END`)
	execStamp(t, s, `CREATE TRIGGER inject_fail2 BEFORE INSERT ON interpretation BEGIN SELECT RAISE(ABORT, 'x'); END`)
	ft := &fakeSeedT{}
	s.SeedEntityForTest(ft, wEntity("6", "h"))
	s.SeedInterpretationForTest(ft, wRow("6", "me", "mine"))
	if ft.fatals != 2 {
		t.Fatalf("fatals = %d, want both store errors reported", ft.fatals)
	}
}
