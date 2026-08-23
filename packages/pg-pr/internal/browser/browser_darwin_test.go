//go:build darwin

package browser

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain points BinEnvVar at a path that cannot exist before any test in this
// package runs.
//
// This is a fail-safe, not the mechanism: every test that exercises the exec
// path installs its own stub via stubChrome. The guard exists because the
// failure mode of forgetting one is uniquely bad — openWindow would exec the
// REAL Chrome at DefaultBin and windows would appear on the operator's desktop
// during `go test`. With the guard, a forgotten stub is a missing-binary error
// instead.
func TestMain(m *testing.M) {
	if err := os.Setenv(BinEnvVar, filepath.Join(os.TempDir(), "pg-pr-test-must-never-be-a-real-browser")); err != nil {
		panic("guard BinEnvVar: " + err.Error())
	}
	os.Exit(m.Run())
}

// stubChrome installs an executable standing in for Chrome that appends its
// argv (one argument per line) to a record file, and points BinEnvVar at it.
// It returns the record path.
//
// Stubbing at the BINARY boundary is deliberate: the argv is the entire
// contract with Chrome — --new-window is what yields one window rather than
// tabs appended to the operator's current one, and --profile-directory is what
// keeps them in the operator's own profile — so it is the thing worth pinning.
func stubChrome(t *testing.T, exitCode int) (record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "chrome-stub")

	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + record + "\"; done\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write chrome stub: %v", err)
	}
	t.Setenv(BinEnvVar, bin)
	return record
}

// recordedArgv returns the stub's captured arguments, or nil when the stub was
// never invoked.
func recordedArgv(t *testing.T, record string) []string {
	t.Helper()
	raw, err := os.ReadFile(record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read argv record: %v", err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func TestOpenWindowForwardsProfileNewWindowAndURLsInOrder(t *testing.T) {
	record := stubChrome(t, 0)

	urls := []string{
		"https://example.test/pull/1",
		"https://example.test/pull/2",
		"https://example.test/pull/3",
	}
	if err := openWindow(urls); err != nil {
		t.Fatalf("openWindow() error = %v", err)
	}

	got := recordedArgv(t, record)
	want := append([]string{"--profile-directory=" + DefaultProfileDir, "--new-window"}, urls...)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv =\n  %v\nwant\n  %v", got, want)
	}
}

func TestOpenWindowHonorsProfileOverride(t *testing.T) {
	record := stubChrome(t, 0)
	t.Setenv(ProfileEnvVar, "Profile 2")

	if err := openWindow([]string{"https://example.test/pull/1"}); err != nil {
		t.Fatalf("openWindow() error = %v", err)
	}

	got := recordedArgv(t, record)
	if len(got) == 0 || got[0] != "--profile-directory=Profile 2" {
		t.Errorf("argv[0] = %q, want the overridden profile", got)
	}
}

// TestOpenWindowNoOpOnEmptyURLs pins that an empty selection never launches a
// browser — the caller reports "(no PRs match)" instead.
func TestOpenWindowNoOpOnEmptyURLs(t *testing.T) {
	record := stubChrome(t, 0)

	if err := openWindow(nil); err != nil {
		t.Fatalf("openWindow(nil) error = %v", err)
	}
	if got := recordedArgv(t, record); got != nil {
		t.Errorf("browser was launched for an empty selection: %v", got)
	}
}

func TestOpenWindowErrorsWhenBinaryMissing(t *testing.T) {
	t.Setenv(BinEnvVar, filepath.Join(t.TempDir(), "definitely-absent"))

	err := openWindow([]string{"https://example.test/pull/1"})
	if err == nil {
		t.Fatal("openWindow() error = nil, want a missing-binary error")
	}
	if !strings.Contains(err.Error(), "definitely-absent") {
		t.Errorf("error %q does not name the path it tried", err)
	}
	if !strings.Contains(err.Error(), BinEnvVar) {
		t.Errorf("error %q does not name the override env var", err)
	}
}

// TestOpenWindowReportsNonZeroExit exercises the exit-detection path, not the
// timeout path, so it widens forwardWait well past the production default:
// the stub exits in low milliseconds under normal scheduling, but under
// severe CPU contention observing that exit can be delayed past the 5s
// default, which makes the timeout branch win the select and return nil
// instead of the stub's real exit-3 error (observed 2026-08-21 under
// concurrent nix jobs). Widening only the test's own copy of the var doesn't
// change openWindow's production contract — see forwardWait's doc comment.
func TestOpenWindowReportsNonZeroExit(t *testing.T) {
	prev := forwardWait
	t.Cleanup(func() { forwardWait = prev })
	forwardWait = 30 * time.Second

	stubChrome(t, 3)

	err := openWindow([]string{"https://example.test/pull/1"})
	if err == nil {
		t.Fatal("openWindow() error = nil, want the stub's failure reported")
	}
	if !strings.Contains(err.Error(), "exited with error") {
		t.Errorf("error %q does not report the non-zero exit", err)
	}
}
