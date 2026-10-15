package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func tempToml(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigCheckExitCodes(t *testing.T) {
	valid := tempToml(t, "heartbeat_interval = \"10s\"\nwatchdog_timeout = \"10m\"\n[battery]\nhold_pct = 30\n")
	malformed := tempToml(t, "heartbeat_interval = [oops\n")
	inverted := tempToml(t, "[battery]\nhold_pct = 10\nrelease_pct = 20\n")
	missing := filepath.Join(t.TempDir(), "nope.toml")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"valid", []string{"config", "check", "--config", valid}, 0},
		{"malformed", []string{"config", "check", "--config", malformed}, 4},
		{"inverted battery", []string{"config", "check", "--config", inverted}, 4},
		{"explicit missing file", []string{"config", "check", "--config", missing}, 4},
		{"unknown flag", []string{"config", "check", "--bogus"}, 2},
		{"stray argument", []string{"config", "check", "extra"}, 2},
		{"no subcommand", []string{"config"}, 2},
		{"unknown subcommand", []string{"config", "frob"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, tc.args...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tc.want, stderr)
			}
			if tc.want == 4 && !strings.Contains(stderr, "pg-keep-awake:") {
				t.Fatalf("invalid config gave no diagnostic: %q", stderr)
			}
		})
	}
}

func TestConfigCheckDefaultPath(t *testing.T) {
	t.Run("absent default file is valid", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		if code, _, stderr := runCLI(t, "config", "check"); code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
	})
	t.Run("bad default file is invalid", func(t *testing.T) {
		xdg := t.TempDir()
		dir := filepath.Join(xdg, "pg-keep-awake")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[otel\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdg)
		if code, _, _ := runCLI(t, "config", "check"); code != 4 {
			t.Fatalf("exit = %d, want 4", code)
		}
	})
}
