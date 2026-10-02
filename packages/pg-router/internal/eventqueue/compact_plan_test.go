package eventqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Tests for the compaction DRY RUN (CompactPlan, bead pg2-maxn1) and the
// last-compaction record (Queue.LastCompaction): the plan is the same fold a real
// compaction runs, so what it predicts must be what a real run then does, and
// computing it must change nothing.

// planMatchesStats asserts a plan predicted exactly what the compaction that
// followed it did.
func planMatchesStats(t *testing.T, when string, p CompactPlan, st CompactStats) {
	t.Helper()
	if p.BytesBefore != st.BytesBefore || p.BytesAfter != st.BytesAfter ||
		p.RecordsBefore != st.RecordsBefore || p.RecordsAfter != st.RecordsAfter {
		t.Fatalf("%s: plan %+v does not match the real compaction %+v", when, p, st)
	}
}

func writeJSONL(t *testing.T, path string, recs []Record) {
	t.Helper()
	var buf bytes.Buffer
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// On a live store the dry run changes nothing (file bytes, directory contents,
// size gauge) and predicts the real compaction exactly; the counts are the seeded
// log's: 40 evicted events dropped, 5 retained, 1 gate.
func TestFileStorePlanCompaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	seedLog(t, path, newClock())
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	bytesBefore, entriesBefore, sizeBefore := fileBytes(t, path), dirEntries(t, dir), fs.LogSize()

	plan, err := fs.PlanCompaction()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBytes(t, path), bytesBefore) || fs.LogSize() != sizeBefore {
		t.Fatal("the dry run changed the log or the size gauge")
	}
	if got := dirEntries(t, dir); len(got) != len(entriesBefore) {
		t.Fatalf("the dry run left files behind: %v -> %v", entriesBefore, got)
	}
	if plan.EventsKept != 5 || plan.EventsDropped != 40 || plan.GatesKept != 1 || plan.Torn {
		t.Fatalf("plan counts = %+v, want 5 kept / 40 dropped / 1 gate / not torn", plan)
	}
	if plan.NoProgress() {
		t.Fatalf("a dead-weight log must plan to shrink: %+v", plan)
	}

	st, err := fs.Compact()
	if err != nil {
		t.Fatal(err)
	}
	planMatchesStats(t, "live store", plan, st)
}

// The offline plan reads the file alone: it takes no lock, creates no lock file,
// and leaves no temp file; a missing log plans to nothing.
func TestPlanCompactionFileIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	if p, err := PlanCompactionFile(path); err != nil || p != (CompactPlan{}) {
		t.Fatalf("missing log: plan = %+v, err = %v; want zero, nil", p, err)
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Fatalf("planning a missing log created %v", got)
	}

	seedLog(t, path, newClock())
	_ = os.Remove(lockPath(path)) // seedLog's store left one; a never-opened log has none
	before := fileBytes(t, path)
	plan, err := PlanCompactionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBytes(t, path), before) {
		t.Fatal("the offline dry run changed the log")
	}
	if got := dirEntries(t, dir); len(got) != 1 {
		t.Fatalf("the offline dry run left files behind: %v", got)
	}
	// It must work beside a holder of the lock.
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	again, err := PlanCompactionFile(path)
	if err != nil || again != plan {
		t.Fatalf("plan beside a lock holder = %+v, %v; want %+v", again, err, plan)
	}
	st, err := fs.Compact()
	if err != nil {
		t.Fatal(err)
	}
	planMatchesStats(t, "offline plan", plan, st)
}

// Over random hostile logs the plan predicts the real compaction's sizes and
// record counts exactly, and the counts agree with the compacted record set.
func TestPlanCompactionMatchesCompactionProperty(t *testing.T) {
	for seed := int64(0); seed < 60; seed++ {
		r := rand.New(rand.NewSource(seed))
		recs := randomLog(r, 1+r.Intn(160))
		dir := t.TempDir()
		path := filepath.Join(dir, "queue.jsonl")
		writeJSONL(t, path, recs)

		plan, err := PlanCompactionFile(path)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if plan.RecordsBefore != len(recs) {
			t.Fatalf("seed %d: RecordsBefore = %d, want %d", seed, plan.RecordsBefore, len(recs))
		}
		compacted := compactRecords(recs)
		kept := 0
		gates := 0
		for _, c := range compacted {
			switch c.Op {
			case opEnqueue:
				kept++
			case opGateSet:
				gates++
			}
		}
		if plan.EventsKept != kept || plan.GatesKept != gates || plan.RecordsAfter != len(compacted) {
			t.Fatalf("seed %d: plan %+v disagrees with compactRecords (kept %d, gates %d, records %d)",
				seed, plan, kept, gates, len(compacted))
		}
		fs, err := NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		st, err := fs.Compact()
		_ = fs.Close()
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		planMatchesStats(t, "seed", plan, st)
	}
}

// A torn tail is reported, and the plan (like a real run) counts only the
// decodable prefix as records but the whole file as bytes-before.
func TestPlanCompactionReportsTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	writeJSONL(t, path, []Record{{Op: opEnqueue, EventID: "a", Type: "T"}})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"enqueue","eventId":"b","ty`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	plan, err := PlanCompactionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Torn || plan.RecordsBefore != 1 || plan.BytesBefore != fileSize(t, path) {
		t.Fatalf("torn plan = %+v (file is %d bytes)", plan, fileSize(t, path))
	}
	if plan.NoProgress() {
		t.Fatalf("dropping a torn tail is progress: %+v", plan)
	}
}

func TestCompactPlanNoProgress(t *testing.T) {
	for _, c := range []struct {
		name          string
		before, after int64
		want          bool
	}{
		{"shrinks", 100, 40, false},
		{"same size", 100, 100, true},
		{"empty", 0, 0, true},
	} {
		if got := (CompactPlan{BytesBefore: c.before, BytesAfter: c.after}).NoProgress(); got != c.want {
			t.Errorf("%s: NoProgress = %v, want %v", c.name, got, c.want)
		}
	}
}

// The queue remembers its latest compaction: none before the first, then startup
// (stamped with the queue's clock), then manual, with the stats it returned.
func TestQueueLastCompaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	clk := newClock()
	seedLog(t, path, clk)

	q, fs := newFileQueue(t, path, WithClock(clk.now), WithCompaction(0, true))
	defer func() { _ = fs.Close() }()
	info, ok := q.LastCompaction()
	if !ok || info.Trigger != "startup" || !info.At.Equal(clk.now()) {
		t.Fatalf("after startup: %+v, %v; want trigger startup at %v", info, ok, clk.now())
	}
	if info.RecordsAfter >= info.RecordsBefore || info.BytesAfter != fs.LogSize() {
		t.Fatalf("startup stats not recorded: %+v (log is %d bytes)", info, fs.LogSize())
	}

	clk.advance(1)
	mustEnqueue(t, q, evtUntil("x", "U", clk.in(time.Hour)))
	plan, err := q.PlanCompaction()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := q.LastCompaction(); again != info {
		t.Fatal("a dry run must not replace the last-compaction record")
	}
	st, err := q.CompactNow()
	if err != nil {
		t.Fatal(err)
	}
	planMatchesStats(t, "queue", plan, st)
	got, ok := q.LastCompaction()
	if !ok || got.Trigger != "manual" || got.CompactStats != st || !got.At.Equal(clk.now()) {
		t.Fatalf("after manual: %+v, %v; want manual with %+v", got, ok, st)
	}
	if q.Compactions() != 2 {
		t.Fatalf("Compactions = %d, want 2", q.Compactions())
	}
}

// A queue over a store with no compaction support says so, never panics, and has
// no last compaction.
func TestQueueCompactionUnsupportedStore(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.LastCompaction(); ok {
		t.Fatal("a queue that never compacted reports a last compaction")
	}
	if _, err := q.PlanCompaction(); !errors.Is(err, ErrCompactionUnsupported) {
		t.Fatalf("PlanCompaction err = %v, want ErrCompactionUnsupported", err)
	}
	if _, err := q.CompactNow(); !errors.Is(err, ErrCompactionUnsupported) {
		t.Fatalf("CompactNow err = %v, want ErrCompactionUnsupported", err)
	}
}
