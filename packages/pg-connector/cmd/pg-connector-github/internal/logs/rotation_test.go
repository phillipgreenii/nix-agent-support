package logs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

const (
	fiveMiB = 5 << 20
	oneMiB  = 1 << 20
)

// seed writes n bytes of whole JSONL lines to path (n > 0), each line
// `{"seed":<i>,"pad":"xxx"}`, and returns the number of lines written.
func seed(t *testing.T, path string, n int) int {
	t.Helper()
	var b bytes.Buffer
	lines := 0
	for b.Len() < n {
		fmt.Fprintf(&b, "{\"seed\":%d,\"pad\":\"%s\"}\n", lines, strings.Repeat("x", 200))
		lines++
	}
	// Trim to exactly n bytes by padding the last line's pad, so a boundary
	// test can sit exactly at the threshold.
	if over := b.Len() - n; over > 0 {
		raw := b.Bytes()
		raw = append(raw[:len(raw)-over-1], '\n') // drop `over` bytes of the last line, keep its newline
		b = *bytes.NewBuffer(raw)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return lines
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(raw, []byte("\n"))
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// names lists the entries of dir.
func names(t *testing.T, dir string) []string {
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

// assertOneArchive checks that dir holds the live log, exactly one archive
// named <live>.1, the flock file, nothing else, and that only the live file
// matches *.jsonl.
func assertOneArchive(t *testing.T, dir, live string) {
	t.Helper()
	want := map[string]bool{live: true, live + ".1": true, live + ".lock": true}
	for _, n := range names(t, dir) {
		if !want[n] {
			t.Errorf("unexpected file %q in %v", n, names(t, dir))
		}
		matches, _ := filepath.Match("*.jsonl", n)
		if matches != (n == live) {
			t.Errorf("%q: matches *.jsonl = %v; only the live file %q may", n, matches, live)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, live+".1")); err != nil {
		t.Errorf("no archive %s.1: %v", live, err)
	}
}

// Each log rotates past exactly its limit and not at it: 5 MiB for the event
// log and the structured log, 1 MiB for the client log.
func TestEachLogRotatesPastItsLimit(t *testing.T) {
	appenders := map[string]struct {
		live  string
		limit int
		add   func(dir string)
	}{
		"event log": {config.EventLogFileName, fiveMiB, func(dir string) {
			NewEventLog(dir, "v").OnSpend(governor.SpendEvent{At: t0, Bucket: governor.BucketGraphQL, Purpose: governor.PurposeQuery, Cost: 1, Remaining: -1})
		}},
		"structured log": {config.DaemonLogFileName, fiveMiB, func(dir string) {
			NewStructuredLog(dir, "v").Log("daemon_started")
		}},
		"client log": {config.ClientLogFileName, oneMiB, func(dir string) {
			NewClientLog(dir, "v").DaemonUnreachable("pr", "list", t0, "daemon down")
		}},
	}
	for name, a := range appenders {
		t.Run(name, func(t *testing.T) {
			// At the limit: appended to, not rotated.
			dir := t.TempDir()
			live := filepath.Join(dir, a.live)
			seeded := seed(t, live, a.limit)
			if got := fileSize(t, live); got != int64(a.limit) {
				t.Fatalf("seed size %d, want exactly %d", got, a.limit)
			}
			a.add(dir)
			if _, err := os.Stat(live + ".1"); !os.IsNotExist(err) {
				t.Fatalf("a log AT its limit must not rotate: %v", err)
			}
			if got := lineCount(t, live); got != seeded+1 {
				t.Fatalf("lines %d, want %d", got, seeded+1)
			}

			// One byte past: rotated once, the new line starts a fresh file.
			dir = t.TempDir()
			live = filepath.Join(dir, a.live)
			seeded = seed(t, live, a.limit+1)
			a.add(dir)
			assertOneArchive(t, dir, a.live)
			if got := lineCount(t, live); got != 1 {
				t.Errorf("the fresh log holds the new line only, got %d lines", got)
			}
			if got := lineCount(t, live+".1"); got != seeded {
				t.Errorf("the archive holds the old lines: %d, want %d", got, seeded)
			}
			if got := fileSize(t, live+".1"); got <= int64(a.limit) {
				t.Errorf("archive size %d must exceed the limit %d", got, a.limit)
			}

			// A second rotation replaces the archive: still exactly one.
			seed(t, live, a.limit+1)
			a.add(dir)
			assertOneArchive(t, dir, a.live)
		})
	}
}

func TestLimitsAreTheDocumentedOnes(t *testing.T) {
	dir := t.TempDir()
	if got := NewEventLog(dir, "v").MaxBytes; got != fiveMiB {
		t.Errorf("event log limit %d", got)
	}
	if got := NewStructuredLog(dir, "v").MaxBytes; got != fiveMiB {
		t.Errorf("structured log limit %d", got)
	}
	if got := NewClientLog(dir, "v").MaxBytes; got != oneMiB {
		t.Errorf("client log limit %d", got)
	}
}

// The three live names are distinct, inside the state directory and the ones
// the doc comment records; no archive or lock name can match a *.jsonl glob.
func TestFileNamesAreDistinctAndNoArchiveMatchesTheJSONLGlob(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{"events": EventLogPath(dir), "daemon": DaemonLogPath(dir), "client": ClientLogPath(dir)}
	seen := map[string]string{}
	for what, p := range paths {
		if filepath.Dir(p) != dir {
			t.Errorf("%s log %q is outside the state directory", what, p)
		}
		base := filepath.Base(p)
		if prev, dup := seen[base]; dup {
			t.Errorf("%s and %s share the file name %q", what, prev, base)
		}
		seen[base] = what
		if ok, _ := filepath.Match("*.jsonl", base); !ok {
			t.Errorf("live log %q must match the *.jsonl glob", base)
		}
		for _, sfx := range []string{".1", ".lock"} {
			if ok, _ := filepath.Match("*.jsonl", base+sfx); ok {
				t.Errorf("%q matches *.jsonl", base+sfx)
			}
		}
	}
	if len(seen) != 3 {
		t.Errorf("three distinct names expected: %v", seen)
	}
	if ServiceName == ClientServiceName {
		t.Error("the client log has its own service_name")
	}
}

// Two clients appending to an over-size client log at once rotate it once and
// lose no line (goroutines, each with its own open file, contend on the flock
// exactly as processes do).
func TestConcurrentClientsRotateTheClientLogOnce(t *testing.T) {
	const writers = 24
	dir := t.TempDir()
	live := ClientLogPath(dir)
	seeded := seed(t, live, oneMiB+1)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			NewClientLog(dir, "v").DaemonUnreachable("pr", fmt.Sprintf("op%d", i), t0, "daemon down")
		}()
	}
	close(start)
	wg.Wait()

	assertOneArchive(t, dir, config.ClientLogFileName)
	if got := lineCount(t, live) + lineCount(t, live+".1"); got != seeded+writers {
		t.Errorf("lost or duplicated lines: %d on disk, want %d", got, seeded+writers)
	}
	if got := lineCount(t, live+".1"); got < seeded {
		t.Errorf("the archive must hold the whole seed (%d lines), has %d", seeded, got)
	}
	if len(readRows(t, live)) == 0 {
		t.Fatal("the fresh log is empty")
	}
}

// The same with real processes: N copies of this test binary append at once.
func TestConcurrentClientProcessesRotateTheClientLogOnce(t *testing.T) {
	const procs = 8
	dir := t.TempDir()
	live := ClientLogPath(dir)
	seeded := seed(t, live, oneMiB+1)

	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestHelperClientProcess$")
		cmds[i].Env = append(os.Environ(), "LOGS_TEST_HELPER_DIR="+dir, fmt.Sprintf("LOGS_TEST_HELPER_OP=op%d", i))
		outs[i] = &bytes.Buffer{}
		cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
	}
	for i, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", i, err, outs[i])
		}
	}
	assertOneArchive(t, dir, config.ClientLogFileName)
	if got := lineCount(t, live) + lineCount(t, live+".1"); got != seeded+procs {
		t.Errorf("lost or duplicated lines: %d on disk, want %d", got, seeded+procs)
	}
}

// TestHelperClientProcess is the child of the process test; it does nothing in
// a normal run.
func TestHelperClientProcess(t *testing.T) {
	dir := os.Getenv("LOGS_TEST_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	NewClientLog(dir, "v").DaemonUnreachable("pr", os.Getenv("LOGS_TEST_HELPER_OP"), time.Now(), "daemon down")
}

// Nothing about where a log goes comes from the environment: with every
// variable a path could be derived from pointed at a decoy directory, the
// logs still land in the state directory and nothing is written to the decoy.
func TestNoLogPathIsReadFromTheEnvironment(t *testing.T) {
	decoy := t.TempDir()
	for _, k := range []string{"XDG_STATE_HOME", "HOME", "TMPDIR", "PG_CONNECTOR_GITHUB_EVENTS_FILE", "PG_CONNECTOR_PR_GITHUB_EVENTS_FILE", config.EnvStateDir} {
		t.Setenv(k, decoy)
	}
	dir := t.TempDir()
	NewEventLog(dir, "v").OnSpend(governor.SpendEvent{At: t0, Bucket: governor.BucketGraphQL, Cost: 1, Remaining: -1})
	NewStructuredLog(dir, "v").Log("daemon_started")
	NewClientLog(dir, "v").DaemonUnreachable("pr", "list", t0, "down")
	for _, f := range []string{config.EventLogFileName, config.DaemonLogFileName, config.ClientLogFileName} {
		if got := lineCount(t, filepath.Join(dir, f)); got != 1 {
			t.Errorf("%s: %d lines in the state directory", f, got)
		}
	}
	if got := names(t, decoy); len(got) != 0 {
		t.Errorf("something was written where the environment points: %v", got)
	}

	// And with the environment empty, as a daemon started with no HOME sees it.
	for _, k := range []string{"XDG_STATE_HOME", "HOME", "PG_CONNECTOR_GITHUB_EVENTS_FILE"} {
		t.Setenv(k, "")
	}
	dir = t.TempDir()
	NewStructuredLog(dir, "v").Log("daemon_started")
	if got := lineCount(t, DaemonLogPath(dir)); got != 1 {
		t.Errorf("an empty environment still logs: %d lines", got)
	}
}
