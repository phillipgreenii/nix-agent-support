package changes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// failEntityUpdates installs a SQLite trigger on the store's own file that
// aborts every UPDATE of the entity table: a fault injected into the REAL
// store through a second connection, so the store's own write path takes its
// own error exit.
func failEntityUpdates(t *testing.T, st *store.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), `CREATE TRIGGER inject_fail_entity_update BEFORE UPDATE ON entity BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}

// bumpVersion is a competing write: it bumps the entity's version.
func bumpVersion(t *testing.T, st *store.Store, id string) {
	t.Helper()
	row, found, err := st.GetEntity("o/r", "issue", id)
	if err != nil || !found {
		t.Fatalf("competing write read: found=%v err=%v", found, err)
	}
	if _, err := st.WriteEntityStateWithLog(row, row.Version, row.HydratedAt, true, []string{"reconcile"}, "pg-desk", "2026-09-16T00:00:00Z"); err != nil {
		t.Fatalf("competing write: %v", err)
	}
}

func TestDeactivateIf_ReadFailureIsReturned(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.deactivateIf("issue", "bd-1", nil, nil); err == nil {
		t.Fatal("expected the store read failure")
	}
}

func TestDeactivateIf_AbsentInactiveAndGuardedRowsAreLeftAlone(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	if err := e.deactivateIf("issue", "bd-none", []string{"removed"}, nil); err != nil {
		t.Fatalf("absent entity: %v", err)
	}
	seedKnown(t, st, "bd-1", "F1")
	declines := func(store.Entity) bool { return false }
	if err := e.deactivateIf("issue", "bd-1", []string{"removed"}, declines); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := st.GetEntity("o/r", "issue", "bd-1"); row.Inactive {
		t.Fatal("a declining guard still deactivated the row")
	}
	before := kindsOfRows(t, st, "bd-1")

	// Deactivated once, a second call is a no-op: no second record.
	if err := e.deactivateIf("issue", "bd-1", []string{"removed"}, nil); err != nil {
		t.Fatal(err)
	}
	afterFirst := kindsOfRows(t, st, "bd-1")
	if err := e.deactivateIf("issue", "bd-1", []string{"removed"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := kindsOfRows(t, st, "bd-1"); !reflect.DeepEqual(got, afterFirst) || len(afterFirst) != len(before)+1 {
		t.Fatalf("history before=%v afterFirst=%v afterSecond=%v, want exactly one removed record", before, afterFirst, got)
	}
}

func TestDeactivateIf_NonConflictWriteFailureIsReturnedWithoutRetry(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	failEntityUpdates(t, st)
	attempts := 0
	e.beforeCASWrite = func(int) { attempts++ }

	err := e.deactivateIf("issue", "bd-1", []string{"removed"}, nil)
	if err == nil || !strings.Contains(err.Error(), "injected failure") || errors.Is(err, store.ErrVersionConflict) {
		t.Fatalf("err = %v, want the store's own failure", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1: only a version conflict retries", attempts)
	}
	if row, _, _ := st.GetEntity("o/r", "issue", "bd-1"); row.Inactive {
		t.Fatal("a failed write deactivated the row")
	}
}

func TestDeactivateIf_LostRaceRetriesThenGivesUp(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	e.beforeCASWrite = func(a int) {
		if a == 0 {
			bumpVersion(t, st, "bd-1")
		}
	}
	if err := e.deactivateIf("issue", "bd-1", []string{"removed"}, nil); err != nil {
		t.Fatalf("one lost race must be retried: %v", err)
	}
	if row, _, _ := st.GetEntity("o/r", "issue", "bd-1"); !row.Inactive {
		t.Fatal("the retry did not deactivate")
	}

	seedKnown(t, st, "bd-2", "F1")
	calls := 0
	e.beforeCASWrite = func(int) { calls++; bumpVersion(t, st, "bd-2") }
	err := e.deactivateIf("issue", "bd-2", []string{"removed"}, nil)
	if !errors.Is(err, store.ErrVersionConflict) || !strings.Contains(err.Error(), "still losing the version race after 5 attempts") {
		t.Fatalf("err = %v, want the bounded version-race error", err)
	}
	if calls != casAttempts {
		t.Fatalf("attempts = %d, want %d", calls, casAttempts)
	}
	if row, _, _ := st.GetEntity("o/r", "issue", "bd-2"); row.Inactive {
		t.Fatal("the entity was deactivated although every write lost the race")
	}
}

func TestReconcileOne_ReadFailureIsReturned(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileOne("issue", "bd-1"); err == nil {
		t.Fatal("expected the store read failure")
	}
}

func TestReconcileOne_AppendsOneLocalReconcileRecordAndKeepsTheSnapshot(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	before, _, _ := st.GetEntity("o/r", "issue", "bd-1")
	rowsBefore := kindsOfRows(t, st, "bd-1")

	if err := e.reconcileOne("issue", "bd-1"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := st.GetEntity("o/r", "issue", "bd-1")
	if after.Version != before.Version+1 || after.Facts != before.Facts || after.HydratedAt != before.HydratedAt || after.Inactive {
		t.Fatalf("before=%+v after=%+v, want a version bump with snapshot and hydrated_at kept", before, after)
	}
	rows := kindsOfRows(t, st, "bd-1")
	if len(rows) != len(rowsBefore)+1 || !reflect.DeepEqual(rows[0], []string{"reconcile"}) {
		t.Fatalf("history = %v, want one more row, newest the reconcile record", rows)
	}
}

func TestReconcileOne_AbsentAndInactiveEntitiesAreLeftAlone(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	if err := e.reconcileOne("issue", "bd-none"); err != nil {
		t.Fatalf("absent entity: %v", err)
	}
	seedKnown(t, st, "bd-1", "F1")
	if err := e.deactivate("issue", "bd-1", nil); err != nil {
		t.Fatal(err)
	}
	before := kindsOfRows(t, st, "bd-1")
	if err := e.reconcileOne("issue", "bd-1"); err != nil {
		t.Fatal(err)
	}
	if got := kindsOfRows(t, st, "bd-1"); !reflect.DeepEqual(got, before) {
		t.Fatalf("history changed for an inactive entity: before=%v after=%v", before, got)
	}
}

func TestReconcileOne_NonConflictWriteFailureIsReturnedWithoutRetry(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	failEntityUpdates(t, st)
	attempts := 0
	e.beforeCASWrite = func(int) { attempts++ }
	err := e.reconcileOne("issue", "bd-1")
	if err == nil || !strings.Contains(err.Error(), "injected failure") || errors.Is(err, store.ErrVersionConflict) {
		t.Fatalf("err = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestReconcileOne_LostRaceRetriesThenGivesUp(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	seedKnown(t, st, "bd-1", "F1")
	e.beforeCASWrite = func(a int) {
		if a == 0 {
			bumpVersion(t, st, "bd-1")
		}
	}
	if err := e.reconcileOne("issue", "bd-1"); err != nil {
		t.Fatalf("one lost race must be retried: %v", err)
	}

	calls := 0
	e.beforeCASWrite = func(int) { calls++; bumpVersion(t, st, "bd-1") }
	err := e.reconcileOne("issue", "bd-1")
	if !errors.Is(err, store.ErrVersionConflict) || calls != casAttempts {
		t.Fatalf("err = %v calls = %d, want the bounded version-race error after %d attempts", err, calls, casAttempts)
	}
}
