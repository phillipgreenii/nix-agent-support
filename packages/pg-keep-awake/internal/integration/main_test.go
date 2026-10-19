// Package integration runs the real pg-keep-awake keeper, anchor, CLI and
// pg-caffeinate-shim, and the real /usr/bin/caffeinate, hermetically: every
// child gets a temp XDG_STATE_HOME and XDG_CONFIG_HOME and a file-backed
// battery. The suite skips itself, with one visible "SKIPPED integration:"
// line, on a host that cannot run caffeinate and observe it through pmset.
package integration

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	caffeinatePath = "/usr/bin/caffeinate"
	pmsetPath      = "/usr/bin/pmset"

	// envSkip forces the skip path on a machine where the suite normally runs.
	envSkip = "PG_KEEP_AWAKE_ITEST_SKIP"
	// envHelper selects a re-exec helper mode of this test binary.
	envHelper = "PGKA_ITEST_HELPER"
	// envHelperDir is the directory the holdlock helper takes leases/.lock in.
	envHelperDir = "PGKA_ITEST_LEASES"
)

var (
	// skipReason is computed once in TestMain; empty means the suite runs.
	skipReason string
	// keepAwakeBin and shimBin are the built binaries.
	keepAwakeBin string
	shimBin      string
)

func TestMain(m *testing.M) {
	if os.Getenv(envHelper) == "holdlock" {
		os.Exit(holdLockHelper(os.Getenv(envHelperDir)))
	}

	// A runner started under nohup has SIGHUP/SIGINT ignored, and children
	// inherit that. Holding a handler makes Go reset those signals to the
	// default in every child it execs.
	keep := make(chan os.Signal, 1)
	signal.Notify(keep, syscall.SIGHUP, syscall.SIGINT)

	os.Exit(func() int {
		skipReason = computeSkipReason()
		if line := skipLine(skipReason); line != "" {
			fmt.Println(line)
			return m.Run()
		}
		dir, err := os.MkdirTemp("", "pg-keep-awake-itest-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		goTool, err := exec.LookPath("go")
		if err != nil {
			goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
		}
		keepAwakeBin = filepath.Join(dir, "pg-keep-awake")
		shimBin = filepath.Join(dir, "pg-caffeinate-shim")
		for out, pkg := range map[string]string{
			keepAwakeBin: "../../cmd/pg-keep-awake",
			shimBin:      "../../cmd/pg-caffeinate-shim",
		} {
			build := exec.Command(goTool, "build", "-o", out, pkg)
			if msg, err := build.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, msg)
				return 1
			}
		}
		return m.Run()
	}())
}

// skipReason inputs, gathered from the host.
type hostFacts struct {
	goos            string
	caffeinateFound bool
	pmsetErr        error
	probeVisible    bool
	forced          bool
}

// computeSkipReason probes the host once. The probe holds a real caffeinate
// and waits for pmset to list it: a sandbox can run pmset yet never list the
// assertion, and the listing can lag by seconds even outside one.
func computeSkipReason() string {
	f := hostFacts{goos: runtime.GOOS, forced: os.Getenv(envSkip) != ""}
	if f.forced || f.goos != "darwin" {
		return skipReasonFor(f)
	}
	if _, err := exec.LookPath(caffeinatePath); err == nil {
		f.caffeinateFound = true
	}
	if !f.caffeinateFound {
		return skipReasonFor(f)
	}
	if err := exec.Command(pmsetPath, "-g", "assertions").Run(); err != nil {
		f.pmsetErr = err
		return skipReasonFor(f)
	}
	f.probeVisible = probeCaffeinateVisible()
	return skipReasonFor(f)
}

// skipReasonFor maps the host facts to the reason the suite skips, or "".
// The order is the order the checks are made.
func skipReasonFor(f hostFacts) string {
	switch {
	case f.forced:
		return "forced by " + envSkip
	case f.goos != "darwin":
		return "GOOS is " + f.goos + ", not darwin"
	case !f.caffeinateFound:
		return caffeinatePath + " not found"
	case f.pmsetErr != nil:
		return "pmset -g assertions failed: " + f.pmsetErr.Error()
	case !f.probeVisible:
		return "caffeinate assertions are not visible to pmset here"
	}
	return ""
}

// skipLine formats the one stdout line TestMain prints; an empty reason
// yields no line.
func skipLine(reason string) string {
	if reason == "" {
		return ""
	}
	return "SKIPPED integration: " + reason
}

// probeCaffeinateVisible starts /usr/bin/caffeinate -i -t 20 as a child and
// polls pmset every 200 ms for up to 10 seconds for an entry with its pid.
func probeCaffeinateVisible() bool {
	cmd := exec.Command(caffeinatePath, "-i", "-t", "20")
	if err := cmd.Start(); err != nil {
		return false
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if held(cmd.Process.Pid) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// holdLockHelper is the holdlock re-exec mode: take flock(LOCK_EX) on
// <dir>/.lock, print "locked" and block until killed.
func holdLockHelper(dir string) int {
	fd, err := unix.Open(filepath.Join(dir, ".lock"), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "holdlock open:", err)
		return 1
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		fmt.Fprintln(os.Stderr, "holdlock flock:", err)
		return 1
	}
	fmt.Println("locked")
	// Not select{}: with no other goroutine the runtime would report a
	// deadlock and exit, releasing the lock. Exit when the test binary dies.
	for ppid := os.Getppid(); os.Getppid() == ppid; {
		time.Sleep(200 * time.Millisecond)
	}
	return 0
}

// requireIntegration is the first call of every integration test.
func requireIntegration(t *testing.T) {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipLine(skipReason))
	}
}
