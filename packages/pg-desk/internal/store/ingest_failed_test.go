package store

import (
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
