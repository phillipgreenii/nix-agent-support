package nudger

import (
	"sort"
	"testing"
	"time"
)

func TestPendingStoreAddIdempotent(t *testing.T) {
	s := NewPendingStore()
	in := NudgeIntent{
		Key: IntentKey{"sid", SourceManual}, Text: "continue",
		EmittedAt: time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
	}
	if added := s.Add(in); !added {
		t.Error("Add new key returned false, want true")
	}
	in.EmittedAt = in.EmittedAt.Add(time.Minute) // mutated; same key
	if added := s.Add(in); added {
		t.Error("Add same key returned true, want false (idempotent)")
	}
	if list := s.List(); len(list) != 1 {
		t.Errorf("len(List) = %d, want 1", len(list))
	}
}

func TestPendingStoreCancel(t *testing.T) {
	s := NewPendingStore()
	k := IntentKey{"sid", SourceDisrupted}
	s.Add(NudgeIntent{Key: k, EmittedAt: time.Now()})
	if !s.HasAny("sid") {
		t.Fatal("HasAny = false after Add, want true")
	}
	s.Cancel(k)
	if s.HasAny("sid") {
		t.Error("HasAny = true after Cancel, want false")
	}
	s.Cancel(k) // second cancel is a no-op
}

func TestPendingStoreClearSession(t *testing.T) {
	s := NewPendingStore()
	for _, src := range []Source{SourceWindowReset, SourceDisrupted, SourceManual} {
		s.Add(NudgeIntent{Key: IntentKey{"sid", src}, EmittedAt: time.Now()})
	}
	s.Add(NudgeIntent{Key: IntentKey{"other", SourceManual}, EmittedAt: time.Now()})
	s.ClearSession("sid")
	if s.HasAny("sid") {
		t.Error("HasAny(sid) = true after ClearSession, want false")
	}
	if !s.HasAny("other") {
		t.Error("HasAny(other) = false after ClearSession(sid), want true")
	}
}

func TestPendingStoreSourcesFor(t *testing.T) {
	s := NewPendingStore()
	s.Add(NudgeIntent{Key: IntentKey{"sid", SourceWindowReset}, EmittedAt: time.Now()})
	s.Add(NudgeIntent{Key: IntentKey{"sid", SourceManual}, EmittedAt: time.Now()})
	got := s.SourcesFor("sid")
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []Source{SourceManual, SourceWindowReset}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("SourcesFor = %v, want %v", got, want)
	}
}
