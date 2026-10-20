package runner_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

// waitFile polls up to five seconds for path and returns its trimmed content.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	for i := 0; i < 250; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
	return ""
}

func pidOf(t *testing.T, path string) int {
	t.Helper()
	n, err := strconv.Atoi(waitFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunStdinFeedsStdinAndAppendsExtraEnvAfterVerification(t *testing.T) {
	r := testutil.NewRunner(t)
	dir := t.TempDir()
	sh := testutil.WriteScript(t, dir, "echo.sh", `read line; echo "got:$line:$STAND_IN"`)
	res, err := r.RunStdin(context.Background(), 10*time.Second, "hello\n", []string{"STAND_IN=x"}, sh)
	if err != nil || res.Exit != 0 {
		t.Fatalf("RunStdin: %v %+v", err, res)
	}
	if got := strings.TrimSpace(res.Stdout); got != "got:hello:x" {
		t.Errorf("stdout = %q", got)
	}
	// The extra variable is not on the allowlist, yet it is accepted: only the
	// BASE environment is verified. A bad base environment is still refused.
	r.Env = append(r.Env, "STRAY=1")
	if _, err := r.RunStdin(context.Background(), 10*time.Second, "", []string{"STAND_IN=x"}, sh); !errors.Is(err, runner.ErrSafety) {
		t.Fatalf("a stray base variable must be refused with ErrSafety, got %v", err)
	}
}

func TestRunStillReportsANonZeroExitAsAResult(t *testing.T) {
	r := testutil.NewRunner(t)
	sh := testutil.WriteScript(t, t.TempDir(), "exit3.sh", `echo out; echo err >&2; exit 3`)
	res, err := r.Run(context.Background(), 10*time.Second, sh)
	if err != nil || res.Exit != 3 || strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("Run = %+v, %v", res, err)
	}
}

// drainScript is a stand-in long-lived child: it records its pid and a
// grandchild's, announces "started" only once its trap is installed and both
// pids are recorded, and drains ("drained", exit 0) on SIGTERM. The group-wide
// SIGTERM also kills the grandchild, which makes `wait` return before the trap
// has run, so it waits in a loop: the pending trap then always runs at the next
// command boundary, whichever signal or child death the shell sees first.
// "started" also waits for the grandchild to have exec'd sleep: a SIGTERM that
// lands between the shell's fork and that exec is consumed by the forked
// shell's inherited trap handler, so the grandchild would survive it (an
// artefact of the shell stand-in, not of Stop).
func drainScript(dir string) string {
	return `echo $$ > "` + dir + `/serve.pid"
trap 'echo drained; exit 0' TERM
sleep 300 &
echo $! > "` + dir + `/grandchild.pid"
until [ "$(ps -o comm= -p $!)" = sleep ]; do :; done
echo started
while :; do wait; done
`
}

// syncBuffer is a concurrency-safe io.Writer that is NOT an *os.File, so the
// runner copies the child's output through a pipe and a copier goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestStartRunsALongLivedChildAndStopEndsItOnSIGTERMWithNoOrphan(t *testing.T) {
	r := testutil.NewRunner(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "child.out")
	logFile, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	r.ChildOut = logFile
	sh := testutil.WriteScript(t, dir, "serve.sh", drainScript(dir))
	c, err := r.Start(context.Background(), sh)
	if err != nil {
		t.Fatal(err)
	}
	// "started" is written after the trap is installed and both pids are
	// recorded, so SIGTERM below can only meet a child ready to drain.
	if got := waitFile(t, outPath); got != "started" {
		t.Fatalf("first child output = %q, want started", got)
	}
	pid := pidOf(t, filepath.Join(dir, "serve.pid"))
	grand := pidOf(t, filepath.Join(dir, "grandchild.pid"))
	select {
	case <-c.Done():
		t.Fatal("the child must keep running after Start returns")
	default:
	}
	if err := c.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done must be closed once Stop returned")
	}
	if c.ExitCode() != 0 {
		t.Errorf("a stand-in that traps SIGTERM exits 0, got %d", c.ExitCode())
	}
	if !testutil.ProcessGone(pid) || !testutil.ProcessGone(grand) {
		t.Errorf("orphan left behind: child %d or grandchild %d still exists", pid, grand)
	}
	out, _ := os.ReadFile(outPath)
	if string(out) != "started\ndrained\n" {
		t.Errorf("ChildOut = %q: want the child's stdout, including the SIGTERM drain", out)
	}
	if err := c.Stop(time.Second); err != nil {
		t.Errorf("a second Stop must be a no-op, got %v", err)
	}
}

// Stop must not return before the child's output has been copied out: the
// daemon supervisor reads the daemon log after Stop. A non-file ChildOut goes
// through a copier goroutine, which Wait (and so Done and Stop) must outlast.
func TestStopReturnsOnlyAfterTheChildOutputIsFullyCopied(t *testing.T) {
	r := testutil.NewRunner(t)
	dir := t.TempDir()
	buf := &syncBuffer{}
	r.ChildOut = buf
	sh := testutil.WriteScript(t, dir, "serve.sh", drainScript(dir))
	c, err := r.Start(context.Background(), sh)
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(dir, "grandchild.pid"))
	for i := 0; i < 250 && !strings.Contains(buf.String(), "started"); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := buf.String(); got != "started\ndrained\n" {
		t.Errorf("output read right after Stop = %q, want everything the child wrote", got)
	}
}

func TestStopKillsAChildThatIgnoresSIGTERMAndSaysSo(t *testing.T) {
	r := testutil.NewRunner(t)
	dir := t.TempDir()
	sh := testutil.WriteScript(t, dir, "stubborn.sh", `trap '' TERM
echo $$ > "`+dir+`/serve.pid"
while :; do sleep 1; done
`)
	c, err := r.Start(context.Background(), sh)
	if err != nil {
		t.Fatal(err)
	}
	pid := pidOf(t, filepath.Join(dir, "serve.pid"))
	err = c.Stop(200 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("Stop of a child that ignores SIGTERM must report the kill, got %v", err)
	}
	if !testutil.ProcessGone(pid) {
		t.Errorf("child %d survived SIGKILL", pid)
	}
}

func TestStartRefusesAnUnsafeEnvironmentAndNeverLaunches(t *testing.T) {
	r := testutil.NewRunner(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	sh := testutil.WriteScript(t, dir, "mark.sh", `echo x > "`+marker+`"`)
	r.Env = append(r.Env, "STRAY_VARIABLE=1")
	if _, err := r.Start(context.Background(), sh); !errors.Is(err, runner.ErrSafety) {
		t.Fatalf("Start must refuse with ErrSafety, got %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the child ran despite the safety refusal")
	}
}

func TestStartWithACancelledContextDoesNotLaunch(t *testing.T) {
	r := testutil.NewRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Start(ctx, "/bin/sleep", "300"); err == nil {
		t.Fatal("Start with a cancelled context must fail")
	}
}

func TestStopAfterTheChildExitedOnItsOwnIsANoOp(t *testing.T) {
	r := testutil.NewRunner(t)
	sh := testutil.WriteScript(t, t.TempDir(), "quick.sh", `exit 7`)
	c, err := r.Start(context.Background(), sh)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done never closed for a child that exited")
	}
	if c.ExitCode() != 7 {
		t.Errorf("ExitCode = %d, want 7", c.ExitCode())
	}
	if err := c.Stop(time.Second); err != nil {
		t.Errorf("Stop of an exited child = %v", err)
	}
}
