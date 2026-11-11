package core

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

// eventsFixture records the projection's transition events.
type eventsFixture struct {
	*projFixture
	changes []string // "resource:from>to"
	graces  []string // "resource/lastLevel"
	rejects []string // "resource/level/reason"
	expired []string // "resource/level/owner"
}

func newEventsProj(t *testing.T) *eventsFixture {
	t.Helper()
	f := &eventsFixture{projFixture: &projFixture{clk: newLockedClock(), app: &fakeLevelAppender{}}}
	f.p = NewLevelProjection(LevelProjectionConfig{
		Registry:  levels.Registry{"cpu": {Levels: []string{"LOW", "HIGH"}, Unknown: "LOW"}},
		Resources: map[string]LevelResource{"cpu": {Sensor: "host-sensor", GraceFactor: 2}},
		Skew:      5 * time.Minute,
		Now:       f.clk.now,
		Appender:  f.app,
		OnLevelChange: func(res, from, to string) {
			f.changes = append(f.changes, res+":"+from+">"+to)
		},
		OnGraceExpired: func(res, lvl string) { f.graces = append(f.graces, res+"/"+lvl) },
		OnReject: func(res, lvl, reason string) {
			f.rejects = append(f.rejects, res+"/"+lvl+"/"+reason)
		},
		OnOverrideExpired: func(res, lvl, owner string) { f.expired = append(f.expired, res+"/"+lvl+"/"+owner) },
	})
	return f
}

// A level change is reported once per change of the level in force, and an
// absent level reads as the scale's unknown value.
func TestLevelProjEvents_levelChangedOncePerChangeAndGraceExpiredOnce(t *testing.T) {
	f := newEventsProj(t)
	if _, _, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", 0, 100)); err != nil {
		t.Fatal(err)
	}
	f.p.Snapshot()
	f.p.Snapshot() // a second evaluation at the same level reports nothing
	if want := []string{"cpu:LOW>HIGH"}; !slices.Equal(f.changes, want) {
		t.Fatalf("changes = %v, want %v", f.changes, want)
	}
	f.clk.advance(100*time.Second + time.Nanosecond) // stale, still HIGH
	f.p.Snapshot()
	if len(f.graces) != 0 || len(f.changes) != 1 {
		t.Fatalf("stale: graces=%v changes=%v, want none yet", f.graces, f.changes)
	}
	f.clk.advance(200 * time.Second) // past grace: no level in force, reads LOW
	f.p.Snapshot()
	f.p.Snapshot()
	if want := []string{"cpu/HIGH"}; !slices.Equal(f.graces, want) {
		t.Fatalf("graces = %v, want %v (once, naming the last level held)", f.graces, want)
	}
	if want := []string{"cpu:LOW>HIGH", "cpu:HIGH>LOW"}; !slices.Equal(f.changes, want) {
		t.Fatalf("changes = %v, want %v", f.changes, want)
	}
}

// A rejection names the resource and level of the answer, including an answer
// that failed schema validation but could still be read.
func TestLevelProjEvents_rejectNamesResourceAndLevel(t *testing.T) {
	f := newEventsProj(t)
	if _, _, err := f.p.Ingest(f.ans("cpu", "NOPE", "host-sensor", 0, 100)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.p.IngestJSON([]byte(`{"resource":"cpu","level":"low"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.p.IngestJSON([]byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	want := []string{"cpu/NOPE/unknown_level", "cpu/low/malformed", "//malformed"}
	if !slices.Equal(f.rejects, want) {
		t.Fatalf("rejects = %v, want %v", f.rejects, want)
	}
}

// An override that lapses reports the level it held.
func TestLevelProjEvents_overrideExpiredNamesLevel(t *testing.T) {
	f := newEventsProj(t)
	if err := f.p.SetOverride("cpu", "HIGH", "", f.clk.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.clk.advance(time.Minute + time.Nanosecond)
	f.p.Snapshot()
	f.p.Snapshot()
	if want := []string{"cpu/HIGH/operator"}; !slices.Equal(f.expired, want) {
		t.Fatalf("expired = %v, want %v (once)", f.expired, want)
	}
}
