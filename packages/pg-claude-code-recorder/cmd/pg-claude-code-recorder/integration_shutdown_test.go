//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Bounded-shutdown scenarios against the real binary. They sit in the
// integration tier (a daemon process per scenario, a large corpus), so they
// never run in the unit gate.

// shutdownDeadline is the daemon's hard shutdown deadline.
const shutdownDeadline = 8 * time.Second

// writeCorpus fills the claude directory with files transcripts spread over
// projects directories, so the startup sweep has real work to be cut short.
func (d *daemonEnv) writeCorpus(t *testing.T, files, projects int) {
	t.Helper()
	body := []byte(strings.Repeat("{\"n\":1,\"text\":\"example line\"}\n", 8))
	for p := 0; p < projects; p++ {
		dir := filepath.Join(d.claude, "projects", fmt.Sprintf("example-proj-%d", p))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := p; i < files; i += projects {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s%d.jsonl", i)), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// logEvents returns the daemon log's JSON lines whose event is name.
func (d *daemonEnv) logEvents(name string) []map[string]any {
	f, err := os.Open(d.logFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["event"] == name {
			out = append(out, m)
		}
	}
	return out
}

// logHasMsg reports whether some log line carries msg (the slog message, which
// is how the sweep lines are written).
func (d *daemonEnv) logHasMsg(msg string) bool {
	b, _ := os.ReadFile(d.logFile)
	return strings.Contains(string(b), `"msg":"`+msg+`"`)
}

// lockFree reports whether store.lock can be taken without waiting.
func (d *daemonEnv) lockFree(t *testing.T) bool {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(d.state, "store.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

func waitDone(t *testing.T, p *proc, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	select {
	case <-p.done:
	case <-time.After(within):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatalf("daemon still running %s after the signal", within)
	}
	return time.Since(start)
}

// Scenario 1: SIGTERM while the startup sweep is still walking a large corpus
// exits 0 within the deadline, frees store.lock, and a second instance starts
// at once. The logged shutdown_phase durations are the rehearsal: the measured
// non-overrun worst case.
func TestSIGTERMMidSweepExitsCleanlyWithinTheDeadline(t *testing.T) {
	d := newDaemonEnv(t)
	d.writeCorpus(t, 30000, 300)
	p := d.start(t)
	poll(t, "startup to complete", 120*time.Second, func() bool { return len(d.logEvents("startup_complete")) > 0 })
	if d.logHasMsg("sweep_done") {
		t.Skip("corpus swept before the signal; enlarge it") // keeps the scenario honest rather than vacuous
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	took := waitDone(t, p, shutdownDeadline+2*time.Second)
	if p.code != 0 {
		t.Fatalf("exit %d, want 0 (a clean stop)\nlog events: %v", p.code, d.logEvents("shutdown_overrun"))
	}
	if took >= shutdownDeadline {
		t.Fatalf("stop took %s, the deadline is %s", took, shutdownDeadline)
	}
	if !d.lockFree(t) {
		t.Fatal("store.lock is still held after the daemon exited")
	}
	var sum float64
	for _, ev := range d.logEvents("shutdown_phase") {
		t.Logf("rehearsal shutdown_phase %v duration_ms=%v", ev["phase"], ev["duration_ms"])
		var ms float64
		fmt.Sscan(fmt.Sprint(ev["duration_ms"]), &ms)
		sum += ms
	}
	if len(d.logEvents("shutdown_done")) != 1 || len(d.logEvents("shutdown_overrun")) != 0 {
		t.Fatalf("want one shutdown_done and no shutdown_overrun")
	}
	for _, ev := range d.logEvents("shutdown_done") {
		t.Logf("rehearsal shutdown_done total_ms=%v (sum of phases %.0f ms, signal to exit %s)", ev["total_ms"], sum, took)
	}

	// A second instance starts at once: it answers on the socket.
	start := time.Now()
	p2 := d.start(t)
	poll(t, "the second instance to answer", 20*time.Second, func() bool { _, ok := d.coverageFiles(t); return ok })
	t.Logf("second instance answered %s after launch", time.Since(start))
	if len(d.logEvents("open_failed")) != 0 {
		t.Fatal("second instance logged open_failed")
	}
	stop(t, p2)
}

// Scenario 2: a daemon with a loop that ignores stop is cut off at the
// deadline: exit 10, a shutdown_overrun naming the in-flight loop, and the next
// start recovers with the corpus intact.
func TestBlockedStopExitsTenAtTheDeadlineAndRecovers(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.WriteFile(d.transcript(), []byte("{\"n\":0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := d.start(t, "PG_CLAUDE_CODE_RECORDER_FAULT_BLOCK=shutdown.block")
	d.waitCaughtUp(t)
	poll(t, "the transcript to be copied", 60*time.Second, func() bool { return d.offsetEqualsSize(t, d.transcript()) })
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	took := waitDone(t, p, shutdownDeadline+5*time.Second)
	if p.code != 10 {
		t.Fatalf("exit %d, want 10 (shutdown_overrun)", p.code)
	}
	if took < shutdownDeadline-time.Second {
		t.Fatalf("exited after %s, before the %s deadline", took, shutdownDeadline)
	}
	over := d.logEvents("shutdown_overrun")
	if len(over) != 1 {
		t.Fatalf("want one shutdown_overrun, got %d", len(over))
	}
	if in, _ := over[0]["in_flight"].(string); !strings.Contains(in, "refresh_gauges") {
		t.Fatalf("shutdown_overrun in_flight %q lacks the blocked loop", in)
	}
	t.Logf("overrun after %s: %v", took, over[0])

	p2 := d.start(t)
	d.waitCaughtUp(t)
	poll(t, "the recovered copy to equal the original", 60*time.Second, func() bool { return d.offsetEqualsSize(t, d.transcript()) })
	stop(t, p2)
	if !d.copyMatches(t, d.transcript()) {
		t.Fatal("the corpus differs from the original after recovery")
	}
}

// A second SIGTERM sent while the first stop is stuck kills the process at
// once (the signal handler is released after the first), which a unit test
// cannot prove.
func TestSecondSIGTERMKillsABlockedDaemonAtOnce(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.WriteFile(d.transcript(), []byte("{\"n\":0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := d.start(t, "PG_CLAUDE_CODE_RECORDER_FAULT_BLOCK=shutdown.block")
	d.waitCaughtUp(t)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	poll(t, "the stop to begin", 5*time.Second, func() bool { return len(d.logEvents("shutdown_started")) > 0 })
	select {
	case <-p.done:
		t.Fatal("daemon exited on the first SIGTERM although its stop is blocked")
	case <-time.After(500 * time.Millisecond):
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	took := waitDone(t, p, 3*time.Second)
	if p.code == 0 || p.code == 10 {
		t.Fatalf("exit %d, want death by the second signal", p.code)
	}
	if took > 2*time.Second {
		t.Fatalf("second SIGTERM took %s", took)
	}
	if len(d.logEvents("shutdown_overrun")) != 0 {
		t.Fatal("the watchdog fired; the second signal did not kill it first")
	}
}
