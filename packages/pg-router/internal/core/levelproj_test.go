package core

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

// lockedClock is a mutex-guarded test clock (the eventqueue mockClock has no
// lock).
type lockedClock struct {
	mu sync.Mutex
	t  time.Time
}

func newLockedClock() *lockedClock {
	return &lockedClock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *lockedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *lockedClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeLevelAppender struct {
	mu   sync.Mutex
	recs []LevelRecord
	next int64
	err  error
}

func (f *fakeLevelAppender) Append(rec LevelRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.next += 10
	f.recs = append(f.recs, rec)
	return f.next, nil
}

type projFixture struct {
	hmu     sync.Mutex
	clk     *lockedClock
	p       *LevelProjection
	app     *fakeLevelAppender
	rejects []string
	bounds  []string
	expired []string
}

func newProj(t *testing.T) *projFixture {
	t.Helper()
	f := &projFixture{clk: newLockedClock(), app: &fakeLevelAppender{}}
	f.p = NewLevelProjection(LevelProjectionConfig{
		Registry: levels.Registry{
			"cpu":      {Levels: []string{"LOW", "HIGH"}, Unknown: "LOW"},
			"git_auth": {Levels: []string{"AVAILABLE", "UNAVAILABLE"}, Unknown: "UNAVAILABLE"},
		},
		Resources: map[string]LevelResource{
			"cpu":      {Sensor: "host-sensor", GraceFactor: 2},
			"git_auth": {Sensor: "git-auth-sensor", GraceFactor: 2},
		},
		Skew:     5 * time.Minute,
		Now:      f.clk.now,
		Appender: f.app,
		OnReject: func(_, _, r string) { f.hmu.Lock(); f.rejects = append(f.rejects, r); f.hmu.Unlock() },
		OnBoundary: func(res, from, to string) {
			f.hmu.Lock()
			f.bounds = append(f.bounds, res+":"+from+">"+to)
			f.hmu.Unlock()
		},
		OnOverrideExpired: func(res, _, owner string) { f.expired = append(f.expired, res+"/"+owner) },
	})
	return f
}

func (f *projFixture) ans(resource, level, owner string, observedOffset time.Duration, validFor int) LevelAnswer {
	return LevelAnswer{
		Resource: resource, Level: level, Owner: owner, ValidForS: validFor,
		ObservedAt: f.clk.now().Add(observedOffset),
	}
}

func (f *projFixture) level(resource string) (levels.Level, bool) {
	lv, ok := f.p.Snapshot().Levels[resource]
	return lv, ok
}

func mustOutcome(t *testing.T, got LevelOutcome, reason string, err error, want LevelOutcome, wantReason string) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want || reason != wantReason {
		t.Fatalf("outcome = %s/%q, want %s/%q", got, reason, want, wantReason)
	}
}

func TestLevelProj_acceptedAppendsAndSnapshots(t *testing.T) {
	f := newProj(t)
	o, r, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", 0, 180))
	mustOutcome(t, o, r, err, LevelAccepted, "")
	if len(f.app.recs) != 1 {
		t.Fatalf("appended %d records, want 1", len(f.app.recs))
	}
	rec := f.app.recs[0]
	if !rec.ReceivedAt.Equal(f.clk.now()) || !rec.ExpiresAt.Equal(f.clk.now().Add(180*time.Second)) {
		t.Fatalf("record times = %v / %v", rec.ReceivedAt, rec.ExpiresAt)
	}
	lv, ok := f.level("cpu")
	if !ok || lv.Value != "HIGH" || lv.State != levels.StateHeld || lv.Source != "sensor" || lv.Owner != "host-sensor" {
		t.Fatalf("level = %+v ok=%v", lv, ok)
	}
}

func TestLevelProj_snapshotAccessorSignature(t *testing.T) {
	f := newProj(t)
	var _ func() levels.Snapshot = f.p.Snapshot
}

func TestLevelProj_rejectionReasons(t *testing.T) {
	f := newProj(t)
	cases := []struct {
		name string
		a    LevelAnswer
		want string
	}{
		{"unknown resource", f.ans("nope", "HIGH", "host-sensor", 0, 60), ReasonUnknownResource},
		{"unknown level", f.ans("cpu", "BOGUS", "host-sensor", 0, 60), ReasonUnknownLevel},
		{"unknown is not a level", f.ans("cpu", "unknown", "host-sensor", 0, 60), ReasonUnknownLevel},
		{"wrong owner", f.ans("cpu", "HIGH", "someone", 0, 60), ReasonNotRegisteredSensor},
		{"operator via ingest", f.ans("cpu", "HIGH", "operator", 0, 60), ReasonNotRegisteredSensor},
		{"future observed_at", f.ans("cpu", "HIGH", "host-sensor", 5*time.Minute+time.Second, 60), ReasonFutureObservedAt},
	}
	for _, c := range cases {
		o, r, err := f.p.Ingest(c.a)
		if err != nil || o != LevelRejected || r != c.want {
			t.Errorf("%s: got %s/%q/%v, want rejected/%q", c.name, o, r, err, c.want)
		}
	}
	if len(f.rejects) != len(cases) {
		t.Fatalf("counted %d rejections, want %d", len(f.rejects), len(cases))
	}
	if len(f.app.recs) != 0 {
		t.Fatalf("a rejected answer was appended")
	}
	if _, ok := f.level("cpu"); ok {
		t.Fatal("rejected answer changed the projection")
	}
}

func TestLevelProj_skewBoundaryInclusive(t *testing.T) {
	f := newProj(t)
	o, r, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", 5*time.Minute, 60))
	mustOutcome(t, o, r, err, LevelAccepted, "")
}

func TestLevelProj_ordering(t *testing.T) {
	f := newProj(t)
	mustO := func(a LevelAnswer, want LevelOutcome, reason string) {
		t.Helper()
		o, r, err := f.p.Ingest(a)
		mustOutcome(t, o, r, err, want, reason)
	}
	first := f.ans("cpu", "HIGH", "host-sensor", -time.Minute, 180)
	mustO(first, LevelAccepted, "")
	mustO(first, LevelIdempotent, "")
	if len(f.app.recs) != 1 {
		t.Fatalf("duplicate was appended: %d records", len(f.app.recs))
	}
	conflict := first
	conflict.Level = "LOW"
	mustO(conflict, LevelRejected, ReasonConflict)
	older := f.ans("cpu", "LOW", "host-sensor", -2*time.Minute, 180)
	mustO(older, LevelRejected, ReasonOlderThanHeld)
	if lv, _ := f.level("cpu"); lv.Value != "HIGH" {
		t.Fatalf("older answer regressed the level to %s", lv.Value)
	}
	newer := f.ans("cpu", "LOW", "host-sensor", -30*time.Second, 180)
	mustO(newer, LevelAccepted, "")
	if lv, _ := f.level("cpu"); lv.Value != "LOW" {
		t.Fatalf("level = %s, want LOW", lv.Value)
	}
}

func TestLevelProj_receiptSequenceIsAppendOffset(t *testing.T) {
	f := newProj(t)
	if _, _, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", -time.Minute, 180)); err != nil {
		t.Fatal(err)
	}
	// Replay of an older offset must not overwrite the live (offset 10) record.
	f.p.ApplyRecord(LevelRecord{
		Resource: "cpu", Level: "LOW", Owner: "host-sensor",
		ObservedAt: f.clk.now().Add(-time.Hour), ReceivedAt: f.clk.now(), ExpiresAt: f.clk.now().Add(time.Hour), ValidForS: 3600,
	}, 5)
	if lv, _ := f.level("cpu"); lv.Value != "HIGH" {
		t.Fatalf("level = %s after stale replay, want HIGH", lv.Value)
	}
	f.p.ApplyRecord(LevelRecord{
		Resource: "cpu", Level: "LOW", Owner: "host-sensor",
		ObservedAt: f.clk.now(), ReceivedAt: f.clk.now(), ExpiresAt: f.clk.now().Add(time.Hour), ValidForS: 3600,
	}, 20)
	if lv, _ := f.level("cpu"); lv.Value != "LOW" {
		t.Fatalf("level = %s after newer replay, want LOW", lv.Value)
	}
}

func TestLevelProj_appendFailureStillUpdatesProjection(t *testing.T) {
	f := newProj(t)
	f.app.err = errors.New("disk full")
	o, _, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", 0, 180))
	if o != LevelAccepted || err == nil {
		t.Fatalf("got %s/%v, want accepted with the append error", o, err)
	}
	if lv, ok := f.level("cpu"); !ok || lv.Value != "HIGH" {
		t.Fatalf("projection not updated: %+v %v", lv, ok)
	}
}

func TestLevelProj_ingestJSON(t *testing.T) {
	f := newProj(t)
	obs := f.clk.now().Format(time.RFC3339)
	good := fmt.Sprintf(`{"schemaVersion":"1","resource":"cpu","level":"HIGH","observed_at":%q,"valid_for_s":60,"owner":"host-sensor"}`, obs)
	o, r, err := f.p.IngestJSON([]byte(good))
	mustOutcome(t, o, r, err, LevelAccepted, "")
	for name, bad := range map[string]string{
		"lowercase level": `{"schemaVersion":"1","resource":"cpu","level":"high","observed_at":"2026-11-11T12:00:00Z","valid_for_s":60,"owner":"host-sensor"}`,
		"missing time":    `{"schemaVersion":"1","resource":"cpu","level":"HIGH","valid_for_s":60,"owner":"host-sensor"}`,
		"zero valid":      `{"schemaVersion":"1","resource":"cpu","level":"HIGH","observed_at":"2026-11-11T12:00:00Z","valid_for_s":0,"owner":"host-sensor"}`,
		"not json":        `{`,
	} {
		o, r, err := f.p.IngestJSON([]byte(bad))
		if err != nil || o != LevelRejected || r != ReasonMalformed {
			t.Errorf("%s: got %s/%q/%v", name, o, r, err)
		}
	}
}

func TestLevelProj_heldUntilExpiresAtThenStaleThenUnknown(t *testing.T) {
	f := newProj(t)
	if _, _, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", 0, 100)); err != nil {
		t.Fatal(err)
	}
	// Exactly at expires_at: still held (strict).
	f.clk.advance(100 * time.Second)
	if lv, ok := f.level("cpu"); !ok || lv.State != levels.StateHeld {
		t.Fatalf("at expires_at: %+v ok=%v, want held", lv, ok)
	}
	// One nanosecond past: stale, value still in force.
	f.clk.advance(time.Nanosecond)
	if lv, ok := f.level("cpu"); !ok || lv.State != levels.StateStale || lv.Value != "HIGH" {
		t.Fatalf("past expires_at: %+v ok=%v, want stale HIGH", lv, ok)
	}
	// Exactly at expires_at + grace (2 x 100s): still stale and in force.
	f.clk.advance(200*time.Second - time.Nanosecond)
	if lv, ok := f.level("cpu"); !ok || lv.State != levels.StateStale {
		t.Fatalf("at end of grace: %+v ok=%v, want stale in force", lv, ok)
	}
	// Past grace: reads unknown (no entry).
	f.clk.advance(time.Nanosecond)
	if _, ok := f.level("cpu"); ok {
		t.Fatal("past grace the level must read unknown")
	}
	want := []string{"cpu:unknown>held", "cpu:held>stale", "cpu:stale>unknown"}
	_ = want
	if got := f.bounds; len(got) != 2 || got[0] != "cpu:held>stale" || got[1] != "cpu:stale>unknown" {
		t.Fatalf("boundaries = %v", got)
	}
}

func TestLevelProj_staleWhenObservationOlderThanValidityAtReceipt(t *testing.T) {
	f := newProj(t)
	// received - observed == valid_for_s: not stale (strict).
	if _, _, err := f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", -60*time.Second, 60)); err != nil {
		t.Fatal(err)
	}
	if lv, _ := f.level("cpu"); lv.State != levels.StateHeld {
		t.Fatalf("equal-age state = %v, want held", lv.State)
	}
	// One second older than valid_for_s at receipt: stale although expires_at is ahead.
	if _, _, err := f.p.Ingest(f.ans("cpu", "LOW", "host-sensor", -30*time.Second, 60)); err != nil {
		t.Fatal(err)
	}
	f2 := newProj(t)
	if _, _, err := f2.p.Ingest(f2.ans("cpu", "HIGH", "host-sensor", -61*time.Second, 60)); err != nil {
		t.Fatal(err)
	}
	if lv, ok := f2.level("cpu"); !ok || lv.State != levels.StateStale {
		t.Fatalf("old-observation state = %+v ok=%v, want stale", lv, ok)
	}
}

func TestLevelProj_unknownUntilFirstAnswer(t *testing.T) {
	f := newProj(t)
	snap := f.p.Snapshot()
	if len(snap.Levels) != 0 {
		t.Fatalf("fresh projection holds %v", snap.Levels)
	}
	reg := f.p.cfg.Registry
	req := func(res string, allowed ...string) levels.Requirement {
		return levels.Requirement{Clauses: []levels.Clause{{Index: 0, Text: res, Resource: res, Allowed: allowed}}}
	}
	// Credentials read the worst level until the first answer: blocked.
	if levels.Decide(snap, reg, req("git_auth", "AVAILABLE")).Admit {
		t.Fatal("credential admitted before the first answer")
	}
	// Every other resource reads best.
	if !levels.Decide(snap, reg, req("cpu", "LOW")).Admit {
		t.Fatal("cpu blocked before the first answer")
	}
	// After the answer the credential is admitted; once past grace it is blocked again.
	if _, _, err := f.p.Ingest(f.ans("git_auth", "AVAILABLE", "git-auth-sensor", 0, 60)); err != nil {
		t.Fatal(err)
	}
	if !levels.Decide(f.p.Snapshot(), reg, req("git_auth", "AVAILABLE")).Admit {
		t.Fatal("credential blocked after an AVAILABLE answer")
	}
	f.clk.advance(60*time.Second + 120*time.Second + time.Nanosecond)
	if levels.Decide(f.p.Snapshot(), reg, req("git_auth", "AVAILABLE")).Admit {
		t.Fatal("credential admitted after the grace period")
	}
}

func TestLevelProj_unavailableIsOrdinaryLevel(t *testing.T) {
	f := newProj(t)
	o, r, err := f.p.Ingest(f.ans("git_auth", "UNAVAILABLE", "git-auth-sensor", 0, 60))
	mustOutcome(t, o, r, err, LevelAccepted, "")
	if lv, ok := f.level("git_auth"); !ok || lv.Value != "UNAVAILABLE" || lv.State != levels.StateHeld {
		t.Fatalf("level = %+v ok=%v", lv, ok)
	}
}

func TestLevelProj_overridePrecedenceAndExpiry(t *testing.T) {
	f := newProj(t)
	if _, _, err := f.p.Ingest(f.ans("cpu", "LOW", "host-sensor", 0, 3600)); err != nil {
		t.Fatal(err)
	}
	if err := f.p.SetOverride("cpu", "HIGH", "", f.clk.now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	lv, _ := f.level("cpu")
	if lv.Value != "HIGH" || lv.Source != "operator" || lv.Owner != LevelOwnerOperator {
		t.Fatalf("override not winning: %+v", lv)
	}
	// A newer sensor reading never ends the override and is stored beneath it.
	f.clk.advance(time.Minute)
	if _, _, err := f.p.Ingest(f.ans("cpu", "LOW", "host-sensor", 0, 3600)); err != nil {
		t.Fatal(err)
	}
	if lv, _ := f.level("cpu"); lv.Value != "HIGH" {
		t.Fatalf("sensor displaced the override: %+v", lv)
	}
	// Exactly at expires_at the override is still in force (strict).
	f.clk.advance(time.Hour - time.Minute)
	if lv, _ := f.level("cpu"); lv.Source != "operator" {
		t.Fatalf("override ended at expires_at: %+v", lv)
	}
	if len(f.expired) != 0 {
		t.Fatalf("expiry logged early: %v", f.expired)
	}
	f.clk.advance(time.Nanosecond)
	if lv, _ := f.level("cpu"); lv.Source != "sensor" || lv.Value != "LOW" {
		t.Fatalf("after expiry want the sensor value: %+v", lv)
	}
	f.p.Snapshot()
	if len(f.expired) != 1 || f.expired[0] != "cpu/operator" {
		t.Fatalf("expiry logs = %v, want exactly one", f.expired)
	}
}

func TestLevelProj_untilClearedNeverLapsAndClearRemoves(t *testing.T) {
	f := newProj(t)
	if err := f.p.SetOverride("cpu", "HIGH", "alice", time.Time{}); err != nil {
		t.Fatal(err)
	}
	f.clk.advance(1000 * time.Hour)
	if lv, ok := f.level("cpu"); !ok || lv.Value != "HIGH" || lv.Owner != "alice" {
		t.Fatalf("until-cleared override lapsed: %+v ok=%v", lv, ok)
	}
	if err := f.p.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.level("cpu"); ok {
		t.Fatal("cleared override still in force with no sensor record")
	}
	if err := f.p.SetOverride("nope", "HIGH", "", time.Time{}); err == nil {
		t.Fatal("override on an unknown resource accepted")
	}
}

func TestLevelProj_replayDropsLapsedOverrideKeepsSensor(t *testing.T) {
	f := newProj(t)
	now := f.clk.now()
	f.p.ApplyRecord(LevelRecord{Resource: "cpu", Level: "LOW", Owner: "host-sensor", ObservedAt: now, ReceivedAt: now, ExpiresAt: now.Add(time.Hour), ValidForS: 3600}, 1)
	f.p.ApplyRecord(LevelRecord{Resource: "cpu", Level: "HIGH", Owner: "operator", ReceivedAt: now, ExpiresAt: now.Add(-time.Second)}, 2)
	f.p.ApplyRecord(LevelRecord{Resource: "ghost", Level: "HIGH", Owner: "operator"}, 3)
	if lv, _ := f.level("cpu"); lv.Source != "sensor" || lv.Value != "LOW" {
		t.Fatalf("lapsed override not dropped on replay: %+v", lv)
	}
}

func TestLevelProj_concurrentIngestAndSnapshot(t *testing.T) {
	f := newProj(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				f.clk.advance(time.Millisecond)
				_, _, _ = f.p.Ingest(f.ans("cpu", "HIGH", "host-sensor", 0, 60))
				f.p.Snapshot()
				f.p.Statuses()
			}
		}(i)
	}
	wg.Wait()
}
