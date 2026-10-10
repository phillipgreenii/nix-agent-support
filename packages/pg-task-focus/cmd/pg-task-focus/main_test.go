package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMain lets this test binary run as the real command: a test that sets
// PG_TASK_FOCUS_AS_MAIN re-executes it and gets main() with the signal handling.
func TestMain(m *testing.M) {
	if os.Getenv("PG_TASK_FOCUS_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeConfig(t *testing.T, path string, port int, minutes int) {
	t.Helper()
	cfg := fmt.Sprintf(`{
  "defaults": {"cycle_minutes": 25, "profile": "normal", "alert": {"sound": "Glass", "repeat_minutes": 5}},
  "listen_port": %d,
  "profiles": {"normal": {"daily": ["plan-day"], "weekly": [], "sprint": [], "cycles": ["deep-work"]}},
  "tasks": {"plan-day": {"title": "Plan the day", "cadence": "daily", "due": {"at": "09:00", "tz": "America/New_York"}}},
  "cycles": {"deep-work": {"title": "Deep work", "minutes": %d, "keys": []}}
}`, port, minutes)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, port int, path string, v any) int {
	t.Helper()
	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return 0
	}
	defer func() { _ = res.Body.Close() }()
	if v != nil {
		_ = json.NewDecoder(res.Body).Decode(v)
	}
	return res.StatusCode
}

// The real process: it serves, reloads its configuration on SIGHUP, and stops
// cleanly (releasing the data directory) on SIGTERM.
func TestServeReloadsOnSIGHUPAndStopsOnSIGTERM(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	data := filepath.Join(dir, "data")
	port := freePort(t)
	writeConfig(t, cfg, port, 50)

	cmd := exec.Command(os.Args[0], "serve", "--config", cfg, "--data-dir", data)
	cmd.Env = append(os.Environ(), "PG_TASK_FOCUS_AS_MAIN=1", "HOME="+dir)
	logFile, err := os.Create(filepath.Join(dir, "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "serve.log"))
		t.Fatalf("timed out waiting for %s\nlog:\n%s", what, b)
	}
	waitFor("readiness", func() bool { return get(t, port, "/readyz", nil) == 200 })

	var h struct {
		Config struct {
			Digest string `json:"digest"`
			Valid  bool   `json:"valid"`
		} `json:"config"`
	}
	get(t, port, "/healthz", &h)
	before := h.Config.Digest
	if before == "" || !h.Config.Valid {
		t.Fatalf("health before the reload: %+v", h)
	}

	writeConfig(t, cfg, port, 90)
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor("the reload", func() bool {
		var got struct {
			Config struct{ Digest string } `json:"config"`
		}
		get(t, port, "/healthz", &got)
		return got.Config.Digest != "" && got.Config.Digest != before
	})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			b, _ := os.ReadFile(filepath.Join(dir, "serve.log"))
			t.Fatalf("a stopped daemon exits 0, got %v\nlog:\n%s", err, b)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the daemon did not stop on SIGTERM")
	}
	// The data directory is released: another daemon can take it.
	cmd2 := exec.Command(os.Args[0], "check", data)
	cmd2.Env = append(os.Environ(), "PG_TASK_FOCUS_AS_MAIN=1")
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Errorf("check after the stop: %v\n%s", err, out)
	}
}

// A daemon that cannot start exits with the serve-failed code and says why.
func TestServeFailureExitsWithItsOwnCode(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"listen_port": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "serve", "--config", cfg, "--data-dir", filepath.Join(dir, "data"))
	cmd.Env = append(os.Environ(), "PG_TASK_FOCUS_AS_MAIN=1")
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 7 {
		t.Fatalf("exit = %v, want 7\n%s", err, out)
	}
}
