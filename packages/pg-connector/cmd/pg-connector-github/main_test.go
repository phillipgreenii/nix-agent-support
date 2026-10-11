package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

const golden = "internal/config/testdata/example.json"

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
