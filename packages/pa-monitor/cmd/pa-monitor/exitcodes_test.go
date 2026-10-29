package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestExitCodeContract exercises the REAL binary and pins the exit-code
// contract of exitcodes.go (bead pg2-xrmfng, operator ruling on pg2-9jk3g5):
// usage is 2, a missing daemon is 6, the all-idle / timeout / no-match codes are
// distinct (>= 3), and exit 1 is reserved for a generic, unplanned error.
//
// HOME and the XDG dirs are a temp dir with no daemon socket, so every
// daemon-bound subcommand sees "daemon unavailable".
func TestExitCodeContract(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pa-monitor")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	home := t.TempDir()
	badCfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(badCfg, "pa-monitor"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Not valid TOML: config loading fails, a plain unplanned error.
	if err := os.WriteFile(filepath.Join(badCfg, "pa-monitor", "config.toml"), []byte("= = not toml\n[[["), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{
		"HOME=" + home, "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".state"),
	}

	cases := []struct {
		name string
		args []string
		cfg  string
		want int
	}{
		{"info without selector is usage", []string{"info"}, filepath.Join(home, ".config"), exitUsage},
		{"agents-busy-check without a daemon is daemon-unavailable", []string{"agents-busy-check"}, filepath.Join(home, ".config"), exitDaemonUnavailable},
		{"agents-busy-check treating a down daemon as busy exits 0", []string{"agents-busy-check", "--consider-daemon-down-as-busy"}, filepath.Join(home, ".config"), 0},
		{"info without a daemon is daemon-unavailable", []string{"info", "session:abc"}, filepath.Join(home, ".config"), exitDaemonUnavailable},
		{"an unplanned internal error (unreadable config) is generic exit 1", []string{"sessions"}, badCfg, exitGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Env = append(append(os.Environ(), base...), "XDG_CONFIG_HOME="+tc.cfg)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			got := 0
			if err := cmd.Run(); err != nil {
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					t.Fatalf("run %v: %v", tc.args, err)
				}
				got = ee.ExitCode()
			}
			if got != tc.want {
				t.Fatalf("%v: exit %d, want %d; stderr: %q", tc.args, got, tc.want, stderr.String())
			}
		})
	}
}

// TestExitCodeConstantsAreDistinct guards the contract's shape: 1 generic,
// 2 usage, and every specific condition a distinct code >= 3.
func TestExitCodeConstantsAreDistinct(t *testing.T) {
	if exitGeneric != 1 || exitUsage != 2 {
		t.Fatalf("exitGeneric=%d exitUsage=%d, want 1 and 2", exitGeneric, exitUsage)
	}
	seen := map[int]string{}
	for name, c := range map[string]int{
		"exitNotBusy": exitNotBusy,
		"exitNoMatch": exitNoMatch, "exitDaemonUnavailable": exitDaemonUnavailable,
	} {
		if c < 3 {
			t.Errorf("%s = %d, want >= 3", name, c)
		}
		if prev, dup := seen[c]; dup {
			t.Errorf("%s and %s share exit code %d", name, prev, c)
		}
		seen[c] = name
	}
}
