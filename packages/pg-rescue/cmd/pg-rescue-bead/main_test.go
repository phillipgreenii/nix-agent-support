package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/contracttest"
	"github.com/phillipgreenii/pg-rescue/internal/inproc"
	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// TestMain re-executes the test binary as the real pg-rescue-bead main (the
// GO_WANT_HELPER_PROCESS pattern used across this repo) for the one test that
// needs a genuine process, the orphan test. Every other test calls run in
// process through inproc.Run: a race-built child costs a flat 1.0 s at exit.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		for i, a := range os.Args {
			if a == "--" {
				os.Args = append([]string{"pg-rescue-bead"}, os.Args[i+1:]...)
				break
			}
		}
		main()
		return
	}
	os.Exit(testenv.Run(m))
}

// handlerArgv is the argv that runs the real main as a process with args.
func handlerArgv(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^$", "--"}, args...)
}

var helperEnv = []string{"GO_WANT_HELPER_PROCESS=1"}

// runHandler runs the handler in process against a canned report.
func runHandler(t *testing.T, fixture string, env []string, args ...string) contracttest.Result {
	t.Helper()
	return inproc.Run(t, run, args, fixture, env)
}

func trackerDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestContractAgainstEveryCannedReport(t *testing.T) {
	for _, fixture := range []string{"first-attempt", "three-prior-attempts", "stdin-mode"} {
		t.Run(fixture, func(t *testing.T) {
			fake := testenv.NewFakeConnector(t)
			tracker := trackerDir(t)
			res := runHandler(t, contracttest.Fixture(fixture), fake.Env(), "--tracker-dir", tracker, "--dedup-query", "pg-rescue-open")
			if res.Exit != 3 || res.Outcome != contract.Deferred {
				t.Fatalf("exit %d outcome %s reason %q stderr %q", res.Exit, res.Outcome, res.Reason, res.Stderr)
			}
			if res.Reported.Summary != "Created pg2-new1" || !strings.Contains(string(res.Reported.Meta), `"item_id":"pg2-new1"`) || !strings.Contains(string(res.Reported.Meta), `"action":"created"`) {
				t.Errorf("reported %+v", res.Reported)
			}
			calls := fake.Calls()
			if len(calls) != 2 || calls[0].Verb() != "list" || calls[1].Verb() != "create" {
				t.Fatalf("calls %v", calls)
			}
			for _, c := range calls {
				if c.TrackerDir != tracker || !strings.HasPrefix(c.Actor, "pg-rescue/") {
					t.Errorf("%s: tracker %q actor %q", c.Verb(), c.TrackerDir, c.Actor)
				}
			}
		})
	}
}

func TestUnknownSchemaVersionExits1(t *testing.T) {
	fake := testenv.NewFakeConnector(t)
	res := runHandler(t, contracttest.Fixture("unknown-schema-version"), fake.Env(), "--tracker-dir", trackerDir(t))
	if res.Exit != 1 || res.Outcome != contract.Failed || !strings.Contains(res.Stderr, "schema_version 2") || len(fake.Calls()) != 0 {
		t.Errorf("exit %d outcome %s stderr %q calls %v", res.Exit, res.Outcome, res.Stderr, fake.Calls())
	}
}

func TestDerivedTrackerAndUnreachableTrackerThroughTheContract(t *testing.T) {
	t.Run("no .beads and no --tracker-dir exits 1", func(t *testing.T) {
		fake := testenv.NewFakeConnector(t)
		// The canned report's cwd (/abs/repo) does not exist, so there is no
		// repository to derive a tracker from.
		res := runHandler(t, contracttest.Fixture("first-attempt"), fake.Env())
		if res.Exit != 1 || !strings.Contains(res.Stderr, "--tracker-dir") || len(fake.Calls()) != 0 {
			t.Errorf("exit %d stderr %q calls %v", res.Exit, res.Stderr, fake.Calls())
		}
	})
	t.Run("an unreachable tracker exits 1", func(t *testing.T) {
		fake := testenv.NewFakeConnector(t)
		fake.Fail("create", 1, "bd unreachable")
		res := runHandler(t, contracttest.Fixture("first-attempt"), fake.Env(), "--tracker-dir", trackerDir(t))
		if res.Exit != 1 || res.Stdout != "" || !strings.Contains(res.Stderr, "bd unreachable") {
			t.Errorf("exit %d stdout %q stderr %q", res.Exit, res.Stdout, res.Stderr)
		}
	})
}

func TestUsageErrorIsNotTheDeclinedExitCode(t *testing.T) {
	res := runHandler(t, contracttest.Fixture("first-attempt"), nil, "--no-such-flag")
	if res.Exit != 1 {
		t.Errorf("exit %d (2 would be read as declined)", res.Exit)
	}
}

// TestOrphanedHandlerKillsItsChildAndExits is the real thing: the wrapper is
// SIGKILLed while pg-connector is in flight, and the handler must notice,
// kill pg-connector and exit rather than run on unsupervised.
func TestOrphanedHandlerKillsItsChildAndExits(t *testing.T) {
	fake := testenv.NewFakeConnector(t)
	fake.Block("create")
	reportPath := contracttest.Fixture("first-attempt")
	argv := handlerArgv("--tracker-dir", trackerDir(t))

	// "wrapper" is a shell that starts the handler in the background and
	// waits; killing the shell orphans the handler.
	wrapper := exec.Command("/bin/sh", append([]string{"-c", `"$@" </dev/null >/dev/null 2>&1 & wait`, "sh"}, argv...)...)
	wrapper.Env = append(append(os.Environ(), helperEnv...), append(fake.Env(), "PG_RESCUE_REPORT="+reportPath)...)
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := wrapper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-wrapper.Process.Pid, syscall.SIGKILL); _ = wrapper.Wait() })

	started := filepath.Join(fake.Dir, "started")
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	handlerPid := strings.TrimSpace(read(t, started))
	childPid := strings.TrimSpace(read(t, filepath.Join(fake.Dir, "started.child")))

	// Kill only the shell (not its process group), orphaning the handler.
	if err := syscall.Kill(wrapper.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = wrapper.Wait()

	waitFor(t, func() bool { return !alive(handlerPid) })
	waitFor(t, func() bool { return !alive(childPid) })
}

func alive(pid string) bool { return exec.Command("kill", "-0", pid).Run() == nil }

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
