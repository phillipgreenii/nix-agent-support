//go:build integration

package watchdog

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/usage"
	"github.com/phillipgreenii/x/gitclient"
)

// These tests hold a deliberately wedged git process (a sleep that outlives the
// bound under test) until gitCallTimeout kills it, so each waits out the
// bound in real time (about 3 s) regardless of how fast the stubs are. A
// wall-clock timeout bound is the point of the test, so it cannot be trimmed
// without weakening it; the tests therefore run in
// checks.<system>.pg-router-handlers-integration-tests, not the unit tier.

// wedgeSleepDuration matches the fixed sleep inside the wrapper script
// writeWedgedGit generates below: how long the fake wedged git process
// blocks when invoked with the wedged verb.
const wedgeSleepDuration = 30 * time.Second

// boundedCallMargin is subtracted from wedgeSleepDuration to build
// boundedCallElapsedCeiling below. It is a fixed fraction of the wedge, since
// the wedged tests shrink gitCallTimeout (see shrinkGitCallTimeout).
const boundedCallMargin = wedgeSleepDuration / 3

// wedgedCallTimeout replaces gitCallTimeout for the wedged-git tests: the kill
// path is the same at any bound, and the production 10 s would make each of
// them wait out a real ten seconds. Not smaller than a few seconds: on a loaded
// host the wrapper shell can take over a second to start and write its pid
// file, and a bound that fires before that fails the test for the wrong reason.
const wedgedCallTimeout = 3 * time.Second

func shrinkGitCallTimeout(t *testing.T) {
	t.Helper()
	prev := gitCallTimeout
	gitCallTimeout = wedgedCallTimeout
	t.Cleanup(func() { gitCallTimeout = prev })
}

// boundedCallElapsedCeiling is the ceiling TestSafeToReset_boundsWedgedToplevelProbe
// and TestTerminal_boundsWedgedReset assert elapsed time against: any value
// under it (and therefore comfortably under wedgeSleepDuration) proves the
// call was actually bounded by gitCallTimeout rather than merely fast, while
// leaving enough headroom to absorb machine load (pg2-rz5ud: a genuinely
// 10s-bounded call once measured over twenty seconds under load, which a
// prior literal wall-clock-speed ceiling -- pinned to speed rather than the
// wedge -- wrongly failed).
const boundedCallElapsedCeiling = wedgeSleepDuration - boundedCallMargin

// wedgePIDFile returns the path writeWedgedGit's wrapper script records the
// wedged process's PID in. A caller proves the wedge was KILLED (not merely
// outrun) after a bounded call returns by asserting that process is gone,
// via assertWedgeKilled.
func wedgePIDFile(wedgedGitPath string) string {
	return filepath.Join(filepath.Dir(wedgedGitPath), "wedge.pid")
}

// assertWedgeKilled fails t unless the wedged git started by writeWedgedGit
// really began its sleep AND is no longer running.
//
// Mechanism note (why not a completion marker): an earlier version had the
// wrapper run `sleep 30; touch <marker>` under /bin/sh and asserted the
// marker's absence. That flaked once under heavy host load (load average
// 45): the marker existed after a 10.66s test although a 30s sleep cannot
// complete in 10s, so the sleep child ended early (by error or stray signal
// from outside the test) without ending the shell, which then fell through
// to `touch`. The exact trigger was NOT reproduced (about 70 wedged calls
// at load average 50 to 120 never flaked), but the structure is the
// defect: a shell step trailing the sleep turns the marker into a function
// of anything that ends the sleep early, so it cannot prove the kill. The
// wrapper now records its PID and `exec`s sleep, so the PID is the sleep
// process itself and no shell step can follow it; the assertion is only
// "that PID is gone", which holds when gitCallTimeout's process-group kill
// removed it (the callers' elapsed ceiling rules out the sleep's natural
// end). A PID that now names a different command (PID reuse) counts as
// gone.
func assertWedgeKilled(t *testing.T, wedgedGitPath string) {
	t.Helper()
	raw, err := os.ReadFile(wedgePIDFile(wedgedGitPath))
	if err != nil {
		t.Fatalf("wedged git never started its wedge (no pid file): %v", err)
	}
	pid := strings.TrimSpace(string(raw))
	out, _ := exec.CommandContext(t.Context(), "ps", "-o", "command=", "-p", pid).Output()
	if strings.Contains(string(out), "sleep") {
		t.Errorf("wedged git process %s is still running (%q): it was not killed by gitCallTimeout", pid, strings.TrimSpace(string(out)))
	}
}

// writeWedgedGit writes a `git` wrapper script at a fresh temp dir that
// records its PID in wedgePIDFile and then `exec`s sleep for
// wedgeSleepDuration -- far longer than any bound asserted below -- when
// invoked with exactly verb as its first two arguments (e.g. "reset
// --hard" or "rev-parse --show-toplevel"), and execs the REAL git
// (resolved via exec.LookPath before this helper runs) for every other
// invocation -- notably a client's own construction-time validation probe
// (`rev-parse --path-format=absolute --git-common-dir`), which must stay
// fast so the client this wrapper backs can still be constructed. Callers
// verify the kill with assertWedgeKilled. Returns the wrapper's absolute
// path, for gitclient.WithGit.
func writeWedgedGit(t *testing.T, verb string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	content := fmt.Sprintf(
		"#!/bin/sh\nif [ \"$1 $2\" = \"%s\" ]; then echo $$ > \"%s\"; exec sleep %d; fi\nexec \"%s\" \"$@\"\n",
		verb, wedgePIDFile(script), int(wedgeSleepDuration.Seconds()), realGit,
	)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("writing wedged git wrapper: %v", err)
	}
	return script
}

// TestSafeToReset_boundsWedgedToplevelProbe verifies pg2-yy42's bounded-
// probe requirement survives the x/gitclient migration (pg2-ljyaj) for the
// Locator role: safeToReset's worktree-root backstop calls
// Locator.Toplevel through openGit, bounded by gitCallTimeout. A git
// invocation still genuinely running when that bound fires must be killed
// PROMPTLY -- bounded by gitclient's own WaitDelay ceiling on top of the
// timeout, not left to run to completion (here, 30s) -- and safeToReset
// must fall back to its safe default (false) rather than hang the hard-stop
// sequence.
func TestSafeToReset_boundsWedgedToplevelProbe(t *testing.T) {
	shrinkGitCallTimeout(t)
	wedged := writeWedgedGit(t, "rev-parse --show-toplevel")

	restore := openGit
	openGit = func(ctx context.Context, dir string) (gitLocatorCleaner, error) {
		return gitclient.New(ctx, dir, gitclient.WithGit(wedged))
	}
	defer func() { openGit = restore }()

	target := initFixtureRepo(t, "target-branch").Dir

	start := time.Now()
	got := safeToReset(context.Background(), target, "/does/not/exist", filepath.Dir(target))
	elapsed := time.Since(start)

	if got {
		t.Error("safeToReset must return false when the toplevel probe cannot complete within gitCallTimeout")
	}
	// Bound: comfortably under wedgeSleepDuration (boundedCallElapsedCeiling,
	// derived from gitCallTimeout) rather than a wall-clock-speed literal --
	// any elapsed time under the wedge proves the probe was actually killed
	// by gitCallTimeout, and the margin absorbs machine load that a tighter
	// bound cannot (pg2-rz5ud).
	if elapsed > boundedCallElapsedCeiling {
		t.Errorf("safeToReset did not bound the wedged toplevel probe: took %s (want well under the %s wedge)", elapsed, wedgeSleepDuration)
	}
	// Outcome: the wedge must have been KILLED, not merely outrun -- see
	// assertWedgeKilled.
	assertWedgeKilled(t, wedged)
}

// TestTerminal_boundsWedgedReset is TestSafeToReset_boundsWedgedToplevelProbe's
// counterpart for the Cleaner role: terminal's guarded hard-stop reset calls
// Cleaner.ResetHard through openGit, now bounded by gitCallTimeout -- a
// bound this call site did NOT have before this migration (it previously
// relied solely on the caller's ambient ctx). A wedged `reset --hard` must
// be killed promptly and terminal must still reach its bead
// comment/unclaim steps, rather than hang the hard-stop sequence
// indefinitely (pg2-yy42).
func TestTerminal_boundsWedgedReset(t *testing.T) {
	shrinkGitCallTimeout(t)
	wedged := writeWedgedGit(t, "reset --hard")

	restore := openGit
	openGit = func(ctx context.Context, dir string) (gitLocatorCleaner, error) {
		return gitclient.New(ctx, dir, gitclient.WithGit(wedged))
	}
	defer func() { openGit = restore }()

	repo := initFixtureRepo(t, "wt-branch").Dir

	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", CWD: repo}}}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, tokBudget(1000))
	wd.RepoRoot = "/does/not/exist"
	wd.WorktreeDir = filepath.Dir(repo)

	start := time.Now()
	wd.terminal(context.Background(), "s", "zr-1", &BudgetError{})
	elapsed := time.Since(start)

	// Bound: see TestSafeToReset_boundsWedgedToplevelProbe's identical
	// comment above -- boundedCallElapsedCeiling is derived from
	// gitCallTimeout rather than a wall-clock-speed literal, so it survives
	// machine load (pg2-rz5ud).
	if elapsed > boundedCallElapsedCeiling {
		t.Errorf("terminal did not bound a wedged reset --hard: took %s (want well under the %s wedge)", elapsed, wedgeSleepDuration)
	}
	// Outcome: the wedged reset --hard must have been KILLED, not merely
	// outrun -- see assertWedgeKilled.
	assertWedgeKilled(t, wedged)
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("terminal must still reach bd unclaim despite a wedged reset --hard: calls=%v", bd.calls)
	}
}
