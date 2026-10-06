//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/session"
)

// TestCapacity_emptyPoolFreeEqualsMax starts the real ccpool binary against a
// temp, empty pool and asserts `ccpool capacity --json` parses and reports
// free == max_sessions (nothing live, nothing counted).
func TestCapacity_emptyPoolFreeEqualsMax(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	_ = os.MkdirAll(bin, 0o755)
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	installFakePAMonitor(t, bin, `{"sessions":[]}`)
	socket := "ccpool-capacitytest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`
[pool]
max_sessions = 6
[tmux]
socket = "`+socket+`"
`), 0o600)
	env := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"HOME="+base, "PATH="+bin+":"+os.Getenv("PATH"),
	)
	run := func(args ...string) (string, int) {
		cmd := exec.Command(ccpool, args...)
		cmd.Env = env
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, out)
		}
		return string(out), code
	}

	out, code := run("capacity", "--json")
	if code != 0 {
		t.Fatalf("capacity --json exited %d:\n%s", code, out)
	}
	var c session.Capacity
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("parse capacity --json output %q: %v", out, err)
	}
	if c.MaxSessions != 6 {
		t.Fatalf("max_sessions = %d, want 6", c.MaxSessions)
	}
	if c.Free != c.MaxSessions {
		t.Errorf("free = %d, want %d (empty pool: nothing live, nothing counted)", c.Free, c.MaxSessions)
	}
	if c.Live != 0 || c.Counted != 0 || c.Preserved != 0 {
		t.Errorf("got %+v, want all zero besides max_sessions/free on an empty pool", c)
	}
}

// installFakePAMonitor puts a `pa-monitor` on bin (first on the test's PATH) that
// prints the JSON currently in bin/pa-doc.json for `status --json`, so the real
// binary's usage gate runs against a controlled reading and never the developer's
// own daemon. It returns the doc path; rewrite that file to change the reading.
func installFakePAMonitor(t *testing.T, bin, doc string) string {
	t.Helper()
	docPath := filepath.Join(bin, "pa-doc.json")
	if err := os.WriteFile(docPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1 $2\" = \"status --json\" ] || exit 64\ncat \"$(dirname \"$0\")/pa-doc.json\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pa-monitor"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return docPath
}

// TestUsageGate_endToEndRefusesWhileWindowHit drives the REAL ccpool binary
// against a fake monitor: while the 5-hour window is hit, `capacity --json`
// reports free=0 plus the limit, and `new` and `reply` exit 8 without touching
// the pool; once the window's reset passes (or the reading clears), the same
// commands stop refusing. Nothing here launches a session or needs tmux.
func TestUsageGate_endToEndRefusesWhileWindowHit(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	_ = os.MkdirAll(bin, 0o755)
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	hit := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	docPath := installFakePAMonitor(t, bin,
		`{"rate_limits":{"five_hour":{"used_pct":100,"resets_at":"`+hit.Format(time.RFC3339)+`"}}}`)
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[pool]\nmax_sessions = 6\n"), 0o600)
	env := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(base, "run"),
		"HOME="+base, "PATH="+bin+":"+os.Getenv("PATH"),
	)
	run := func(args ...string) (stdout, stderr string, code int) {
		cmd := exec.Command(ccpool, args...)
		cmd.Env = env
		cmd.Dir = base
		var so, se strings.Builder
		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run %v: %v", args, err)
			}
			code = ee.ExitCode()
		}
		return so.String(), se.String(), code
	}

	out, errOut, code := run("capacity", "--json")
	if code != 0 {
		t.Fatalf("capacity --json exited %d: %s", code, errOut)
	}
	var c session.Capacity
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if c.Free != 0 || c.MaxSessions != 6 || c.UsageLimit == nil || string(c.UsageLimit.Window) != "five_hour" || !c.UsageLimit.ResetsAt.Equal(hit) {
		t.Fatalf("capacity = %+v (usage_limit %+v), want free=0 of 6 with the five_hour limit resetting %v", c, c.UsageLimit, hit)
	}

	for _, args := range [][]string{{"new", "alpha"}, {"reply", "alpha", "hello", "--no-wait"}} {
		_, errOut, code := run(args...)
		if code != 8 {
			t.Errorf("ccpool %v exited %d, want 8; stderr: %s", args, code, errOut)
		}
		if !strings.Contains(errOut, "five_hour") {
			t.Errorf("ccpool %v stderr %q must name the window", args, errOut)
		}
	}

	// The reading clears: the same capacity call is unblocked again.
	if err := os.WriteFile(docPath, []byte(`{"rate_limits":{"five_hour":{"used_pct":40,"resets_at":"`+hit.Format(time.RFC3339)+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = run("capacity", "--json")
	if code != 0 {
		t.Fatalf("capacity --json exited %d: %s", code, errOut)
	}
	c = session.Capacity{}
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if c.Free != 6 || c.UsageLimit != nil {
		t.Errorf("after the window clears capacity = %+v, want free=6 and no usage_limit", c)
	}
}
