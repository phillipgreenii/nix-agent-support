package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
)

// End to end through the real durable log: the projection appends through the
// queue, a restart replays into a fresh projection.

type durableFixture struct {
	path string
	clk  *lockedClock
}

func (d *durableFixture) open(t *testing.T) (*eventqueue.Queue, *eventqueue.FileStore, *LevelProjection) {
	t.Helper()
	fs, err := eventqueue.NewFileStore(d.path)
	if err != nil {
		t.Fatal(err)
	}
	q, err := eventqueue.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	p := NewLevelProjection(LevelProjectionConfig{
		Registry: levels.Registry{
			"cpu":    {Levels: []string{"LOW", "HIGH"}, Unknown: "LOW"},
			"router": {Levels: []string{"RUNNING", "PAUSED"}, Unknown: "RUNNING"},
		},
		Resources: map[string]LevelResource{"cpu": {Sensor: "host-sensor", GraceFactor: 2}},
		Skew:      5 * time.Minute,
		Now:       d.clk.now,
		Appender:  QueueLevelAppender{Q: q},
	})
	ReplayLevels(q, p)
	return q, fs, p
}

func TestDurableLevels_replayRestoresProjection(t *testing.T) {
	d := &durableFixture{path: filepath.Join(t.TempDir(), "q.jsonl"), clk: newLockedClock()}
	_, fs, p := d.open(t)
	out, reason, err := p.Ingest(LevelAnswer{Resource: "cpu", Level: "HIGH", Owner: "host-sensor", ValidForS: 600, ObservedAt: d.clk.now()})
	mustOutcome(t, out, reason, err, LevelAccepted, "")
	if err := p.SetOverride("cpu", "LOW", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()

	d.clk.advance(time.Minute)
	_, _, p2 := d.open(t)
	lv, ok := p2.Snapshot().Levels["cpu"]
	if !ok || lv.Value != "LOW" || lv.Source != "operator" {
		t.Fatalf("override not restored: %+v %v", lv, ok)
	}
	if err := p2.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	lv, ok = p2.Snapshot().Levels["cpu"]
	if !ok || lv.Value != "HIGH" || lv.Source != "sensor" {
		t.Fatalf("the sensor record beneath the override was not restored: %+v %v", lv, ok)
	}
}

// An override that lapsed during downtime stays lapsed after the restart.
func TestDurableLevels_overrideLapsedDuringDowntimeStaysLapsed(t *testing.T) {
	d := &durableFixture{path: filepath.Join(t.TempDir(), "q.jsonl"), clk: newLockedClock()}
	_, fs, p := d.open(t)
	out, reason, err := p.Ingest(LevelAnswer{Resource: "cpu", Level: "HIGH", Owner: "host-sensor", ValidForS: 36000, ObservedAt: d.clk.now()})
	mustOutcome(t, out, reason, err, LevelAccepted, "")
	if err := p.SetOverride("cpu", "LOW", "", d.clk.now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
	d.clk.advance(2 * time.Hour) // down for two hours
	_, _, p2 := d.open(t)
	lv, ok := p2.Snapshot().Levels["cpu"]
	if !ok || lv.Source != "sensor" || lv.Value != "HIGH" {
		t.Fatalf("lapsed override came back: %+v %v", lv, ok)
	}
}

// An operator override survives a later sensor record, across a restart and a
// compaction.
func TestDurableLevels_overrideSurvivesLaterSensorRecordAndCompaction(t *testing.T) {
	d := &durableFixture{path: filepath.Join(t.TempDir(), "q.jsonl"), clk: newLockedClock()}
	q, fs, p := d.open(t)
	if err := p.SetOverride("cpu", "LOW", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	d.clk.advance(time.Second)
	out, reason, err := p.Ingest(LevelAnswer{Resource: "cpu", Level: "HIGH", Owner: "host-sensor", ValidForS: 600, ObservedAt: d.clk.now()})
	mustOutcome(t, out, reason, err, LevelAccepted, "")
	if _, err := q.CompactNow(); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
	_, _, p2 := d.open(t)
	lv := p2.Snapshot().Levels["cpu"]
	if lv.Value != "LOW" || lv.Source != "operator" {
		t.Fatalf("override displaced by the later sensor record: %+v", lv)
	}
}

// The router PAUSED level is durable with its owner and lease.
func TestDurableLevels_routerPauseSurvivesRestartWithOwnerAndLease(t *testing.T) {
	d := &durableFixture{path: filepath.Join(t.TempDir(), "q.jsonl"), clk: newLockedClock()}
	_, fs, p := d.open(t)
	if err := p.SetOverride("router", "PAUSED", "alice", d.clk.now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
	d.clk.advance(time.Hour)
	_, fs2, p2 := d.open(t)
	lv, ok := p2.Snapshot().Levels["router"]
	if !ok || lv.Value != "PAUSED" || lv.Owner != "alice" || lv.ExpireAt.IsZero() {
		t.Fatalf("pause after restart = %+v %v", lv, ok)
	}
	_ = fs2.Close()
	d.clk.advance(2 * time.Hour) // the lease ran out while down
	_, _, p3 := d.open(t)
	if lv, ok := p3.Snapshot().Levels["router"]; ok {
		t.Fatalf("a lapsed pause came back: %+v", lv)
	}
}

// A refresh-throttled answer still extends the in-memory level.
func TestDurableLevels_unwrittenRefreshStillExtendsInMemoryExpiry(t *testing.T) {
	d := &durableFixture{path: filepath.Join(t.TempDir(), "q.jsonl"), clk: newLockedClock()}
	q, fs, p := d.open(t)
	defer func() { _ = fs.Close() }()
	for i := 0; i < 5; i++ {
		out, reason, err := p.Ingest(LevelAnswer{Resource: "cpu", Level: "HIGH", Owner: "host-sensor", ValidForS: 60, ObservedAt: d.clk.now()})
		mustOutcome(t, out, reason, err, LevelAccepted, "")
		d.clk.advance(5 * time.Second)
	}
	lv := p.Snapshot().Levels["cpu"]
	if want := d.clk.now().Add(-5 * time.Second).Add(60 * time.Second); !lv.ExpireAt.Equal(want) {
		t.Fatalf("expiry = %v, want the latest answer's %v", lv.ExpireAt, want)
	}
	plan, err := q.PlanCompaction()
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecordsBefore != 1 {
		t.Fatalf("log holds %d records after 5 pulls in 25 s, want 1", plan.RecordsBefore)
	}
}
