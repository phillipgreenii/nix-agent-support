package caffeinate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate/keepertest"
)

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pg-keep-awake")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExe := filepath.Join(dir, "plain-file")
	if err := os.WriteFile(notExe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("configured absolute path wins and is not searched on PATH", func(t *testing.T) {
		t.Setenv("PATH", "") // an empty PATH must not matter for a configured path
		got, err := Locate(exe)
		if err != nil || got != exe {
			t.Errorf("Locate(%q) = %q, %v; want the path itself", exe, got, err)
		}
	})
	t.Run("a relative configured path is refused", func(t *testing.T) {
		if _, err := Locate("pg-keep-awake"); !errors.Is(err, ErrKeeperNotFound) {
			t.Errorf("err = %v, want ErrKeeperNotFound", err)
		}
	})
	t.Run("a missing configured path is refused", func(t *testing.T) {
		if _, err := Locate(filepath.Join(dir, "nope")); !errors.Is(err, ErrKeeperNotFound) {
			t.Errorf("err = %v, want ErrKeeperNotFound", err)
		}
	})
	t.Run("a non-executable configured path is refused", func(t *testing.T) {
		if _, err := Locate(notExe); !errors.Is(err, ErrKeeperNotFound) {
			t.Errorf("err = %v, want ErrKeeperNotFound", err)
		}
	})
	t.Run("unset falls back to PATH", func(t *testing.T) {
		t.Setenv("PATH", dir)
		got, err := Locate("")
		if err != nil || got != exe {
			t.Errorf("Locate(\"\") = %q, %v; want %q", got, err, exe)
		}
	})
	t.Run("unset and not on PATH is an error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := Locate(""); !errors.Is(err, ErrKeeperNotFound) {
			t.Errorf("err = %v, want ErrKeeperNotFound", err)
		}
	})
}

func TestCLIObserveReadsStatusJSONAndCheckExit(t *testing.T) {
	bin := keepertest.Bin(t)
	state := keepertest.UseStateDir(t)
	c := &CLI{Configured: bin}

	obs := c.Observe()
	if obs.Health != HealthDown || !obs.StatusKnown || obs.Held || obs.Gate != GateUnknown {
		t.Errorf("never-started keeper: %+v, want down, status known, not held, gate unknown", obs)
	}

	keepertest.SimulateKeeper(t, state, keepertest.KeeperState{Held: true, Gate: "no_spawn", OnBattery: true, Percent: 28})
	obs = c.Observe()
	if obs.Health != HealthOK || !obs.StatusKnown || !obs.Held || obs.Gate != GateNoSpawn || !obs.OnBattery || obs.Percent != 28 || !obs.BatteryReadOK {
		t.Errorf("running keeper: %+v, want ok, held, no_spawn, on battery, 28%%", obs)
	}
}

func TestCLICallsFailVisibly(t *testing.T) {
	c := &CLI{Configured: filepath.Join(t.TempDir(), "absent")}
	if err := c.Acquire(os.Getpid()); !errors.Is(err, ErrKeeperNotFound) {
		t.Errorf("Acquire err = %v, want ErrKeeperNotFound", err)
	}
	if err := c.Release(); !errors.Is(err, ErrKeeperNotFound) {
		t.Errorf("Release err = %v, want ErrKeeperNotFound", err)
	}
	if obs := c.Observe(); obs.Health != HealthUnknown || obs.Err == nil || obs.Health.Up() {
		t.Errorf("Observe = %+v, want unknown health with an error", obs)
	}
}

// helperModeEnv selects what TestHelperKeeperProcess does when the test binary
// is re-executed as a subprocess.
const (
	helperModeEnv = "PA_MONITOR_KEEPERTEST_HELPER"
	helperBinEnv  = "PA_MONITOR_KEEPERTEST_BIN"
)

// TestHelperKeeperProcess is not a test: it is the body of the subprocess the
// env -i test starts. It runs pa-monitor's acquire invocation (the CLI client)
// and prints "ok" or "err: ..." to stdout.
func TestHelperKeeperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	c := &CLI{}
	if mode == "configured" {
		c.Configured = os.Getenv(helperBinEnv)
	}
	if err := c.Acquire(os.Getpid()); err != nil {
		os.Stdout.WriteString("err: " + err.Error() + "\n")
		os.Exit(0)
	}
	os.Stdout.WriteString("ok\n")
	os.Exit(0)
}

// TestAcquireResolvesTheCLIWithoutALoginPATH proves the executable lookup does
// not depend on a login shell's PATH: pa-monitor's acquire runs as a subprocess
// under `env -i PATH=/usr/bin:/bin` (launchd's shape) and still finds and runs
// the real CLI through the configured absolute path. The negative control, the
// same call without a configured path, cannot find it on that PATH.
func TestAcquireResolvesTheCLIWithoutALoginPATH(t *testing.T) {
	bin := keepertest.Bin(t)
	state := keepertest.UseStateDir(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode string) string {
		t.Helper()
		cmd := exec.Command("/usr/bin/env", "-i",
			"PATH=/usr/bin:/bin",
			"XDG_STATE_HOME="+os.Getenv("XDG_STATE_HOME"),
			helperModeEnv+"="+mode,
			helperBinEnv+"="+bin,
			self, "-test.run=^TestHelperKeeperProcess$", "-test.v=false")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("helper (%s): %v", mode, err)
		}
		return strings.TrimSpace(string(out))
	}

	got := run("configured")
	if !strings.Contains(got, "ok") {
		t.Fatalf("acquire under env -i PATH=/usr/bin:/bin with a configured path: %q, want ok", got)
	}
	if _, err := os.Stat(filepath.Join(state, "leases", Owner+".default.json")); err != nil {
		t.Errorf("the real CLI did not write the lease under env -i: %v", err)
	}

	got = run("path")
	if !strings.Contains(got, "err:") || !strings.Contains(got, ErrKeeperNotFound.Error()) {
		t.Errorf("negative control (no configured path, PATH=/usr/bin:/bin): %q, want a not-found error; otherwise the test proves nothing about PATH independence", got)
	}
}
