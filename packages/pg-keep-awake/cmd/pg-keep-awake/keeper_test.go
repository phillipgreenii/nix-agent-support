package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// keeperEnv prepares a temp state and config home, a battery source file and
// the hidden battery-source variable. The battery is "19 battery" (at or below
// the release level), so no caffeinate is ever started by these tests.
func keeperEnv(t *testing.T, config string) (stateDir, configFile string) {
	t.Helper()
	stateDir = useStateDir(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if config != "" {
		dir := filepath.Join(cfgHome, "pg-keep-awake")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		configFile = filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configFile, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "battery")
	if err := os.WriteFile(src, []byte("19 battery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_KEEP_AWAKE_BATTERY_SOURCE", "file:"+src)
	return stateDir, configFile
}

// stopOnCleanup makes sure a keeper this test started never outlives it.
func stopOnCleanup(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill() // our own child
			_ = cmd.Wait()
		}
	})
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readStatusFile(t *testing.T, stateDir string) *lease.StatusFile {
	t.Helper()
	d, err := lease.CheckDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.ReadStatus()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitExit(t *testing.T, cmd *exec.Cmd, within time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return cmd.ProcessState.ExitCode()
	case <-time.After(within):
		t.Fatalf("process %d did not exit within %v", cmd.Process.Pid, within)
		return -1
	}
}

func TestKeeperSingletonHeartbeatAndGracefulShutdown(t *testing.T) {
	requireProcInfo(t)
	stateDir, _ := keeperEnv(t, "heartbeat_interval = \"100ms\"\n")

	var out1, err1 bytes.Buffer
	first := startBinary(t, &out1, &err1, "keeper")
	stopOnCleanup(t, first)
	hb := filepath.Join(stateDir, "keeper.heartbeat")
	waitUntil(t, "the first keeper's status.json", func() bool { return fileExists(filepath.Join(stateDir, "status.json")) })
	if !fileExists(hb) || !fileExists(filepath.Join(stateDir, "keeper.lock")) {
		t.Fatal("keeper.heartbeat or keeper.lock missing while the keeper runs")
	}
	if got := readStatusFile(t, stateDir).Keeper.PID; got != first.Process.Pid {
		t.Fatalf("status keeper.pid = %d, want the keeper's own pid %d", got, first.Process.Pid)
	}

	// The heartbeat advances while the keeper runs.
	fi1, _ := os.Stat(hb)
	waitUntil(t, "the heartbeat to advance", func() bool {
		fi2, err := os.Stat(hb)
		return err == nil && fi2.ModTime().After(fi1.ModTime())
	})

	// A lease written by a client survives the keeper's shutdown.
	var o, e bytes.Buffer
	if code := run([]string{"acquire", "--owner", "t", "--for", "60s"}, &o, &e); code != exitOK {
		t.Fatalf("acquire = %d: %s", code, e.String())
	}

	// A second keeper in the same state directory exits at once.
	var out2, err2 bytes.Buffer
	second := startBinary(t, &out2, &err2, "keeper")
	stopOnCleanup(t, second)
	if code := waitExit(t, second, 5*time.Second); code == exitOK {
		t.Fatalf("second keeper exited 0, want a failure; stderr: %s", err2.String())
	}
	if !strings.Contains(err2.String(), "keeper.lock") {
		t.Fatalf("second keeper's stderr does not name the lock: %q", err2.String())
	}
	if !fileExists(hb) {
		t.Fatal("the second keeper disturbed the first one's heartbeat")
	}

	// SIGTERM: exit 0, heartbeat gone, lease kept.
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, first, 10*time.Second); code != exitOK {
		t.Fatalf("keeper exit after SIGTERM = %d; stderr: %s", code, err1.String())
	}
	if fileExists(hb) {
		t.Fatal("keeper.heartbeat survived the graceful shutdown")
	}
	if !fileExists(leasePath(stateDir, "t.default")) {
		t.Fatal("a valid lease was removed at shutdown")
	}
}

func TestKeeperDoesNotCrashLoopOnABadConfig(t *testing.T) {
	requireProcInfo(t)
	stateDir, _ := keeperEnv(t, "heartbeat_interval = \"not a duration\"\n")

	var out, errb bytes.Buffer
	k := startBinary(t, &out, &errb, "keeper")
	stopOnCleanup(t, k)
	waitUntil(t, "status.json", func() bool { return fileExists(filepath.Join(stateDir, "status.json")) })

	s := readStatusFile(t, stateDir)
	if s.Keeper.ConfigError == nil || !strings.Contains(*s.Keeper.ConfigError, "heartbeat_interval") {
		t.Fatalf("keeper.config_error = %v, want the parse error", s.Keeper.ConfigError)
	}
	if s.HeartbeatIntervalS != 10 {
		t.Fatalf("heartbeat_interval_s = %d, want the built-in default 10", s.HeartbeatIntervalS)
	}
	if fileExists(filepath.Join(stateDir, lease.FileLastGood)) {
		t.Fatal("config.last-good.toml written from a bad file")
	}
	if err := k.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, k, 10*time.Second); code != exitOK {
		t.Fatalf("exit after SIGTERM = %d; stderr: %s", code, errb.String())
	}
}

func TestKeeperRecordsLastGoodConfigAndFallsBackToItOnRestart(t *testing.T) {
	requireProcInfo(t)
	stateDir, cfgFile := keeperEnv(t, "heartbeat_interval = \"3s\"\n")

	start := func() (*exec.Cmd, *bytes.Buffer) {
		var out, errb bytes.Buffer
		k := startBinary(t, &out, &errb, "keeper")
		stopOnCleanup(t, k)
		return k, &errb
	}
	stop := func(k *exec.Cmd, errb *bytes.Buffer) {
		t.Helper()
		if err := k.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if code := waitExit(t, k, 10*time.Second); code != exitOK {
			t.Fatalf("exit = %d; stderr: %s", code, errb.String())
		}
	}

	k, errb := start()
	waitUntil(t, "status.json", func() bool { return fileExists(filepath.Join(stateDir, "status.json")) })
	if s := readStatusFile(t, stateDir); s.HeartbeatIntervalS != 3 || s.Keeper.ConfigError != nil {
		t.Fatalf("first run: interval=%d config_error=%v", s.HeartbeatIntervalS, s.Keeper.ConfigError)
	}
	if !fileExists(filepath.Join(stateDir, lease.FileLastGood)) {
		t.Fatal("config.last-good.toml not written after a good parse")
	}
	stop(k, errb)

	// A bad edit: the restarted keeper uses the last-good values and says so.
	if err := os.WriteFile(cfgFile, []byte("linger = = oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stateDir, "status.json")); err != nil {
		t.Fatal(err)
	}
	k, errb = start()
	waitUntil(t, "status.json", func() bool { return fileExists(filepath.Join(stateDir, "status.json")) })
	s := readStatusFile(t, stateDir)
	if s.HeartbeatIntervalS != 3 {
		t.Fatalf("heartbeat_interval_s = %d, want 3 from config.last-good.toml", s.HeartbeatIntervalS)
	}
	if s.Keeper.ConfigError == nil {
		t.Fatal("keeper.config_error not reported for the bad file")
	}
	stop(k, errb)
}

func TestKeeperRejectsArguments(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"keeper", "extra"}, &out, &errb); code != exitUsage {
		t.Fatalf("keeper extra = %d, want %d", code, exitUsage)
	}
}
