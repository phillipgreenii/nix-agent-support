package changes

import (
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var sweepTestNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func sweepEntities() []store.Entity {
	return []store.Entity{
		{EntityType: "pr", EntityID: "fresh", HydratedAt: "2026-10-01T11:00:00Z"},
		{EntityType: "pr", EntityID: "old-b", HydratedAt: "2026-09-30T00:00:00Z"},
		{EntityType: "pr", EntityID: "old-a", HydratedAt: "2026-09-30T00:00:00Z"},
		{EntityType: "pr", EntityID: "oldest", HydratedAt: "2026-09-01T00:00:00Z"},
		{EntityType: "pr", EntityID: "never", HydratedAt: ""},
		{EntityType: "pr", EntityID: "garbled", HydratedAt: "not a time"},
		{EntityType: "pr", EntityID: "gone", HydratedAt: "2026-09-01T00:00:00Z", Inactive: true},
		{EntityType: "issue", EntityID: "other-type", HydratedAt: ""},
		{EntityType: "pr", EntityID: "edge", HydratedAt: "2026-10-01T06:00:00Z"}, // exactly 6h: not older than
	}
}

func TestActiveCount(t *testing.T) {
	es := sweepEntities()
	if got := ActiveCount(es, "pr"); got != 7 {
		t.Errorf("pr active = %d, want 7 (inactive and other-type excluded)", got)
	}
	if got := ActiveCount(es, "issue"); got != 1 {
		t.Errorf("issue active = %d, want 1", got)
	}
	if got := ActiveCount(nil, "pr"); got != 0 {
		t.Errorf("empty = %d", got)
	}
}

func TestDueBacklog(t *testing.T) {
	// due: old-b, old-a, oldest, never, garbled; not due: fresh, edge (6h is
	// not older than 6h), gone (inactive), other-type.
	if got := DueBacklog(sweepEntities(), "pr", sweepTestNow, 6*time.Hour); got != 5 {
		t.Errorf("DueBacklog = %d, want 5", got)
	}
	if got := DueBacklog(sweepEntities(), "pr", sweepTestNow, 100*24*time.Hour); got != 2 {
		t.Errorf("DueBacklog with a huge max age = %d, want 2 (never hydrated and unparseable)", got)
	}
}

func TestSweepBoundHolds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		active   int
		perPoll  int
		maxAge   time.Duration
		interval time.Duration
		want     bool
	}{
		{"comfortably inside", 100, 20, 6 * time.Hour, time.Hour, true}, // 5 x 1h <= 6h
		{"exactly on the bound", 120, 20, 6 * time.Hour, time.Hour, true},
		{"just past the bound", 121, 20, 6 * time.Hour, time.Hour, false},
		{"slow polling", 100, 20, 6 * time.Hour, 2 * time.Hour, false},
		{"no active entities", 0, 20, 6 * time.Hour, time.Hour, true},
		{"zero per poll with entities", 5, 0, 6 * time.Hour, time.Hour, false},
		{"zero per poll without entities", 0, 0, 6 * time.Hour, time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SweepBoundHolds(tc.active, tc.perPoll, tc.maxAge, tc.interval); got != tc.want {
				t.Errorf("SweepBoundHolds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectSweepOldestFirstCapAndExclusions(t *testing.T) {
	es := sweepEntities()
	got := selectSweep(es, "pr", sweepTestNow, 6*time.Hour, 4, nil)
	// never-hydrated and unparseable rank oldest of all (ties by id), then
	// the oldest dated row.
	if want := []string{"garbled", "never", "oldest", "old-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selectSweep = %v, want %v", got, want)
	}
	got = selectSweep(es, "pr", sweepTestNow, 6*time.Hour, 10, map[string]bool{"never": true, "garbled": true})
	if want := []string{"oldest", "old-a", "old-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selectSweep with exclusions = %v, want %v", got, want)
	}
	if got := selectSweep(es, "pr", sweepTestNow, 6*time.Hour, 0, nil); len(got) != 0 {
		t.Errorf("zero cap selected %v", got)
	}
}
