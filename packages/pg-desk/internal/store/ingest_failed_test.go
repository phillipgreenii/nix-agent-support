package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestIngestFailed_RecordIsIdempotentPerKey(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	f := IngestFailure{
		Instance: "jira-main", EntityID: "PROJ-1", Seq: 5, StoreIdentity: "id-a",
		Kinds: []string{"created", "fields_changed"}, FirstFailedAt: "2026-10-01T00:00:00Z", LastError: "first",
	}
	if err := s.RecordIngestFailure(f); err != nil {
		t.Fatal(err)
	}
	again := f
	again.FirstFailedAt = "2026-10-02T00:00:00Z"
	again.LastError = "second"
	again.Kinds = []string{"other"}
	if err := s.RecordIngestFailure(again); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIngestFailures("jira-main")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListIngestFailures = (%v, %v), want one entry", got, err)
	}
	want := f
	want.LastError = "second"
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("entry = %+v, want %+v (LastError updated, everything else kept)", got[0], want)
	}
	if n, err := s.CountIngestFailures("jira-main"); err != nil || n != 1 {
		t.Errorf("CountIngestFailures = (%d, %v), want (1, nil)", n, err)
	}
}

func TestIngestFailed_ListOrdersBySeqAndFiltersInstance(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	for _, f := range []IngestFailure{
		{Instance: "a", EntityID: "X-3", Seq: 30, FirstFailedAt: "2026-10-01T00:00:03Z"},
		{Instance: "b", EntityID: "Y-1", Seq: 10, FirstFailedAt: "2026-10-01T00:00:01Z"},
		{Instance: "a", EntityID: "X-2", Seq: 20, FirstFailedAt: "2026-10-01T00:00:02Z"},
	} {
		if err := s.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	seqs := func(instance string) []int64 {
		t.Helper()
		l, err := s.ListIngestFailures(instance)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, f := range l {
			out = append(out, f.Seq)
		}
		return out
	}
	if got := seqs(""); !reflect.DeepEqual(got, []int64{10, 20, 30}) {
		t.Errorf("all instances = %v, want [10 20 30]", got)
	}
	if got := seqs("a"); !reflect.DeepEqual(got, []int64{20, 30}) {
		t.Errorf("instance a = %v, want [20 30]", got)
	}
	if n, err := s.CountIngestFailures(""); err != nil || n != 3 {
		t.Errorf("CountIngestFailures(\"\") = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := s.CountIngestFailures("b"); err != nil || n != 1 {
		t.Errorf("CountIngestFailures(b) = (%d, %v), want (1, nil)", n, err)
	}
}

func TestIngestFailed_ClearRemovesOneEntry(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	for _, f := range []IngestFailure{
		{Instance: "a", EntityID: "X-1", Seq: 1},
		{Instance: "a", EntityID: "X-1", Seq: 2},
		{Instance: "a", EntityID: "X-2", Seq: 2},
	} {
		if err := s.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClearIngestFailure("a", "X-1", 2); err != nil {
		t.Fatal(err)
	}
	l, err := s.ListIngestFailures("a")
	if err != nil || len(l) != 2 {
		t.Fatalf("after clear: (%v, %v), want two entries", l, err)
	}
	for _, f := range l {
		if f.EntityID == "X-1" && f.Seq == 2 {
			t.Errorf("cleared entry still present: %+v", f)
		}
	}
	if err := s.ClearIngestFailure("a", "X-1", 2); err != nil {
		t.Errorf("clearing an absent entry: %v, want nil", err)
	}
}

func TestIngestFailed_OldestIngestFailureAt(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if _, ok, err := s.OldestIngestFailureAt(); err != nil || ok {
		t.Fatalf("empty table: ok=%v err=%v, want false, nil", ok, err)
	}
	for _, f := range []IngestFailure{
		{Instance: "a", EntityID: "X-1", Seq: 1, FirstFailedAt: "2026-10-03T00:00:00Z"},
		{Instance: "b", EntityID: "Y-1", Seq: 9, FirstFailedAt: "2026-10-01T12:00:00Z"},
		{Instance: "a", EntityID: "X-2", Seq: 2, FirstFailedAt: "2026-10-02T00:00:00Z"},
	} {
		if err := s.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.OldestIngestFailureAt()
	want := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err != nil || !ok || !got.Equal(want) {
		t.Fatalf("OldestIngestFailureAt = (%v, %v, %v), want %v", got, ok, err, want)
	}
	if err := s.ClearIngestFailure("b", "Y-1", 9); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.OldestIngestFailureAt()
	if !got.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("after clearing the oldest: %v, want 2026-10-02", got)
	}
}

func TestIngestFailed_EmptyFirstFailedAtIsStamped(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.RecordIngestFailure(IngestFailure{Instance: "a", EntityID: "X-1", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	at, ok, err := s.OldestIngestFailureAt()
	if err != nil || !ok || time.Since(at) > time.Minute {
		t.Fatalf("OldestIngestFailureAt = (%v, %v, %v), want a recent stamp", at, ok, err)
	}
}

func TestIngestFailedFlagIsStickyAndClearedPerEntity(t *testing.T) {
	s := OpenForTest(t)
	first := "2026-11-10T09:00:00Z"
	rec := func(f IngestFailure) {
		t.Helper()
		if err := s.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	rec(IngestFailure{Instance: "b", EntityID: "i-1", Seq: 1, FirstFailedAt: first, FailureClass: "unavailable"})
	rec(IngestFailure{Instance: "b", EntityID: "i-1", Seq: 1, FailureClass: "not_found", FlaggedAt: "2026-11-10T10:00:00Z"})
	rec(IngestFailure{Instance: "b", EntityID: "i-1", Seq: 1, FailureClass: "not_found", FlaggedAt: "2026-11-10T11:00:00Z"})
	rec(IngestFailure{Instance: "b", EntityID: "i-1", Seq: 2, FirstFailedAt: first, FailureClass: "unavailable"})
	rec(IngestFailure{Instance: "b", EntityID: "i-9", Seq: 3, FirstFailedAt: first, FailureClass: "not_found", FlaggedAt: first})
	got, err := s.ListIngestFailures("b")
	if err != nil || len(got) != 3 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got[0].FlaggedAt != "2026-11-10T10:00:00Z" || got[0].FirstFailedAt != first {
		t.Fatalf("flag moved or first-failed lost: %+v", got[0])
	}
	if n, err := s.ClearFlaggedIngestFailures("b", "i-1"); err != nil || n != 1 {
		t.Fatalf("clear = %d, %v", n, err)
	}
	left, _ := s.ListIngestFailures("b")
	if len(left) != 2 || left[0].Seq != 2 || left[1].EntityID != "i-9" {
		t.Fatalf("left = %+v", left)
	}
}

// A table written before the failure_class and flagged_at columns gains them
// on open and its rows read as unflagged.
func TestIngestFailedLegacyTableGainsFlagColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE ingest_failed DROP COLUMN failure_class`,
		`ALTER TABLE ingest_failed DROP COLUMN flagged_at`,
		`INSERT INTO ingest_failed (instance, entity_id, seq, first_failed_at) VALUES ('b','i-1',1,'2026-11-10T09:00:00Z')`,
	} {
		if _, err := s.sql.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.ListIngestFailures("")
	if err != nil || len(got) != 1 || got[0].Flagged() || got[0].FailureClass != "" {
		t.Fatalf("list = %+v, %v", got, err)
	}
}
