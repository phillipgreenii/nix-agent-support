package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

var endsDriverSeq int

type endsEnv struct {
	t    *testing.T
	tr   *Tracker
	clk  *clock.Fake
	c    *countdriver.Counter
	ends string
}

func newEndsEnv(t *testing.T, sweepEvery time.Duration) *endsEnv {
	t.Helper()
	endsDriverSeq++
	name := fmt.Sprintf("lifecycle-ends-counting-%d", endsDriverSeq)
	c := countdriver.Register(name)
	st, err := store.Open(t.TempDir(), store.Options{DriverName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	tr := NewTracker(TrackerConfig{Store: st, ControlDir: dir, Clock: clk, EndsSweepEvery: sweepEvery})
	ends := filepath.Join(dir, "ends")
	if err := os.MkdirAll(ends, 0o755); err != nil {
		t.Fatal(err)
	}
	return &endsEnv{t: t, tr: tr, clk: clk, c: c, ends: ends}
}

func (e *endsEnv) write(name, body string) string {
	e.t.Helper()
	p := filepath.Join(e.ends, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *endsEnv) count(outcome string) float64 {
	v, _ := e.tr.cfg.Obs.Reg.Counter(obs.MEndFiles, map[string]string{"outcome": outcome})
	return v
}

func (e *endsEnv) found() float64 {
	v, _ := e.tr.cfg.Obs.Reg.Counter(obs.MFallbackFound, map[string]string{"kind": "ends"})
	return v
}

const validEnd = `{"sessionId":"example-%d","reason":"clear","timeMs":1793375050000}`

func TestObserveEndValidatesOncePerFileAndPollsChangeNothing(t *testing.T) {
	e := newEndsEnv(t, time.Hour)
	a := e.write("a.1.json", fmt.Sprintf(validEnd, 1))
	b := e.write("b.2.json", `{not json`)
	c := e.write("c.3.json", fmt.Sprintf(validEnd, 3))
	for _, p := range []string{a, b, c} {
		e.tr.ObserveEnd(p)
		e.tr.ObserveEnd(p) // a re-ingest of the same file counts nothing
	}
	if e.count("ingested") != 2 || e.count("invalid") != 1 {
		t.Fatalf("ingested %v invalid %v", e.count("ingested"), e.count("invalid"))
	}
	for i := 0; i < 3; i++ {
		e.tr.Poll()
	}
	e.clk.Advance(2 * time.Hour) // the fallback scan runs and finds all three known
	e.tr.Poll()
	if e.count("ingested") != 2 || e.count("invalid") != 1 || e.found() != 0 {
		t.Fatalf("after polls: ingested %v invalid %v found %v", e.count("ingested"), e.count("invalid"), e.found())
	}
}

func TestPollWithNoEndFileDueIssuesNoStoreQuery(t *testing.T) {
	e := newEndsEnv(t, time.Hour)
	for i := 0; i < 1000; i++ {
		e.tr.ObserveEnd(e.write(fmt.Sprintf("s%d.%d.json", i, i), fmt.Sprintf(validEnd, i)))
	}
	e.c.Reset()
	for i := 0; i < 3; i++ {
		e.tr.Poll()
	}
	if n := e.c.Queries(); n != 0 {
		t.Fatalf("%d store queries for 1000 end files none due", n)
	}
	if e.count("ingested") != 1000 {
		t.Fatalf("ingested %v", e.count("ingested"))
	}
	// Once due, the per-file SQL runs (the file has no records: kept, retried later).
	e.clk.Advance(EndFileMaxAge + time.Second)
	e.tr.Poll()
	if e.c.Queries() == 0 {
		t.Fatal("no store query ran for files that came due")
	}
	if e.count("removed") != 0 {
		t.Fatalf("a file without records was removed: %v", e.count("removed"))
	}
	e.c.Reset()
	e.tr.Poll() // pushed back by the retry interval: nothing due now
	if n := e.c.Queries(); n != 0 {
		t.Fatalf("%d store queries right after a retry was scheduled", n)
	}
}

func TestEndsFallbackScanRecoversAMissedFileOnce(t *testing.T) {
	e := newEndsEnv(t, 10*time.Minute)
	e.tr.Startup() // runs the fallback once; a file found now is not a miss
	p := e.write("m.1.json", fmt.Sprintf(validEnd, 1))
	e.tr.Poll() // not yet time
	if e.count("ingested") != 0 {
		t.Fatalf("the scan ran early: %v", e.count("ingested"))
	}
	e.clk.Advance(11 * time.Minute)
	e.tr.Poll()
	if e.count("ingested") != 1 || e.found() != 1 {
		t.Fatalf("ingested %v found %v", e.count("ingested"), e.found())
	}
	// The event path arriving late, and later scans, count nothing more.
	e.tr.ObserveEnd(p)
	e.clk.Advance(11 * time.Minute)
	e.tr.Poll()
	if e.count("ingested") != 1 || e.found() != 1 {
		t.Fatalf("double count: ingested %v found %v", e.count("ingested"), e.found())
	}
	// An emptied map (as after a missed event) is rebuilt by the scan.
	e.tr.mu.Lock()
	e.tr.endDue = map[string]time.Time{}
	e.tr.mu.Unlock()
	e.write("n.2.json", fmt.Sprintf(validEnd, 2))
	e.clk.Advance(11 * time.Minute)
	e.tr.Poll()
	if e.found() < 2 {
		t.Fatalf("found %v", e.found())
	}
}

func TestStartupSweepDoesNotCountAsAMiss(t *testing.T) {
	e := newEndsEnv(t, time.Hour)
	e.write("s.1.json", fmt.Sprintf(validEnd, 1))
	e.tr.Startup()
	if e.count("ingested") != 1 || e.found() != 0 {
		t.Fatalf("ingested %v found %v", e.count("ingested"), e.found())
	}
}

func TestPollAfterCloseRunsNoEndsWork(t *testing.T) {
	e := newEndsEnv(t, time.Minute)
	e.tr.Close()
	e.write("c.1.json", fmt.Sprintf(validEnd, 1))
	e.clk.Advance(time.Hour)
	e.tr.Poll()
	if e.count("ingested") != 0 {
		t.Fatalf("a closed tracker scanned: %v", e.count("ingested"))
	}
}
