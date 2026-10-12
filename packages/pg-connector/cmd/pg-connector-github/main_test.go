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
	code := m.Run()
	removeBuilds()
	os.Exit(code)
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

func TestRealProcessClientModesAnswerUnderEmptyEnvironment(t *testing.T) {
	abs, err := filepath.Abs(golden)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pr", "ci"} {
		// Nothing on stdin: the client answers an unavailable error body and
		// exits with that code, it does not panic or hang.
		code, out, errs := runReal(t, "--config", abs, mode)
		if code != 4 || !strings.Contains(out, `"code":"unavailable"`) {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", mode, code, out, errs)
		}
		if strings.Contains(errs, "panic") || strings.Contains(errs, "goroutine") {
			t.Errorf("%s: must fail cleanly, not panic: %q", mode, errs)
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
	return runArgsIn(t, "", getenv, args...)
}

// runArgsIn is runArgs with stdin.
func runArgsIn(t *testing.T, stdin string, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb, getenv)
	return code, out.String(), errb.String()
}

const capabilitiesRequest = `{"op":"capabilities"}`

func TestVersionIsStamped(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "26.10.10.00001+abcd1234"
	code, out, _ := runArgs(t, noEnv, "--version")
	if code != 0 || strings.TrimSpace(out) != "pg-connector-github 26.10.10.00001+abcd1234" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

// pr and ci are client modes: with the config loaded they answer the request on
// stdin (capabilities is answered with no daemon at all).
func TestClientModesAnswerCapabilitiesWithoutADaemon(t *testing.T) {
	for _, mode := range []string{"pr", "ci"} {
		for name, args := range map[string][]string{
			"flag before mode": {"--config", golden, mode},
			"equals form":      {"--config=" + golden, mode},
		} {
			code, out, errs := runArgsIn(t, capabilitiesRequest, noEnv, args...)
			if code != 0 || !strings.Contains(out, `"cache_opt_out":true`) || !strings.Contains(out, `"owns_changes":true`) {
				t.Errorf("%s %s: code=%d out=%q stderr=%q", mode, name, code, out, errs)
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
	if code, _, errs := runArgsIn(t, capabilitiesRequest, env, "pr"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	// The flag wins over the variable: a bad flag path fails even though the
	// variable names a good file.
	if code, _, errs := runArgsIn(t, capabilitiesRequest, env, "--config", filepath.Join(t.TempDir(), "missing.json"), "pr"); code != exitConfig {
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

func TestGlobalFlagsAcceptedAfterMode(t *testing.T) {
	// --config is global: given after the mode word it takes effect, it is never
	// handed to the mode (where it would be silently ignored).
	o, err := parseArgs([]string{"pr", "--config", "c.json", "--query", "mine"})
	if err != nil || o.mode != "pr" || o.configPath != "c.json" || len(o.modeArgs) != 2 || o.modeArgs[0] != "--query" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseArgs([]string{"ci", "--config=c.json"})
	if err != nil || o.mode != "ci" || o.configPath != "c.json" || len(o.modeArgs) != 0 {
		t.Fatalf("%+v %v", o, err)
	}
	if _, err = parseArgs([]string{"serve", "--config"}); err == nil {
		t.Error("--config without a path must fail after the mode too")
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	for _, mode := range []string{"pr", "ci"} {
		if code, out, errs := runArgsIn(t, capabilitiesRequest, noEnv, mode, "--config", golden); code != 0 || out == "" {
			t.Errorf("%s --config after the mode: code=%d out=%q stderr=%q", mode, code, out, errs)
		}
		// The config named after the mode is the one loaded: a missing file
		// there fails, even though capabilities needs no daemon.
		if code, _, errs := runArgsIn(t, capabilitiesRequest, noEnv, mode, "--config", missing); code != exitConfig || !strings.Contains(errs, "missing.json") {
			t.Errorf("%s --config <missing> after the mode: code=%d stderr=%q", mode, code, errs)
		}
	}
	if code, out, _ := runArgs(t, noEnv, "serve", "--version"); code != 0 || !strings.Contains(out, "pg-connector-github") {
		t.Errorf("--version after the mode: code=%d out=%q", code, out)
	}
}
