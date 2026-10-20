package runner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestStartRunsALongLivedChildAndStopEndsItOnSIGTERMWithNoOrphan(t *testing.T) {
	r := testutil.NewRunner(t)
	dir := t.TempDir()
	logFile, err := os.Create(filepath.Join(dir, "child.out"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	r.ChildOut = logFile
	// The stand-in records its own pid and the pid of a grandchild, announces
	// itself, and exits 0 on SIGTERM.
	sh := testutil.WriteScript(t, dir, "serve.sh", `echo $$ > "`+dir+`/serve.pid"
trap 'echo drained; exit 0' TERM
sleep 300 &
echo $! > "`+dir+`/grandchild.pid"
echo started
wait
`)
	c, err := r.Start(context.Background(), sh)
	if err != nil {
		t.Fatal(err)
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
	out, _ := os.ReadFile(logFile.Name())
	if !strings.Contains(string(out), "started") || !strings.Contains(string(out), "drained") {
		t.Errorf("ChildOut = %q: want the child's stdout, including the SIGTERM drain", out)
	}
	if err := c.Stop(time.Second); err != nil {
		t.Errorf("a second Stop must be a no-op, got %v", err)
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
