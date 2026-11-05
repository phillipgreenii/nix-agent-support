package serve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

type truncEnv struct {
	d        *Daemon
	clk      interface{ Advance(time.Duration) }
	passives int
	truncs   int
	log      int
	done     int
	busy     bool
	err      error
	waits    []time.Duration
}

// newTruncEnv builds a daemon whose WAL file is walSize bytes with the limit at
// 1000, caught up, and counting fakes for both checkpoint seams.
func newTruncEnv(t *testing.T, walSize int) *truncEnv {
	t.Helper()
	d, clk, _ := tickEnv()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, store.DBName+"-wal"), make([]byte, walSize), 0o600); err != nil {
		t.Fatal(err)
	}
	d.cfg = config.Config{StateDir: dir, WALTruncateAbove: 1000}
	d.backfillDone.Store(true)
	e := &truncEnv{d: d, clk: clk, log: 10, done: 10}
	d.checkpoint = func() (int, int, error) { e.passives++; return e.log, e.done, nil }
	d.truncate = func(w time.Duration) (bool, error) {
		e.truncs++
		e.waits = append(e.waits, w)
		return e.busy, e.err
	}
	return e
}

func truncCount(d *Daemon, outcome string) float64 {
	v, _ := d.obs.Reg.Counter(obs.MWALTruncateTotal, map[string]string{"outcome": outcome})
	return v
}

func TestMaybeTruncateAttemptsAboveTheThreshold(t *testing.T) {
	e := newTruncEnv(t, 2000)
	e.d.maybeTruncate()
	if e.truncs != 1 || e.passives != 1 {
		t.Fatalf("truncates=%d fresh passives=%d, want 1 and 1", e.truncs, e.passives)
	}
	if e.waits[0] != walTruncateBusy {
		t.Fatalf("busy wait %v", e.waits[0])
	}
	if truncCount(e.d, "ok") != 1 || truncCount(e.d, "busy") != 0 || truncCount(e.d, "error") != 0 {
		t.Fatalf("outcomes ok=%v busy=%v error=%v", truncCount(e.d, "ok"), truncCount(e.d, "busy"), truncCount(e.d, "error"))
	}
	if h := e.d.obs.Reg.Hist(obs.MWALTruncateDuration); h == nil || h.Samples != 1 {
		t.Fatalf("duration histogram %+v", h)
	}
	start := e.d.obs.EventsNamed(obs.EvWALTruncateStart)
	done := e.d.obs.EventsNamed(obs.EvWALTruncateDone)
	if len(start) != 1 || len(done) != 1 {
		t.Fatalf("events start=%d done=%d", len(start), len(done))
	}
	if start[0].Attrs["source"] != "periodic" || done[0].Attrs["source"] != "periodic" || done[0].Attrs["outcome"] != "ok" {
		t.Fatalf("event attrs %v %v", start[0].Attrs, done[0].Attrs)
	}
	if _, ok := done[0].Attrs["duration_ms"]; !ok {
		t.Fatalf("no duration_ms: %v", done[0].Attrs)
	}
}

func TestMaybeTruncateGuards(t *testing.T) {
	for name, tc := range map[string]struct {
		walSize int
		setup   func(*truncEnv)
	}{
		"at the threshold": {1000, func(*truncEnv) {}},
		"below threshold":  {10, func(*truncEnv) {}},
		"disabled with 0":  {5000, func(e *truncEnv) { e.d.cfg.WALTruncateAbove = 0 }},
		"not caught up":    {5000, func(e *truncEnv) { e.d.backfillDone.Store(false) }},
		"frames pinned":    {5000, func(e *truncEnv) { e.done = 4 }},
		"no WAL file":      {0, func(e *truncEnv) { _ = os.Remove(filepath.Join(e.d.cfg.StateDir, store.DBName+"-wal")) }},
		"passive checkpoint": {5000, func(e *truncEnv) {
			e.d.checkpoint = func() (int, int, error) { e.passives++; return 0, 0, errors.New("boom") }
		}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTruncEnv(t, tc.walSize)
			tc.setup(e)
			e.d.maybeTruncate()
			if e.truncs != 0 {
				t.Fatalf("truncate attempted %d times", e.truncs)
			}
			if truncCount(e.d, "ok")+truncCount(e.d, "busy")+truncCount(e.d, "error") != 0 {
				t.Fatal("an outcome was counted for a guarded call")
			}
		})
	}
}

// A guard that fails on the fresh passive checkpoint must not consume the
// hourly allowance: the next minute may still attempt.
func TestMaybeTruncatePinnedFramesDoNotConsumeTheHour(t *testing.T) {
	e := newTruncEnv(t, 5000)
	e.done = 4
	e.d.maybeTruncate()
	e.done = e.log
	e.clk.Advance(time.Minute)
	e.d.maybeTruncate()
	if e.truncs != 1 {
		t.Fatalf("truncates = %d, want 1", e.truncs)
	}
}

func TestMaybeTruncateAtMostOncePerHour(t *testing.T) {
	e := newTruncEnv(t, 5000)
	e.busy = true // even a busy attempt uses the hour
	e.d.maybeTruncate()
	for i := 0; i < 59; i++ {
		e.clk.Advance(time.Minute)
		e.d.maybeTruncate()
	}
	if e.truncs != 1 {
		t.Fatalf("truncates inside the hour = %d, want 1", e.truncs)
	}
	e.busy = false
	e.clk.Advance(time.Minute)
	e.d.maybeTruncate()
	if e.truncs != 2 {
		t.Fatalf("truncates after the hour = %d, want 2", e.truncs)
	}
	if truncCount(e.d, "busy") != 1 || truncCount(e.d, "ok") != 1 {
		t.Fatalf("busy=%v ok=%v", truncCount(e.d, "busy"), truncCount(e.d, "ok"))
	}
}

func TestMaybeTruncateCountsErrors(t *testing.T) {
	e := newTruncEnv(t, 5000)
	e.err = errors.New("disk I/O error")
	e.d.maybeTruncate()
	if truncCount(e.d, "error") != 1 || truncCount(e.d, "ok") != 0 {
		t.Fatalf("error=%v ok=%v", truncCount(e.d, "error"), truncCount(e.d, "ok"))
	}
	if got := e.d.obs.EventsNamed(obs.EvWALTruncateDone); len(got) != 1 || got[0].Attrs["outcome"] != "error" {
		t.Fatalf("events %+v", got)
	}
}

// Real SQLite: a WAL over the limit with every frame checkpointed is truncated
// and wal_bytes follows; with a limit above the WAL nothing is attempted.
func TestMaybeTruncateShrinksARealWAL(t *testing.T) {
	d, clk, _ := tickEnv()
	dir := t.TempDir()
	s, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < 200; i++ {
		if err := s.Commit(&store.Rec{T: store.TFile, At: 1, File: s.NewFileID(), Source: "x", Mode: "append"}); err != nil {
			t.Fatal(err)
		}
	}
	wal := filepath.Join(dir, store.DBName+"-wal")
	fi, err := os.Stat(wal)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("setup: WAL %v %v", fi, err)
	}
	d.st = s
	d.cfg = config.Config{StateDir: dir, WALTruncateAbove: fi.Size() + 1}
	d.backfillDone.Store(true)
	d.maybeTruncate()
	if truncCount(d, "ok") != 0 {
		t.Fatal("attempted at a WAL below the threshold")
	}
	d.cfg.WALTruncateAbove = 1
	clk.Advance(time.Minute)
	d.maybeTruncate()
	if truncCount(d, "ok") != 1 {
		t.Fatalf("ok=%v busy=%v error=%v", truncCount(d, "ok"), truncCount(d, "busy"), truncCount(d, "error"))
	}
	fi, err = os.Stat(wal)
	if err != nil || fi.Size() > store.JournalSizeLimit {
		t.Fatalf("WAL after TRUNCATE %v %v", fi, err)
	}
	if v, _ := d.obs.Reg.Gauge(obs.MWALBytes, nil); v != float64(fi.Size()) {
		t.Fatalf("wal_bytes = %v, file is %d", v, fi.Size())
	}
}
