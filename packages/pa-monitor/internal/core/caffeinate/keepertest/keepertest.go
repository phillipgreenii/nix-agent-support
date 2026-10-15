// Package keepertest builds the REAL pg-keep-awake binary for tests, so a test
// of pa-monitor's keep-awake wiring runs against the actual CLI contract instead
// of a mock of it.
//
// The binary is built once per test process from the sibling Go module
// packages/pg-keep-awake. A test binary calls Main from its TestMain; tests then
// ask for the binary with Bin and an isolated state directory with UseStateDir.
// When the pg-keep-awake source is absent (it is, inside the pa-monitor nix
// test sandbox, whose source tree holds only pa-monitor and claude-transcript)
// Bin skips the test.
package keepertest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// binPath is the built executable, or "" when the source is absent.
var binPath string

// sourceDir is packages/pg-keep-awake/cmd/pg-keep-awake, found relative to this
// file: packages/pa-monitor/internal/core/caffeinate/keepertest/keepertest.go.
func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "pg-keep-awake", "cmd", "pg-keep-awake"))
}

// Main builds pg-keep-awake (when its source is present), runs the tests and
// removes the build. Call it from TestMain: os.Exit(keepertest.Main(m)).
func Main(m *testing.M) int {
	src := sourceDir()
	if src == "" {
		return m.Run()
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return m.Run()
	}
	tmp, err := os.MkdirTemp("", "pa-monitor-keepertest-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "keepertest: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "pg-keep-awake")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "keepertest: go build pg-keep-awake: %v\n%s", err, b)
		return 1
	}
	binPath = out
	return m.Run()
}

// Bin returns the absolute path of the built pg-keep-awake, skipping the test
// when the source was not available to build.
func Bin(t testing.TB) string {
	t.Helper()
	if binPath == "" {
		t.Skip("packages/pg-keep-awake source is absent (e.g. the pa-monitor nix test sandbox); cannot build the real CLI")
	}
	return binPath
}

// KeeperState is what SimulateKeeper writes as the keeper's last pass.
type KeeperState struct {
	Held      bool
	Gate      string // default "ok"
	OnBattery bool
	Percent   int // default 64
	// UnexplainedTicks > 1 with a live lease and nothing held is what makes the
	// real `status --check` exit 6 (degraded).
	UnexplainedTicks int
}

// SimulateKeeper writes the files a RUNNING keeper leaves in stateDir (a fresh
// keeper.heartbeat and a status.json), so the real `pg-keep-awake status`
// reports the keeper up with the given held state and battery gate. The keeper
// process itself is not started; everything pa-monitor reads is the CLI's own
// output over these files.
func SimulateKeeper(t testing.TB, stateDir string, k KeeperState) {
	t.Helper()
	if k.Gate == "" {
		k.Gate = "ok"
	}
	if k.Percent == 0 {
		k.Percent = 64
	}
	for _, d := range []string{stateDir, filepath.Join(stateDir, "leases")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{
  "schema_version": 1,
  "written_at": %q,
  "keeper": {"pid": 1, "version": "test", "heartbeat_age_s": 0, "config_error": null},
  "assertion": {"held": %v, "flags": ["i"], "pid": 1},
  "paused": null,
  "battery": {"percent": %d, "on_battery": %v, "gate": %q, "read_ok": true},
  "leases": [],
  "last_release": null,
  "external": [],
  "heartbeat_interval_s": 10,
  "unexplained_ticks": %d
}
`, time.Now().UTC().Format(time.RFC3339Nano), k.Held, k.Percent, k.OnBattery, k.Gate, k.UnexplainedTicks)
	if err := os.WriteFile(filepath.Join(stateDir, "status.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "keeper.heartbeat"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(filepath.Join(stateDir, "keeper.heartbeat"), now, now); err != nil {
		t.Fatal(err)
	}
}

// StopKeeper removes the keeper's heartbeat and status, so the real `status
// --check` exits 5 (keeper down or never started).
func StopKeeper(t testing.TB, stateDir string) {
	t.Helper()
	for _, f := range []string{"keeper.heartbeat", "status.json"} {
		if err := os.Remove(filepath.Join(stateDir, f)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

// UseStateDir points XDG_STATE_HOME at a fresh temp directory for the test and
// returns the pg-keep-awake state directory inside it (not yet created). The
// real CLI resolves its state directory from XDG_STATE_HOME, so every
// subprocess the test starts shares this one.
func UseStateDir(t testing.TB) string {
	t.Helper()
	xdg, err := os.MkdirTemp("", "pa-monitor-kastate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_STATE_HOME", xdg)
	return filepath.Join(xdg, "pg-keep-awake")
}
