package store

import (
	"errors"
	"reflect"
	"testing"
)

const (
	kvRepo = "acme/widgets"
	kvType = "pr"
	kvID   = "acme/widgets#7"
)

func kvSeedEntity(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.WriteEntityWithLog(clEntity("a"), 0, []string{"created"}, "sync", "2026-09-10T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func kvAnn(key, value string) KVAnnotation {
	return KVAnnotation{
		Repo: kvRepo, EntityType: kvType, EntityID: kvID, Key: key, Value: value,
		Origin: "pg-desk", SetBy: "operator", SetAt: "2026-09-12T00:00:00Z",
	}
}

func annotationCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.sql.QueryRow(`SELECT COUNT(*) FROM annotation`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnnotationChangedAppendedInSameTransaction(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	kvSeedEntity(t, s)
	logBefore := changeCount(t, s)

	if err := s.SetAnnotation(kvAnn(AnnotationWIP, "true")); err != nil {
		t.Fatal(err)
	}
	hist, err := s.ListEntityHistory(kvRepo, kvType, kvID, 1)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	h := hist[0]
	if !reflect.DeepEqual(h.Kinds, []string{"annotation_changed"}) || h.Version != 1 || h.Origin != "pg-desk" || h.At != "2026-09-12T00:00:00Z" {
		t.Fatalf("annotation record = %+v", h)
	}
	got, _, _ := s.GetEntity(kvRepo, kvType, kvID)
	if got.Version != 1 {
		t.Fatalf("entity version = %d, want unchanged 1", got.Version)
	}
	if changeCount(t, s) != logBefore+1 {
		t.Fatalf("log rows = %d, want %d", changeCount(t, s), logBefore+1)
	}

	// Fault between the annotation write and the log append: neither persists.
	boom := errors.New("injected failure")
	s.betweenAnnotationAndAppend = func() error { return boom }
	err = s.SetAnnotation(kvAnn(AnnotationWIP, "false"))
	s.betweenAnnotationAndAppend = nil
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	a, _, _ := s.GetKVAnnotation(kvRepo, kvType, kvID, AnnotationWIP)
	if a.Value != "true" || changeCount(t, s) != logBefore+1 {
		t.Fatalf("after failed set: value %q, log rows %d (want true, %d)", a.Value, changeCount(t, s), logBefore+1)
	}

	// Same for delete.
	s.betweenAnnotationAndAppend = func() error { return boom }
	_, err = s.DeleteAnnotation(kvRepo, kvType, kvID, AnnotationWIP, "pg-desk", "t")
	s.betweenAnnotationAndAppend = nil
	if !errors.Is(err, boom) {
		t.Fatalf("delete err = %v", err)
	}
	if _, found, _ := s.GetKVAnnotation(kvRepo, kvType, kvID, AnnotationWIP); !found || changeCount(t, s) != logBefore+1 {
		t.Fatalf("failed delete must leave annotation and log untouched")
	}
}

func TestAnnotationAtVersionZeroEntityAndDelete(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertEntity(clEntity("a")); err != nil { // version 0 (never rehydrated)
		t.Fatal(err)
	}
	if err := s.SetAnnotation(kvAnn(AnnotationForceReview, "true")); err != nil {
		t.Fatal(err)
	}
	hist, _ := s.ListEntityHistory(kvRepo, kvType, kvID, 0)
	if len(hist) != 1 || hist[0].Version != 0 {
		t.Fatalf("history = %+v, want one record at version 0", hist)
	}
	removed, err := s.DeleteAnnotation(kvRepo, kvType, kvID, AnnotationForceReview, "pg-desk", "t2")
	if err != nil || !removed {
		t.Fatalf("delete = (%v, %v)", removed, err)
	}
	// Deleting an absent key is a no-op with no log record.
	removed, err = s.DeleteAnnotation(kvRepo, kvType, kvID, AnnotationForceReview, "pg-desk", "t3")
	if err != nil || removed {
		t.Fatalf("second delete = (%v, %v)", removed, err)
	}
	if hist, _ := s.ListEntityHistory(kvRepo, kvType, kvID, 0); len(hist) != 2 {
		t.Fatalf("history len = %d, want 2", len(hist))
	}
}

func TestAnnotateEntityWithoutRowErrorsAndWritesNothing(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	err := s.SetAnnotation(kvAnn(AnnotationWIP, "true"))
	if !errors.Is(err, ErrNoEntity) {
		t.Fatalf("err = %v, want ErrNoEntity", err)
	}
	if _, err := s.DeleteAnnotation(kvRepo, kvType, kvID, AnnotationWIP, "o", "t"); !errors.Is(err, ErrNoEntity) {
		t.Fatalf("delete err = %v, want ErrNoEntity", err)
	}
	if annotationCount(t, s) != 0 || changeCount(t, s) != 0 {
		t.Fatalf("wrote annotations=%d log=%d, want 0/0", annotationCount(t, s), changeCount(t, s))
	}
}

func TestReservedKeysRoundTrip(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	kvSeedEntity(t, s)
	want := map[string]string{
		AnnotationHidden:             `{"value":true,"reason":"waiting"}`,
		AnnotationWIP:                "false",
		KeyDisposition("c1"):         DispositionWillFix,
		KeyDisposition("c2"):         DispositionWontFix,
		KeyDisposition("c3"):         DispositionNoAction,
		KeySuppress("stale_review"):  "true",
		AnnotationForceReview:        "true",
		AnnotationReadyToLand:        "true",
		KeyDecider("triage", "note"): "seen",
	}
	for k, v := range want {
		if err := s.SetAnnotation(kvAnn(k, v)); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	for k, v := range want {
		a, found, err := s.GetKVAnnotation(kvRepo, kvType, kvID, k)
		if err != nil || !found || a.Value != v || a.Origin != "pg-desk" || a.SetBy != "operator" || a.SetAt != "2026-09-12T00:00:00Z" {
			t.Fatalf("get %s = (%+v, %v, %v)", k, a, found, err)
		}
	}
	all, err := s.ListKVAnnotations(kvRepo, kvType, kvID)
	if err != nil || len(all) != len(want) {
		t.Fatalf("list = %d rows, %v; want %d", len(all), err, len(want))
	}
	// Overwrite replaces.
	if err := s.SetAnnotation(kvAnn(AnnotationWIP, "true")); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.GetKVAnnotation(kvRepo, kvType, kvID, AnnotationWIP); a.Value != "true" {
		t.Fatalf("overwrite value = %q", a.Value)
	}
}

func TestMigratedSyntheticDBRoundTripsThroughNewAPI(t *testing.T) {
	s, _ := openSyntheticCopy(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	for _, c := range []struct{ id, key, value string }{
		{"acme/widgets#1", AnnotationHidden, `{"value":true,"reason":"waiting on upstream"}`},
		{"acme/widgets#1", AnnotationWIP, "true"},
		{"acme/widgets#1", KeyDisposition("c100"), "will-fix"},
		{"acme/widgets#1", KeyDisposition("c200"), "wont-fix"},
		{"acme/widgets#2", AnnotationHidden, `{"value":false,"reason":null}`},
	} {
		a, found, err := s.GetKVAnnotation("acme/widgets", "pr", c.id, c.key)
		if err != nil || !found || a.Value != c.value {
			t.Fatalf("%s %s = (%+v, %v, %v), want value %s", c.id, c.key, a, found, err, c.value)
		}
	}
	// Writing through the new API onto a migrated (version 0) entity works.
	if err := s.SetAnnotation(KVAnnotation{
		Repo: "acme/widgets", EntityType: "pr", EntityID: "acme/widgets#1",
		Key: AnnotationWIP, Value: "false", Origin: "pg-desk", SetBy: "operator", SetAt: "2026-09-20T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKVAnnotationAPIRefusesOldSchema(t *testing.T) {
	s := OpenForTest(t)
	if err := s.SetAnnotation(kvAnn(AnnotationWIP, "true")); err == nil {
		t.Fatal("SetAnnotation on an old-schema store must error")
	}
}
