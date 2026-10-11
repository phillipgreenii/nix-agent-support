package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

const golden = "internal/config/testdata/example.json"

// reexecEnv marks a child process that is this test binary standing in for the
// real command: TestMain runs the real main() in it, so a test sees what a
// supervisor or a shell would see (a real exit status, a real process).
const reexecEnv = "PG_CONNECTOR_GITHUB_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		main() // exits the process
		return
	}
	os.Exit(m.Run())
}

// runReal runs the real command in a child process the way a supervisor starts
// it: an otherwise empty environment (env -i) and working directory "/".
func runReal(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.Env = []string{reexecEnv + "=1"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func TestRealProcessModesFailCleanlyUnderEmptyEnvironment(t *testing.T) {
	abs, err := filepath.Abs(golden)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"serve", "pr", "ci"} {
		code, out, errs := runReal(t, "--config", abs, mode)
		if code != exitNotImplemented || out != "" {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", mode, code, out, errs)
		}
		if !strings.Contains(errs, "not implemented") || !strings.Contains(errs, mode) {
			t.Errorf("%s: stderr %q must name the missing implementation and the mode", mode, errs)
		}
		if strings.Contains(errs, "panic") || strings.Contains(errs, "goroutine") {
			t.Errorf("%s: a stub must fail cleanly, not panic: %q", mode, errs)
		}
	}
}

func TestRealProcessVersionAndUsage(t *testing.T) {
	code, out, errs := runReal(t, "--version")
	if code != 0 || !regexp.MustCompile(`^pg-connector-github \S+\n$`).MatchString(out) {
		t.Errorf("--version: exit=%d stdout=%q stderr=%q", code, out, errs)
	}
	if code, _, errs := runReal(t); code != exitUsage || !strings.Contains(errs, "usage:") {
		t.Errorf("no mode: exit=%d stderr=%q", code, errs)
	}
	// With nothing in the environment and no flag there is no config to load.
	if code, _, errs := runReal(t, "serve"); code != exitConfig || !strings.Contains(errs, config.EnvConfig) {
		t.Errorf("no config: exit=%d stderr=%q", code, errs)
	}
}

func noEnv(string) string { return "" }

func runArgs(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb, getenv)
	return code, out.String(), errb.String()
}

func TestVersionIsStamped(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "26.10.10.00001+abcd1234"
	code, out, _ := runArgs(t, noEnv, "--version")
	if code != 0 || strings.TrimSpace(out) != "pg-connector-github 26.10.10.00001+abcd1234" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestModesAreStubsThatFailCleanly(t *testing.T) {
	for _, mode := range []string{"serve", "pr", "ci"} {
		for name, args := range map[string][]string{
			"flag before mode": {"--config", golden, mode},
			"equals form":      {"--config=" + golden, mode},
		} {
			code, out, errs := runArgs(t, noEnv, args...)
			if code != exitNotImplemented || out != "" {
				t.Errorf("%s %s: code=%d out=%q", mode, name, code, out)
			}
			if !strings.Contains(errs, "not implemented") || !strings.Contains(errs, mode) {
				t.Errorf("%s %s: stderr %q must name the stub and the mode", mode, name, errs)
			}
		}
	}
}

func TestConfigFromEnvWhenFlagAbsent(t *testing.T) {
	env := func(k string) string {
		if k == config.EnvConfig {
			return golden
		}
		return ""
	}
	if code, _, errs := runArgs(t, env, "serve"); code != exitNotImplemented {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	// The flag wins over the variable: a bad flag path fails even though the
	// variable names a good file.
	if code, _, errs := runArgs(t, env, "--config", filepath.Join(t.TempDir(), "missing.json"), "serve"); code != exitConfig {
		t.Fatalf("flag must win, code=%d stderr=%q", code, errs)
	}
}

func TestConfigErrors(t *testing.T) {
	if code, _, errs := runArgs(t, noEnv, "serve"); code != exitConfig || !strings.Contains(errs, config.EnvConfig) {
		t.Errorf("no config: code=%d stderr=%q", code, errs)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"state_dir":"/tmp/s","bogus":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runArgs(t, noEnv, "--config", bad, "pr"); code != exitConfig || !strings.Contains(errs, "bogus") {
		t.Errorf("bad config: code=%d stderr=%q", code, errs)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no mode":      {},
		"unknown mode": {"--config", golden, "bogus"},
		"unknown flag": {"--nope", "serve"},
		"flag no path": {"--config"},
	}
	for name, args := range cases {
		code, _, errs := runArgs(t, noEnv, args...)
		if code != exitUsage || !strings.Contains(errs, "usage:") {
			t.Errorf("%s: code=%d stderr=%q", name, code, errs)
		}
	}
	if code, out, _ := runArgs(t, noEnv, "--help"); code != 0 || !strings.Contains(out, "serve") {
		t.Errorf("help: code=%d out=%q", code, out)
	}
}

func TestModeIsFirstNonFlagWord(t *testing.T) {
	o, err := parseArgs([]string{"--config", "c.json", "pr", "--query", "mine"})
	if err != nil || o.mode != "pr" || o.configPath != "c.json" || len(o.modeArgs) != 2 || o.modeArgs[0] != "--query" {
		t.Fatalf("%+v %v", o, err)
	}
}
