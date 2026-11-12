package eventqueue

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Tests for the durable level path (level.go): the append bypasses the admission
// limits, a record is written on a change and as a bounded refresh, replay hands
// the folded records over with their stored offsets, and compaction keeps the
// latest record per slot.

var levelT0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func sensorRec(resource, owner, level string, at time.Time, validFor int) LevelRecord {
	return LevelRecord{
		Resource: resource, Level: level, Owner: owner, ObservedAt: at, ReceivedAt: at,
		ExpiresAt: at.Add(time.Duration(validFor) * time.Second), ValidForS: validFor,
	}
}

func overrideRec(resource, level, owner string, at, expires time.Time) LevelRecord {
	return LevelRecord{Resource: resource, Level: level, Owner: owner, ObservedAt: at, ReceivedAt: at, ExpiresAt: expires}
}

func levelRecordsOf(t *testing.T, s Store) []Record {
	t.Helper()
	recs, err := s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, r := range recs {
		if isLevelOp(r.Op) {
			out = append(out, r)
		}
	}
	return out
}

func replayedLevels(t *testing.T, q *Queue) (recs []LevelRecord, offs []int64) {
	t.Helper()
	q.ReplayLevels(func(r LevelRecord, off int64) {
		recs = append(recs, r)
		offs = append(offs, off)
	})
	return recs, offs
}

func newLevelFileQueue(t *testing.T, path string, opts ...Option) (*Queue, *FileStore) {
	t.Helper()
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(fs, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return q, fs
}

// Replay rebuilds the levels across a restart, with the offset the live append
// returned as the receipt sequence ("live and replay yield the same order").
func TestLevel_replayRebuildsLevelsWithTheLiveSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	q, fs := newLevelFileQueue(t, path)
	seqCPU, err := q.AppendLevel(sensorRec("cpu", "host-sensor", "HIGH", levelT0, 60))
	if err != nil {
		t.Fatal(err)
	}
	seqNet, err := q.AppendLevel(sensorRec("network", "host-sensor", "UP", levelT0.Add(time.Second), 60))
	if err != nil {
		t.Fatal(err)
	}
	seqOv, err := q.AppendLevel(overrideRec("cpu", "LOW", "operator", levelT0.Add(2*time.Second), time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	if seqCPU >= seqNet || seqNet >= seqOv {
		t.Fatalf("receipt sequences not increasing: %d %d %d", seqCPU, seqNet, seqOv)
	}
	_ = fs.Close()

	q2, _ := newLevelFileQueue(t, path)
	recs, offs := replayedLevels(t, q2)
	if len(recs) != 3 {
		t.Fatalf("replayed %d records, want 3: %+v", len(recs), recs)
	}
	want := []int64{seqCPU, seqNet, seqOv}
	for i := range want {
		if offs[i] != want[i] {
			t.Errorf("replay offset %d = %d, want the live sequence %d", i, offs[i], want[i])
		}
	}
	if recs[0].Resource != "cpu" || recs[0].Level != "HIGH" || recs[2].Owner != "operator" || recs[2].Level != "LOW" {
		t.Fatalf("records = %+v", recs)
	}
	if n := q2.ReplayLevels(func(LevelRecord, int64) { t.Error("handed over twice") }); n != 0 {
		t.Fatalf("second ReplayLevels handed over %d", n)
	}
	// The next live append is above every replayed offset.
	next, err := q2.AppendLevel(sensorRec("cpu", "host-sensor", "LOW", levelT0.Add(time.Minute), 60))
	if err != nil || next <= seqOv {
		t.Fatalf("append after restart = %d, %v; want above %d", next, err, seqOv)
	}
}

// A level record never becomes a queue event, and survives alongside events.
func TestLevel_isNotAQueueEvent(t *testing.T) {
	mem := NewMemStore()
	q, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(evt("e1", "T")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AppendLevel(sensorRec("cpu", "host-sensor", "HIGH", levelT0, 60)); err != nil {
		t.Fatal(err)
	}
	if got := q.DepthByType(); len(got) != 1 || got["T"] != 1 {
		t.Fatalf("depth = %v", got)
	}
	q2, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	if got := q2.DepthByType(); len(got) != 1 || got["T"] != 1 {
		t.Fatalf("depth after restart = %v", got)
	}
	if recs, _ := replayedLevels(t, q2); len(recs) != 1 {
		t.Fatalf("levels after restart = %+v", recs)
	}
}

// A level append succeeds with the log at its hard limit and with the log marked
// unwritable (the append is attempted against the real log), while an event is
// refused.
func TestLevel_appendBypassesLogFull(t *testing.T) {
	mem := NewMemStore()
	q, _ := newLimitQueue(t, mem, 900, 1000)
	mem.SetLogSize(1000)
	if _, err := q.Enqueue(evt("e1", "T")); err == nil {
		t.Fatal("Enqueue at the hard limit should be refused")
	}
	if _, err := q.AppendLevel(sensorRec("cpu", "host-sensor", "HIGH", levelT0, 60)); err != nil {
		t.Fatalf("level append with the log full: %v", err)
	}
	if got := levelRecordsOf(t, mem); len(got) != 1 {
		t.Fatalf("level records = %d, want 1", len(got))
	}
}

func TestLevel_appendBypassesLogUnwritable(t *testing.T) {
	fs := newFailEveryAppendStore()
	q, _ := newLimitQueue(t, fs, 0, 0)
	if _, err := q.Enqueue(evt("e1", "T")); err == nil {
		t.Fatal("Enqueue on a failing log should fail")
	}
	if _, err := q.Enqueue(evt("e2", "T")); err == nil {
		t.Fatal("Enqueue while marked unwritable should be refused")
	} else if r, ok := RejectReason(err); !ok || r != ReasonLogUnwritable {
		t.Fatalf("second Enqueue: %v, want a log_unwritable refusal", err)
	}
	calls := fs.attempts()
	// The disk heals; the mark is still set (no tick has probed). The level
	// append goes to the real log anyway and clears the mark.
	fs.heal()
	if _, err := q.AppendLevel(sensorRec("cpu", "host-sensor", "HIGH", levelT0, 60)); err != nil {
		t.Fatalf("level append while marked unwritable: %v", err)
	}
	if fs.attempts() != calls+1 {
		t.Fatalf("the level append did not reach the store")
	}
	if got := levelRecordsOf(t, fs.inner); len(got) != 1 {
		t.Fatalf("level records = %d, want 1", len(got))
	}
	// A level append against a truly failing log reports the error.
	fs2 := newFailEveryAppendStore()
	q2, _ := newLimitQueue(t, fs2, 0, 0)
	if _, err := q2.AppendLevel(sensorRec("cpu", "host-sensor", "HIGH", levelT0, 60)); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("level append on a failing log: %v", err)
	}
}

// A sensor record is written on a change and as a refresh every valid_for_s / 2,
// never per pull; the projection still gets a fresh, increasing sequence for an
// answer that was not written. An operator override is always written.
func TestLevel_refreshIsBoundedAndNeverPerPull(t *testing.T) {
	mem := NewMemStore()
	q, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	at := func(s int) time.Time { return levelT0.Add(time.Duration(s) * time.Second) }
	var seqs []int64
	appendAt := func(s int, level string) {
		seq, err := q.AppendLevel(sensorRec("cpu", "host-sensor", level, at(s), 60))
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	appendAt(0, "LOW")  // first answer: written
	appendAt(10, "LOW") // pull 10 s later: not written
	appendAt(20, "LOW") // not written
	appendAt(29, "LOW") // 29 s < 30 s: not written
	if n := len(levelRecordsOf(t, mem)); n != 1 {
		t.Fatalf("records after 4 pulls = %d, want 1", n)
	}
	appendAt(30, "LOW") // valid_for_s / 2 reached: refresh written
	appendAt(40, "LOW") // not written
	appendAt(45, "HIGH")
	if n := len(levelRecordsOf(t, mem)); n != 3 {
		t.Fatalf("records = %d, want 3 (first, refresh at 30 s, change at 45 s)", n)
	}
	appendAt(50, "HIGH") // 5 s after the change: not written
	if n := len(levelRecordsOf(t, mem)); n != 3 {
		t.Fatalf("records = %d, want still 3", n)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("sequence did not increase at %d: %v", i, seqs)
		}
	}
	// Overrides are not throttled.
	for i := 0; i < 2; i++ {
		if _, err := q.AppendLevel(overrideRec("cpu", "LOW", "operator", at(51), time.Time{})); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(levelRecordsOf(t, mem)); n != 5 {
		t.Fatalf("records = %d, want 5 after two override sets", n)
	}
	// A failed write does not advance the throttle: the same answer is retried.
	fm := newFailEveryAppendStore()
	q2, _ := New(fm)
	if _, err := q2.AppendLevel(sensorRec("cpu", "host-sensor", "LOW", at(0), 60)); err == nil {
		t.Fatal("want an error")
	}
	fm.heal()
	if _, err := q2.AppendLevel(sensorRec("cpu", "host-sensor", "LOW", at(1), 60)); err != nil {
		t.Fatal(err)
	}
	if n := len(levelRecordsOf(t, fm.inner)); n != 1 {
		t.Fatalf("retry after a failed write wrote %d records, want 1", n)
	}
}

func TestLevel_sequenceStaysAboveAfterCompactionShrinksTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	q, fs := newLevelFileQueue(t, path)
	defer func() { _ = fs.Close() }()
	var last int64
	for i := 0; i < 20; i++ {
		seq, err := q.AppendLevel(sensorRec("cpu", "host-sensor", []string{"LOW", "HIGH"}[i%2], levelT0.Add(time.Duration(i)*time.Second), 60))
		if err != nil {
			t.Fatal(err)
		}
		last = seq
	}
	if _, err := q.CompactNow(); err != nil {
		t.Fatal(err)
	}
	seq, err := q.AppendLevel(sensorRec("cpu", "host-sensor", "LOW", levelT0.Add(time.Hour), 60))
	if err != nil || seq <= last {
		t.Fatalf("sequence after compaction = %d, %v; want above %d", seq, err, last)
	}
}

// --- compaction ------------------------------------------------------------

func compactedLevels(t *testing.T, recs []Record, now time.Time) []Record {
	t.Helper()
	f := newLogFold()
	f.now = now
	for _, r := range recs {
		f.apply(r)
	}
	var out []Record
	for _, r := range f.records() {
		if isLevelOp(r.Op) {
			out = append(out, r)
		}
	}
	return out
}

func lvlSet(r LevelRecord) Record { return r.record() }

// Compaction keeps ONE record per (resource, owner): two owners of one resource
// and two resources of one owner are four slots, and older records fold away.
func TestLevelCompaction_oneRecordPerResourceOwner(t *testing.T) {
	at := func(s int) time.Time { return levelT0.Add(time.Duration(s) * time.Second) }
	log := []Record{
		lvlSet(sensorRec("cpu", "sensor-a", "LOW", at(0), 60)),
		lvlSet(sensorRec("cpu", "sensor-b", "LOW", at(1), 60)),
		lvlSet(sensorRec("ram", "sensor-a", "LOW", at(2), 60)),
		lvlSet(sensorRec("cpu", "sensor-a", "HIGH", at(3), 60)),
		lvlSet(sensorRec("cpu", "sensor-a", "LOW", at(4), 60)),
		lvlSet(sensorRec("cpu", "sensor-b", "HIGH", at(5), 60)),
	}
	got := compactedLevels(t, log, at(10))
	if len(got) != 3 {
		t.Fatalf("kept %d records, want 3 (cpu/a, cpu/b, ram/a): %+v", len(got), got)
	}
	byKey := map[string]Record{}
	for _, r := range got {
		byKey[r.Resource+"/"+r.Owner] = r
	}
	if byKey["cpu/sensor-a"].LevelName != "LOW" || !byKey["cpu/sensor-a"].ObservedAt.Equal(at(4)) {
		t.Errorf("cpu/sensor-a = %+v, want the latest (LOW at 4)", byKey["cpu/sensor-a"])
	}
	if byKey["cpu/sensor-b"].LevelName != "HIGH" {
		t.Errorf("cpu/sensor-b = %+v", byKey["cpu/sensor-b"])
	}
	// log order is kept
	if !got[0].ObservedAt.Before(got[1].ObservedAt) || !got[1].ObservedAt.Before(got[2].ObservedAt) {
		t.Errorf("compacted records out of log order: %+v", got)
	}
}

// An operator override and a later sensor record never displace each other, and
// a sensor record is kept even when it is long stale (the last-known value feeds
// unknown and grace).
func TestLevelCompaction_operatorOverrideAndSensorRecordCoexist(t *testing.T) {
	at := func(s int) time.Time { return levelT0.Add(time.Duration(s) * time.Second) }
	log := []Record{
		lvlSet(overrideRec("cpu", "LOW", "operator", at(0), time.Time{})),
		lvlSet(sensorRec("cpu", "host-sensor", "HIGH", at(5), 60)),
	}
	got := compactedLevels(t, log, levelT0.Add(100*time.Hour))
	if len(got) != 2 {
		t.Fatalf("kept %d, want 2 (override and sensor record): %+v", len(got), got)
	}
	if got[0].Owner != "operator" || got[1].Owner != "host-sensor" {
		t.Fatalf("records = %+v", got)
	}
	// and the other order
	log = []Record{log[1], log[0]}
	if got := compactedLevels(t, log, levelT0.Add(100*time.Hour)); len(got) != 2 {
		t.Fatalf("reverse order kept %d, want 2", len(got))
	}
}

// Only a LAPSED operator override is dropped; an until-cleared one never lapses;
// a cleared override drops with its clear; a lapsed override never lets an older
// override of another owner come back.
func TestLevelCompaction_dropsOnlyLapsedOperatorOverride(t *testing.T) {
	at := func(s int) time.Time { return levelT0.Add(time.Duration(s) * time.Second) }
	now := at(3600)
	t.Run("lapsed is dropped, until-cleared stays", func(t *testing.T) {
		log := []Record{
			lvlSet(overrideRec("cpu", "LOW", "operator", at(0), at(60))),
			lvlSet(overrideRec("ram", "LOW", "operator", at(0), time.Time{})),
			lvlSet(overrideRec("disk", "LOW", "operator", at(0), now.Add(time.Hour))),
			lvlSet(sensorRec("cpu", "host-sensor", "HIGH", at(0), 60)),
		}
		got := compactedLevels(t, log, now)
		res := map[string]bool{}
		for _, r := range got {
			res[r.Resource+"/"+r.Owner] = true
		}
		if res["cpu/operator"] || !res["ram/operator"] || !res["disk/operator"] || !res["cpu/host-sensor"] {
			t.Fatalf("kept = %v", res)
		}
	})
	t.Run("expiry equal to now has not lapsed", func(t *testing.T) {
		log := []Record{lvlSet(overrideRec("cpu", "LOW", "operator", at(0), now))}
		if got := compactedLevels(t, log, now); len(got) != 1 {
			t.Fatalf("kept %d, want 1 (lapsed means strictly before now)", len(got))
		}
	})
	t.Run("cleared override leaves nothing", func(t *testing.T) {
		log := []Record{
			lvlSet(overrideRec("cpu", "LOW", "operator", at(0), time.Time{})),
			lvlSet(LevelRecord{Cleared: true, Resource: "cpu", Owner: "operator", ReceivedAt: at(5)}),
		}
		if got := compactedLevels(t, log, now); len(got) != 0 {
			t.Fatalf("kept %+v, want none", got)
		}
	})
	t.Run("lapsed override does not resurrect an older one of another owner", func(t *testing.T) {
		log := []Record{
			lvlSet(overrideRec("cpu", "HIGH", "alice", at(0), time.Time{})),
			lvlSet(overrideRec("cpu", "LOW", "operator", at(5), at(60))),
		}
		if got := compactedLevels(t, log, now); len(got) != 0 {
			t.Fatalf("kept %+v, want none (the later override replaced alice's, then lapsed)", got)
		}
	})
}

// A compaction through the real FileStore folds the level records, keeps events,
// and replay of the compacted log equals replay of the original.
func TestLevelCompaction_fileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	fixed := levelT0.Add(time.Hour)
	q, fs := newLevelFileQueue(t, path)
	fs.nowFn = func() time.Time { return fixed }
	if _, err := q.Enqueue(evtUntil("e1", "T", levelT0.Add(48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := q.AppendLevel(sensorRec("cpu", "host-sensor", []string{"LOW", "HIGH"}[i%2], levelT0.Add(time.Duration(i)*time.Second), 60)); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = q.AppendLevel(overrideRec("ram", "LOW", "operator", levelT0, levelT0.Add(time.Minute))) // lapsed by fixed
	_, _ = q.AppendLevel(overrideRec("disk", "LOW", "operator", levelT0, time.Time{}))
	st, err := q.CompactNow()
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsAfter != 3 { // enqueue + cpu sensor + disk override
		t.Fatalf("RecordsAfter = %d, want 3 (%+v)", st.RecordsAfter, st)
	}
	_ = fs.Close()
	q2, _ := newLevelFileQueue(t, path)
	recs, _ := replayedLevels(t, q2)
	if len(recs) != 2 || recs[0].Resource != "cpu" || recs[1].Resource != "disk" {
		t.Fatalf("replayed after compaction = %+v", recs)
	}
	if recs[0].Level != "HIGH" { // i=49 is odd
		t.Fatalf("latest cpu level = %s, want HIGH", recs[0].Level)
	}
	if q2.DepthByType()["T"] != 1 {
		t.Fatalf("the event did not survive")
	}
}

// The plan (dry run) counts the level records a compaction keeps.
func TestLevelCompaction_planCountsLevelRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	q, fs := newLevelFileQueue(t, path)
	defer func() { _ = fs.Close() }()
	for i := 0; i < 10; i++ {
		_, _ = q.AppendLevel(overrideRec("cpu", "LOW", "operator", levelT0.Add(time.Duration(i)*time.Second), time.Time{}))
	}
	plan, err := q.PlanCompaction()
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecordsBefore != 10 || plan.RecordsAfter != 1 {
		t.Fatalf("plan = %+v, want 10 -> 1", plan)
	}
}

// --- old binary -------------------------------------------------------------

// A log with the gate-era encoding plus a record whose op this binary does not
// know replays without error, the unknown record is ignored, the gate records
// are still there, and level records beside them replay.
func TestLevel_unknownOpsReplayOnAnOldBinaryFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	lines := `{"op":"enqueue","eventId":"e1","type":"T","at":"2026-11-11T12:00:00Z","expiresAt":"2026-11-12T12:00:00Z","enqueuedAt":"2026-11-11T12:00:00Z"}
{"op":"gate_set","eventId":"","gateType":"SYSTEM_PAUSE","owner":"operator","at":"2026-11-11T12:00:01Z"}
{"op":"from_the_future","eventId":"x","resource":"cpu","mystery":42}
{"op":"level_set","eventId":"","resource":"cpu","level":"HIGH","owner":"host-sensor","observedAt":"2026-11-11T12:00:02Z","receivedAt":"2026-11-11T12:00:02Z","expiresAt":"2026-11-11T12:01:02Z","validForS":60,"newerField":true}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	q, fs := newLevelFileQueue(t, path)
	defer func() { _ = fs.Close() }()
	if q.DepthByType()["T"] != 1 {
		t.Fatalf("the enqueue record did not replay: %v", q.DepthByType())
	}
	recs, offs := replayedLevels(t, q)
	if len(recs) != 1 || recs[0].Level != "HIGH" || recs[0].ValidForS != 60 {
		t.Fatalf("levels = %+v", recs)
	}
	if offs[0] <= 0 {
		t.Fatalf("offset = %d, want a byte offset into the log", offs[0])
	}
	// And a compaction over the same log keeps the levels and the event.
	st, err := q.CompactNow()
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsBefore != 4 {
		t.Fatalf("RecordsBefore = %d, want 4", st.RecordsBefore)
	}
}

// --- pause ------------------------------------------------------------------

func TestPauseRecord_shape(t *testing.T) {
	p := PauseRecord("", levelT0, 0)
	if p.Resource != RouterResource || p.Level != LevelPaused || p.Owner != LevelOwnerOperator || !p.ExpiresAt.IsZero() || p.Cleared || !p.IsOperator() {
		t.Fatalf("until-cleared pause = %+v", p)
	}
	p = PauseRecord("alice", levelT0, 2*time.Hour)
	if p.Owner != "alice" || !p.ExpiresAt.Equal(levelT0.Add(2*time.Hour)) {
		t.Fatalf("leased pause = %+v", p)
	}
	r := ResumeRecord(levelT0)
	if !r.Cleared || r.Resource != RouterResource || !r.IsOperator() {
		t.Fatalf("resume = %+v", r)
	}
}

func TestPause_isDurableWithOwnerAndLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.jsonl")
	q, fs := newLevelFileQueue(t, path)
	if _, err := q.AppendLevel(PauseRecord("alice", levelT0, 2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = fs.Close()
	q2, fs2 := newLevelFileQueue(t, path)
	recs, _ := replayedLevels(t, q2)
	if len(recs) != 1 {
		t.Fatalf("replayed %+v", recs)
	}
	r := recs[0]
	if r.Resource != RouterResource || r.Level != LevelPaused || r.Owner != "alice" || !r.ExpiresAt.Equal(levelT0.Add(2*time.Hour)) {
		t.Fatalf("replayed pause = %+v", r)
	}
	// resume clears it durably
	if _, err := q2.AppendLevel(ResumeRecord(levelT0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := q2.CompactNow(); err != nil {
		t.Fatal(err)
	}
	_ = fs2.Close()
	q3, _ := newLevelFileQueue(t, path)
	if recs, _ := replayedLevels(t, q3); len(recs) != 0 {
		t.Fatalf("after resume and compaction: %+v", recs)
	}
}
