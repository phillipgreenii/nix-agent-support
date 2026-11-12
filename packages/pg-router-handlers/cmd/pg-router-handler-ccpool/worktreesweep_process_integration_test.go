//go:build integration

package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
)

// The bead's acceptance test: kill the handler between isolation.Ensure and
// CC.Ensure, then assert the next dispatch reclaims. The "handler" is a real
// child process that holds the per-bead worktree lock as run() does; SIGKILL
// drops it with the process.
func TestSweep_killedHandlerBetweenWorktreeAndSession_reclaimedOnNextDispatch(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperHoldWorktreeLock$")
	// The helper never runs git, so it gets a minimal environment: nothing
	// inherited (in particular no GIT_*), only what it reads.
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, "PGR_HELPER_HOLD_LOCK=1", "PGR_HELPER_LOCK_DIR="+h.lockDir, "PGR_HELPER_BEAD=zr-k")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "locked") {
		t.Fatalf("helper did not report holding the lock: %q %v", line, err)
	}

	if got := h.sweep(); got != 0 {
		t.Fatalf("the live handler's worktree must not be swept; removed %d", got)
	}
	h.wantKept(path, "zr-k")

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	if got := h.sweep(); got != 1 {
		t.Fatalf("after the handler was killed the sweep removed %d, want 1", got)
	}
	if h.exists(path) || h.branchExists("zr-k") {
		t.Errorf("worktree and branch must be reclaimed; worktree exists=%v branch exists=%v", h.exists(path), h.branchExists("zr-k"))
	}
}

// TestHelperHoldWorktreeLock is not a test: it is the child process of the kill
// test above, a stand-in for a handler parked between isolation.Ensure and
// CC.Ensure. It exits immediately when run as a normal test.
func TestHelperHoldWorktreeLock(t *testing.T) {
	if os.Getenv("PGR_HELPER_HOLD_LOCK") != "1" {
		t.Skip("helper process only")
	}
	l, err := sessionlock.TryRLock(os.Getenv("PGR_HELPER_LOCK_DIR"), sessionlock.WorktreeKey(os.Getenv("PGR_HELPER_BEAD")))
	if err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(2)
	}
	_ = l
	os.Stdout.WriteString("locked\n")
	select {} // until killed
}

// End to end with the production probe: a REAL process whose working directory is
// the worktree (what the spared claude is), found by the real lsof. Reclaimed
// once the process is gone.
func TestSweep_realProcessInTheWorktreeIsFoundByLsof(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.env.cwds = processCWDs

	cmd := exec.CommandContext(t.Context(), "sleep", "300")
	cmd.Dir = path
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// lsof can lag the exec by an instant; poll briefly until it sees the child.
	deadline := time.Now().Add(10 * time.Second)
	for {
		cwds, err := processCWDs(context.Background())
		if err != nil {
			t.Skipf("lsof cannot list working directories here: %v", err)
		}
		if worktreeHeldByProcess(cwds, leakedWorktree{path: path}) {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("lsof did not report the child's working directory (restricted environment)")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got := h.sweep(); got != 0 {
		t.Fatalf("removed %d while a real process ran in the worktree, want 0", got)
	}
	h.wantKept(path, "zr-k")

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if got := h.sweep(); got != 1 || h.exists(path) {
		t.Errorf("after the process exited: removed=%d exists=%v, want reclaimed", got, h.exists(path))
	}
}
