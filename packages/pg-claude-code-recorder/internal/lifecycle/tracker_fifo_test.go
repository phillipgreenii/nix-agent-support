package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

type fifoEnv struct {
	t     *testing.T
	tr    *Tracker
	clk   *clock.Fake
	c     *countdriver.Counter
	sys   *proc.Fake
	inbox string
}

func newFifoEnv(t *testing.T, every time.Duration) *fifoEnv {
	t.Helper()
	endsDriverSeq++
	name := "lifecycle-fifo-counting-" + strconv.Itoa(endsDriverSeq)
	c := countdriver.Register(name)
	st, err := store.Open(t.TempDir(), store.Options{DriverName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	sys := proc.NewFake()
	tr := NewTracker(TrackerConfig{Store: st, Sys: sys, ControlDir: dir, ClaudeDir: t.TempDir(), Clock: clk, FifoSweepEvery: every})
	t.Cleanup(tr.Close)
	inbox := filepath.Join(dir, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	return &fifoEnv{t: t, tr: tr, clk: clk, c: c, sys: sys, inbox: inbox}
}

func (e *fifoEnv) mkfifo(pid int) string {
	e.t.Helper()
	p := filepath.Join(e.inbox, strconv.Itoa(pid)+".fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *fifoEnv) counter(name, kind string) float64 {
	var l map[string]string
	if kind != "" {
		l = map[string]string{"kind": kind}
	}
	v, _ := e.tr.cfg.Obs.Reg.Counter(name, l)
	return v
}

func present(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestStartSweepRemovesFifosOfDeadAndNeverTrackedProcesses(t *testing.T) {
	e := newFifoEnv(t, time.Hour)
	e.sys.Add(11, e.clk.Now(), nil) // alive, never tracked: kept
	live := e.mkfifo(11)
	dead := e.mkfifo(12) // never tracked and gone (the StartTime/ErrGone branch)
	e.tr.Startup()
	if !present(live) || present(dead) {
		t.Fatalf("live present=%v dead present=%v", present(live), present(dead))
	}
	if e.counter(obs.MStaleFifosRemoved, "") != 1 || e.counter(obs.MFallbackFound, "fifos") != 0 {
		t.Fatalf("removed %v fallback %v", e.counter(obs.MStaleFifosRemoved, ""), e.counter(obs.MFallbackFound, "fifos"))
	}
}

func TestPollBetweenFifoSweepsIssuesNoQueryAndSweepRunsOnTheClock(t *testing.T) {
	e := newFifoEnv(t, 10*time.Minute)
	e.tr.Startup()
	missed := e.mkfifo(21) // its process is gone and nothing removed the FIFO
	e.c.Reset()
	for i := 0; i < 3; i++ {
		e.tr.Poll()
	}
	if n := e.c.Queries(); n != 0 {
		t.Fatalf("%d store queries in polls between sweeps", n)
	}
	if !present(missed) {
		t.Fatal("the FIFO was swept between sweeps")
	}
	e.clk.Advance(11 * time.Minute)
	e.tr.Poll()
	if present(missed) {
		t.Fatal("the slow sweep did not remove the FIFO")
	}
	if e.counter(obs.MStaleFifosRemoved, "") != 1 || e.counter(obs.MFallbackFound, "fifos") != 1 {
		t.Fatalf("removed %v fallback %v", e.counter(obs.MStaleFifosRemoved, ""), e.counter(obs.MFallbackFound, "fifos"))
	}
	e.clk.Advance(11 * time.Minute)
	e.tr.Poll()
	if e.counter(obs.MStaleFifosRemoved, "") != 1 || e.counter(obs.MFallbackFound, "fifos") != 1 {
		t.Fatal("a removal was counted twice")
	}
}

func TestProcessExitRemovesItsFifoWithoutTheFallback(t *testing.T) {
	e := newFifoEnv(t, 10*time.Minute)
	start := e.clk.Now()
	e.sys.Add(31, start, nil)
	reg := filepath.Join(e.tr.cfg.ClaudeDir, "sessions", "31.json")
	body := `{"pid":31,"sessionId":"example-f","procStart":"` + proc.FormatStart(start) + `"}`
	if err := os.MkdirAll(filepath.Dir(reg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	e.tr.Startup()
	p := e.mkfifo(31)
	e.sys.Exit(31, start.Add(time.Minute))
	deadline := time.Now().Add(5 * time.Second)
	for present(p) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if present(p) {
		t.Fatal("exit did not remove the FIFO")
	}
	e.clk.Advance(11 * time.Minute)
	e.tr.Poll()
	if e.counter(obs.MStaleFifosRemoved, "") != 1 || e.counter(obs.MFallbackFound, "fifos") != 0 {
		t.Fatalf("removed %v fallback %v", e.counter(obs.MStaleFifosRemoved, ""), e.counter(obs.MFallbackFound, "fifos"))
	}
}

func TestClosedTrackerRunsNoFifoSweep(t *testing.T) {
	e := newFifoEnv(t, time.Minute)
	e.tr.Startup()
	p := e.mkfifo(41)
	e.tr.Close()
	e.clk.Advance(time.Hour)
	e.tr.Poll()
	if !present(p) {
		t.Fatal("a closed tracker swept")
	}
}
