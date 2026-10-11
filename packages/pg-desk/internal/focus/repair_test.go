package focus

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func lockRows(t *testing.T, f *fixture, period string, keys ...Key) {
	t.Helper()
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", period)
		if err != nil {
			return err
		}
		for i, k := range keys {
			if _, err := tx.InsertSelection(store.FocusSelection{FocusPeriodID: p.ID, Repo: testRepo, EntityType: k.Type, EntityID: k.ID, SelectedAt: fixtureAt, RankPosition: i + 1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func setFocusAnnotation(t *testing.T, f *fixture, k Key, v string) {
	t.Helper()
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: k.Type, EntityID: k.ID, Key: store.AnnotationFocusSelected,
		Value: v, Origin: "pg-desk", SetBy: "me", SetAt: fixtureAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRepairScansEveryAnnotatedEntityAndEveryPlanRowEntity(t *testing.T) {
	f := newFixture(t)
	missing := f.pr("1", prSpec{ownership: "mine"})  // row, no annotation
	mismatch := f.pr("2", prSpec{ownership: "mine"}) // row, wrong annotation
	stale := f.pr("3", prSpec{ownership: "mine"})    // annotation, no row
	good := f.pr("4", prSpec{ownership: "mine"})     // row and matching annotation
	untouched := f.pr("5", prSpec{ownership: "mine"})
	lockRows(t, f, "2026-10-09", missing, mismatch, good)
	lockRows(t, f, lockToday, mismatch) // max period key is the later one
	setFocusAnnotation(t, f, mismatch, "2026-10-09")
	setFocusAnnotation(t, f, stale, "2026-10-08")
	setFocusAnnotation(t, f, good, "2026-10-09")
	d := lockDeps(t, f)

	dry, err := Repair(context.Background(), d, true)
	if err != nil || len(dry.Written) != 3 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	for _, w := range dry.Written {
		if w.Outcome != AnnotationWouldWrite {
			t.Errorf("dry-run outcome = %q", w.Outcome)
		}
	}
	if v, found, _ := f.st.GetKVAnnotation(testRepo, "pr", "1", store.AnnotationFocusSelected); found {
		t.Fatalf("a dry run wrote an annotation: %v", v)
	}
	res, err := Repair(context.Background(), d, false)
	if err != nil || len(res.Written) != 3 {
		t.Fatalf("repair = %+v, %v", res, err)
	}
	want := map[Key]string{missing: "2026-10-09", mismatch: lockToday, stale: "none", good: "2026-10-09"}
	for k, v := range want {
		a, found, _ := f.st.GetKVAnnotation(testRepo, k.Type, k.ID, store.AnnotationFocusSelected)
		if !found || a.Value != v {
			t.Errorf("annotation of %v = %q (found %v), want %q", k, a.Value, found, v)
		}
	}
	if _, found, _ := f.st.GetKVAnnotation(testRepo, untouched.Type, untouched.ID, store.AnnotationFocusSelected); found {
		t.Errorf("repair annotated an entity with no row and no annotation")
	}
	again, err := Repair(context.Background(), d, false)
	if err != nil || len(again.Written) != 0 {
		t.Errorf("a second repair wrote %+v, %v", again, err)
	}
}

func TestRepairIgnoresAClosedPeriodAndMendsAnnotationWithoutRow(t *testing.T) {
	f := newFixture(t)
	a, orphan := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	lockRows(t, f, "2026-10-09", a)
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-10-09")
		if err != nil {
			return err
		}
		return tx.ClosePeriod(p.ID, fixtureAt, "done")
	}); err != nil {
		t.Fatal(err)
	}
	setFocusAnnotation(t, f, orphan, "2026-10-09")
	res, err := Repair(context.Background(), lockDeps(t, f), false)
	if err != nil || len(res.Written) != 2 {
		t.Fatalf("repair = %+v, %v", res, err)
	}
	if v, _ := f.st.FocusAnnotationValue(testRepo, "pr", "2"); v != "none" {
		t.Errorf("value of the row-less entity = %q", v)
	}
	if a, _, _ := f.st.GetKVAnnotation(testRepo, "pr", "2", store.AnnotationFocusSelected); a.Value != "none" {
		t.Errorf("annotation without a row = %q, want none", a.Value)
	}
}
