package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakePgReview installs a fake pg-review on a temp PATH. It records its argv
// (one per line) and every signal it receives, prints a line to stdout, and
// exits with the given code.
func fakePgReview(t *testing.T, exit int, extra string) (argvFile, sigFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	sigFile = filepath.Join(dir, "sigs")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argvFile + "'; done\n" +
		"echo \"$$\" > '" + filepath.Join(dir, "pid") + "'\n" +
		"trap 'echo TERM >> \"" + sigFile + "\"; exit " + strconv.Itoa(exit) + "' TERM\n" +
		extra +
		"echo child-out\n" +
		"exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-review"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return argvFile, sigFile
}

// lockedBuf is a goroutine-safe buffer: exec copies the child's stderr into
// it concurrently with the adapter's own log writes.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func readLines(t *testing.T, f string) []string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func do(args ...string) (code int, out, errOut string) {
	var o bytes.Buffer
	e := &lockedBuf{}
	code = run(args, &o, e, make(chan os.Signal))
	return code, o.String(), e.String()
}

func TestRunBuildsCommandLineAndKey(t *testing.T) {
	argv, _ := fakePgReview(t, 0, "")
	code, out, errOut := do("run", "2026-11-05T02:00:00-05:00", "agent-workflow-workspace-daily", "agent-workflow", "workspace", "24h")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	want := []string{
		"job", "run", "agent-workflow", "--scope", "workspace",
		"--from", "2026-11-04T02:00:00-05:00",
		"--to", "2026-11-05T02:00:00-05:00",
		"--run-key", "agent-workflow/workspace/24h/2026-11-05T07:00:00Z",
		"--schedule-id", "agent-workflow-workspace-daily",
	}
	got := readLines(t, argv)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	if out != "child-out\n" {
		t.Fatalf("child stdout not passed through: %q", out)
	}
	first := strings.SplitN(errOut, "\n", 2)[0]
	if !strings.Contains(first, "from=2026-11-04T02:00:00-05:00") || !strings.Contains(first, "to=2026-11-05T02:00:00-05:00") {
		t.Fatalf("first log line lacks the window: %q", first)
	}
}

func TestRunKeyUTCZAndDailyVersusWeeklyDiffer(t *testing.T) {
	cases := []struct {
		ts, window, want string
	}{
		{"2026-11-05T07:00:00Z", "24h", "l/s/24h/2026-11-05T07:00:00Z"},
		{"2026-11-05T07:00:00Z", "7d", "l/s/7d/2026-11-05T07:00:00Z"},
		{"2026-11-05T02:00:00-05:00", "7d", "l/s/7d/2026-11-05T07:00:00Z"},
		{"2026-11-05T13:30:00+06:30", "24h", "l/s/24h/2026-11-05T07:00:00Z"},
		{"2026-11-05T07:00:00.987Z", "24h", "l/s/24h/2026-11-05T07:00:00Z"}, // second precision
	}
	for _, c := range cases {
		a, err := parseRun([]string{c.ts, "id", "l", "s", c.window})
		if err != nil {
			t.Fatal(err)
		}
		if got := a.runKey(); got != c.want {
			t.Errorf("%s %s: key %q want %q", c.ts, c.window, got, c.want)
		}
	}
	d, _ := parseRun([]string{"2026-11-05T07:00:00Z", "id", "l", "s", "24h"})
	w, _ := parseRun([]string{"2026-11-05T07:00:00Z", "id", "l", "s", "7d"})
	if d.runKey() == w.runKey() {
		t.Fatal("daily and weekly keys ending at the same instant must differ")
	}
}

func TestToPassedVerbatimWithOffset(t *testing.T) {
	argv, _ := fakePgReview(t, 0, "")
	if code, _, e := do("run", "2026-11-05T02:00:00.5-05:00", "id", "l", "s", "24h"); code != 0 {
		t.Fatal(code, e)
	}
	got := readLines(t, argv)
	for i, a := range got {
		if a == "--to" && got[i+1] != "2026-11-05T02:00:00.5-05:00" {
			t.Fatalf("--to not verbatim: %q", got[i+1])
		}
	}
}

// The adapter does its arithmetic in Go on parsed instants and never calls
// date(1), so macOS (BSD) and Linux (GNU) cannot differ. These tests pin the
// result against a fixed-instant oracle and against the process time zone.
func TestWindowArithmeticElapsedAcrossDST(t *testing.T) {
	for _, tz := range []string{"UTC", "America/New_York", "Pacific/Auckland", "Asia/Kolkata"} {
		t.Run(tz, func(t *testing.T) {
			t.Setenv("TZ", tz)
			// US DST starts 2026-03-08; a 7d window ending 2026-03-09 spans it.
			a, err := parseRun([]string{"2026-03-09T02:00:00-04:00", "id", "l", "s", "7d"})
			if err != nil {
				t.Fatal(err)
			}
			if got := a.to.Sub(a.from); got != 168*time.Hour {
				t.Fatalf("7d window is %v, want 168h elapsed", got)
			}
			if got := a.from.Format(time.RFC3339); got != "2026-03-02T02:00:00-04:00" {
				t.Fatalf("from %s", got)
			}
			if got := a.from.UTC().Format(time.RFC3339); got != "2026-03-02T06:00:00Z" {
				t.Fatalf("from UTC %s", got)
			}
			d, _ := parseRun([]string{"2026-03-09T02:00:00-04:00", "id", "l", "s", "24h"})
			if d.to.Sub(d.from) != 24*time.Hour {
				t.Fatal("24h window")
			}
			if got := d.from.Format(time.RFC3339); got != "2026-03-08T02:00:00-04:00" {
				t.Fatalf("24h from %s", got)
			}
		})
	}
}

func TestWindowGrammar(t *testing.T) {
	good := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 168 * time.Hour, "30d": 720 * time.Hour}
	for w, want := range good {
		if got, err := parseWindow(w); err != nil || got != want {
			t.Errorf("%s: %v %v", w, got, err)
		}
	}
	for _, w := range []string{"", "h", "d", "0h", "0d", "-1h", "+1h", "1.5h", "24", "24H", "7w", "1m", "1h30m", " 1h", "1 h", "99999999999999999999h", "9999999d"} {
		if _, err := parseWindow(w); err == nil {
			t.Errorf("%q must be rejected", w)
		}
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	fakePgReview(t, 0, "")
	cases := [][]string{
		{},
		{"bogus"},
		{"run"},
		{"run", "2026-11-05T07:00:00Z", "id", "l", "s"},
		{"run", "2026-11-05T07:00:00Z", "id", "l", "s", "24h", "extra"},
		{"run", "not-a-time", "id", "l", "s", "24h"},
		{"run", "2026-11-05", "id", "l", "s", "24h"},
		{"run", "2026-11-05T07:00:00Z", "id", "l", "s", "1w"},
		{"run", "2026-11-05T07:00:00Z", "id", "--lens", "s", "24h"},
		{"run", "2026-11-05T07:00:00Z", "", "l", "s", "24h"},
		{"maintain", "extra"},
	}
	for _, c := range cases {
		if code, _, _ := do(c...); code != 2 {
			t.Errorf("%q: exit %d, want 2", c, code)
		}
	}
}

// TestSkippedCodePinned pins the duplicated value of pg-review's skipped code.
func TestSkippedCodePinned(t *testing.T) {
	if exitSkipped != 20 || exitBusyDecline != 9 {
		t.Errorf("exitSkipped %d, exitBusyDecline %d, want 20 and 9", exitSkipped, exitBusyDecline)
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		child, want int
		logged      bool
	}{
		{0, 0, false},
		{20, 0, true},
		{9, 0, true},
		// 3 and 5 meant skipped and busy before the shared exit codes; they
		// are now Unavailable and Degraded and pass through as failures.
		{3, 3, false},
		{5, 5, false},
		{4, 4, false},
		{6, 6, false},
		{7, 7, false},
		{8, 8, false},
		{1, 1, false},
		{2, 2, false},
		{42, 42, false},
		{21, 21, false},
		{22, 22, false},
		{23, 23, false},
	}
	for _, c := range cases {
		for _, sub := range [][]string{
			{"run", "2026-11-05T07:00:00Z", "id", "l", "s", "24h"},
			{"maintain"},
		} {
			fakePgReview(t, c.child, "")
			code, _, errOut := do(sub...)
			if code != c.want {
				t.Errorf("%s child %d: exit %d want %d", sub[0], c.child, code, c.want)
			}
			if code == 9 {
				t.Errorf("adapter must never exit 9")
			}
			n := strings.Count(errOut, "exited "+strconv.Itoa(c.child)+",")
			if c.logged && n != 1 {
				t.Errorf("%s child %d: want exactly one mapping log line, got %d in %q", sub[0], c.child, n, errOut)
			}
			if !c.logged && n != 0 {
				t.Errorf("%s child %d: unexpected mapping line in %q", sub[0], c.child, errOut)
			}
			if c.child == 20 && !strings.Contains(errOut, "skipped or already done") {
				t.Errorf("skip reason missing: %q", errOut)
			}
			if c.child == 9 && !strings.Contains(errOut, "run lock busy") {
				t.Errorf("lock reason missing: %q", errOut)
			}
		}
	}
}

func TestMaintainArgv(t *testing.T) {
	argv, _ := fakePgReview(t, 0, "")
	if code, _, e := do("maintain"); code != 0 {
		t.Fatal(code, e)
	}
	if got := readLines(t, argv); strings.Join(got, " ") != "job maintain" {
		t.Fatalf("argv %q", got)
	}
}

func TestChildKilledBySignalIsNonZeroNot9(t *testing.T) {
	fakePgReview(t, 0, "kill -KILL $$\n")
	code, _, errOut := do("maintain")
	if code != 1 || !strings.Contains(errOut, "terminated by a signal") {
		t.Fatalf("code %d, %q", code, errOut)
	}
}

func TestMissingPgReview(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if code, _, _ := do("maintain"); code != 1 {
		t.Fatalf("code %d", code)
	}
}

// On SIGTERM the adapter keeps waiting for the child and forwards nothing: the
// child gets the router's process-group signal directly (simulated here by
// signalling the child ourselves, once), and must see exactly one signal.
func TestSIGTERMWaitsForChildAndForwardsNothing(t *testing.T) {
	// The child blocks in a short sleep loop until its TERM trap fires, and
	// exits 20 (skipped) so the mapped result is observable.
	_, sigFile := fakePgReview(t, 20, "while :; do sleep 0.05; done\n")
	pidFile := filepath.Join(filepath.Dir(sigFile), "pid")
	sigs := make(chan os.Signal, 4)
	var o bytes.Buffer
	e := &lockedBuf{}
	res := make(chan int, 1)
	go func() { res <- run([]string{"run", "2026-11-05T07:00:00Z", "id", "l", "s", "24h"}, &o, e, sigs) }()

	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = p
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("fake child never started")
	}
	// The adapter receives SIGTERM; it must not return or signal the child.
	sigs <- syscall.SIGTERM
	select {
	case c := <-res:
		t.Fatalf("adapter returned %d before the child exited", c)
	case <-time.After(300 * time.Millisecond):
	}
	if got := readLines(t, sigFile); len(got) != 0 {
		t.Fatalf("child received a signal from the adapter: %q", got)
	}
	// The child receives its own (process-group) SIGTERM and exits 20.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-res:
		if c != 0 {
			t.Fatalf("exit %d, want mapped 0", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("adapter did not return after the child exited")
	}
	if got := readLines(t, sigFile); len(got) != 1 || got[0] != "TERM" {
		t.Fatalf("child signals %q, want exactly one TERM", got)
	}
	if !strings.Contains(e.String(), "no signal forwarded") {
		t.Fatalf("SIGTERM not noted in log: %q", e.String())
	}
}

func TestNoSchedulingState(t *testing.T) {
	// The adapter has no clock: two runs with the same input are identical.
	argv, _ := fakePgReview(t, 0, "")
	for range 2 {
		do("run", "2026-11-05T07:00:00Z", "id", "l", "s", "24h")
	}
	got := readLines(t, argv)
	half := len(got) / 2
	if half == 0 || strings.Join(got[:half], " ") != strings.Join(got[half:], " ") {
		t.Fatalf("runs differ: %q", got)
	}
}
