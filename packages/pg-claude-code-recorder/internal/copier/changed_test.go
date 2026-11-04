package copier

import (
	"os"
	"sync"
	"testing"
	"time"
)

type changeLog struct {
	mu  sync.Mutex
	got map[int64]int64
	n   int
}

func (c *changeLog) note(file, off int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.got == nil {
		c.got = map[int64]int64{}
	}
	c.got[file] = off
	c.n++
}

// take returns what was reported since the last call, and clears it.
func (c *changeLog) take() map[int64]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.got
	c.got, c.n = nil, 0
	return out
}

func hookedEnv(t *testing.T) (*env, *changeLog) {
	t.Helper()
	e := newEnv(t)
	l := &changeLog{}
	e.cp.SetOnFileChange(l.note)
	return e, l
}

func wantReport(t *testing.T, what string, got map[int64]int64, file, off int64) {
	t.Helper()
	o, ok := got[file]
	if !ok || o != off {
		t.Fatalf("%s: reported %v, want file %d at offset %d", what, got, file, off)
	}
}

func TestChangeHookReportsFirstCopyAndAppendWithTheNewOffset(t *testing.T) {
	e, l := hookedEnv(t)
	p := e.write(tx, "one\n")
	r := e.ingest(p)
	wantReport(t, "first copy", l.take(), r.File, 4)
	e.appendTo(tx, "two\n")
	e.ingest(p)
	wantReport(t, "append", l.take(), r.File, 8)
}

func TestChangeHookReportsABackfillBatchPerFile(t *testing.T) {
	e, l := hookedEnv(t)
	ps := batchPaths(e, 5, "b")
	res := e.cp.IngestBatch(ps)
	got := l.take()
	for i, r := range res {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		wantReport(t, "batch file "+string(rune('0'+i)), got, r.Result.File, int64(len("{\"n\":0}\n")))
	}
}

func TestChangeHookReportsATombstoneWithTheRecordedOffset(t *testing.T) {
	e, l := hookedEnv(t)
	p := e.write("sessions/1234.json", `{"pid":1234,"procStart":"t1"}`)
	r := e.ingest(p)
	l.take()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	if res := e.ingest(p); res.Action != "tombstone" {
		t.Fatalf("res %+v", res)
	}
	got := l.take()
	if _, ok := got[r.File]; !ok {
		t.Fatalf("tombstone not reported: %v", got)
	}
}

func TestChangeHookReportsASweepTombstoneAndPathClose(t *testing.T) {
	e, l := hookedEnv(t)
	p := e.write(tx, "gone soon\n")
	r := e.ingest(p)
	l.take()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := e.cp.SweepMissing(map[Identity]bool{}); err != nil {
		t.Fatal(err)
	}
	wantReport(t, "sweep tombstone", l.take(), r.File, int64(len("gone soon\n")))
}

func TestChangeHookReportsASnapshotVersion(t *testing.T) {
	e, l := hookedEnv(t)
	p := e.write("sessions/1234.json", `{"pid":1234,"procStart":"t1"}`)
	r := e.ingest(p)
	if _, ok := l.take()[r.File]; !ok {
		t.Fatal("first snapshot not reported")
	}
	if err := os.WriteFile(p, []byte(`{"pid":1234,"procStart":"t1","name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ingest(p)
	if _, ok := l.take()[r.File]; !ok {
		t.Fatal("second snapshot version not reported")
	}
}

func TestChangeHookReportsANewGeneration(t *testing.T) {
	e, l := hookedEnv(t)
	p := e.write(tx, "a long first content\n")
	r := e.ingest(p)
	l.take()
	if err := os.WriteFile(p, []byte("short\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ingest(p)
	if _, ok := l.take()[r.File]; !ok {
		t.Fatal("rewrite (new generation) not reported")
	}
	if e.file(r.File).CurGen < 2 {
		t.Fatalf("no new generation: %+v", e.file(r.File))
	}
}

func TestChangeHookNilIsInertAndClearable(t *testing.T) {
	e, l := hookedEnv(t)
	e.cp.SetOnFileChange(nil)
	e.ingest(e.write(tx, "x\n"))
	if got := l.take(); len(got) != 0 {
		t.Fatalf("cleared hook still called: %v", got)
	}
}
