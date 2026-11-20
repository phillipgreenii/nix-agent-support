package eventqueue

import (
	"testing"
	"time"
)

func TestRedriveIDAndEntity(t *testing.T) {
	if got := RedriveID("pr-1@abc", 1); got != "pr-1@abc~r1" {
		t.Fatalf("RedriveID = %q", got)
	}
	if got := RedriveID("pr-1@abc~r1", 2); got != "pr-1@abc~r2" {
		t.Fatalf("RedriveID of a redrive = %q", got)
	}
	if RedriveBase("pr-1@abc~r2") != "pr-1@abc" || RedriveBase("pr-1@abc") != "pr-1@abc" || RedriveBase("x~rzz") != "x~rzz" {
		t.Fatal("RedriveBase")
	}
	if EntityOf("pr-1@abc~r2") != "pr-1" || EntityOf("plain") != "plain" {
		t.Fatal("EntityOf")
	}
}

func TestAcceptedAfter(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	q, err := New(NewMemStore(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pr-1@old", "pr-1@new", "pr-2@new"} {
		at := now
		if id == "pr-1@old" {
			at = now.Add(-time.Hour)
		}
		q.entries[id] = newEntry(Event{ID: id, Type: "t", At: at, ExpiresAt: at})
		q.entries[id].accepted["r"] = true
	}
	if !q.AcceptedAfter("r", "pr-1@x~r1", now.Add(-time.Minute)) {
		t.Error("a later accepted event of the entity must be found")
	}
	if q.AcceptedAfter("r", "pr-1@x~r1", now) {
		t.Error("an event not after the instant must not count")
	}
	if q.AcceptedAfter("other", "pr-1@x", now.Add(-time.Minute)) {
		t.Error("another listener's accept must not count")
	}
	if q.AcceptedAfter("r", "pr-1@new~r1", now.Add(-time.Minute)) {
		t.Error("the redrive chain's own event must not count")
	}
	if q.AcceptedAfter("r", "pr-3@a", now.Add(-time.Hour)) {
		t.Error("another entity must not count")
	}
}
