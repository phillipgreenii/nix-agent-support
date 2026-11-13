package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestExitCodeContract exercises the REAL binary and pins the exit-code
// contract of exitcodes.go: usage is 2, a missing daemon is the shared
// Unavailable 3, no-match is the shared NotFound 4, all-idle is the app code
// 20, and exit 1 is reserved for a generic, unplanned error. The cases use
// literal numbers so a renumbering of the constants cannot pass unnoticed.
//
// HOME and the XDG dirs are a temp dir with no daemon socket, so every
// daemon-bound subcommand sees "daemon unavailable".
func TestExitCodeContract(t *testing.T) {
	bin := selfBinary(t)
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
		{"agents-busy-check without a daemon is daemon-unavailable (3)", []string{"agents-busy-check"}, filepath.Join(home, ".config"), 3},
		{"agents-busy-check treating a down daemon as busy exits 0", []string{"agents-busy-check", "--consider-daemon-down-as-busy"}, filepath.Join(home, ".config"), 0},
		{"info without a daemon is daemon-unavailable (3)", []string{"info", "session:abc"}, filepath.Join(home, ".config"), 3},
		{"an unplanned internal error (unreadable config) is generic exit 1", []string{"sessions"}, badCfg, exitGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The daemon-unavailable cases each wait out the client dial deadline
			// (2-3 s of real time), so run the cases side by side.
			t.Parallel()
			cmd := exec.CommandContext(context.Background(), bin, tc.args...)
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

// TestExitCodeConstantsArePinned pins each code's number and the contract's
// shape: 1 generic, 2 usage, the shared codes 3 and 4, and the single app code
// in the 20..63 range, never a reserved value.
func TestExitCodeConstantsArePinned(t *testing.T) {
	want := map[string][2]int{
		"exitGeneric":           {exitGeneric, 1},
		"exitUsage":             {exitUsage, 2},
		"exitDaemonUnavailable": {exitDaemonUnavailable, 3},
		"exitNoMatch":           {exitNoMatch, 4},
		"exitNotBusy":           {exitNotBusy, 20},
	}
	seen := map[int]string{}
	for name, c := range want {
		if c[0] != c[1] {
			t.Errorf("%s = %d, want %d", name, c[0], c[1])
		}
		if prev, dup := seen[c[0]]; dup {
			t.Errorf("%s and %s share exit code %d", name, prev, c[0])
		}
		seen[c[0]] = name
	}
	if exitNotBusy < 20 || exitNotBusy > 63 {
		t.Errorf("exitNotBusy = %d, want within the app range 20..63", exitNotBusy)
	}
}
