package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/tui"
)

// withStubTUIRun overrides the package-level tuiRun seam for the duration of
// one test, restoring the real tui.Run afterward. The real tui.Run starts an
// actual bubbletea program and blocks until quit, which needs a real
// terminal and has no place in this test binary.
func withStubTUIRun(t *testing.T, stub func(tui.Options) error) {
	t.Helper()
	orig := tuiRun
	tuiRun = stub
	t.Cleanup(func() { tuiRun = orig })
}

// Routing: `tui` is its own route and forwards its args (--socket/--token),
// matching status/self-status's TestRoute_* pattern (self_status_test.go's
// TestRoute_selfStatus); the help text advertises it.
func TestRoute_tui(t *testing.T) {
	r := route([]string{"pr-pool", "tui", "--socket", "/s", "--token", "t"})
	if r.kind != routeTUI {
		t.Fatalf("kind = %v, want routeTUI", r.kind)
	}
	if strings.Join(r.rest, " ") != "--socket /s --token t" {
		t.Fatalf("rest = %v, want the flags forwarded", r.rest)
	}
	if !strings.Contains(usageLine, "tui") {
		t.Error("usageLine does not mention tui")
	}
	if !strings.Contains(helpText, "tui") {
		t.Error("helpText does not mention tui")
	}
}

// helpText-mentions test (operator-command-surface rule; pattern:
// args_test.go's TestHelpText_MentionsActivityRingEnvVar). PR_POOL_TUI_INTERVAL
// is new operator-facing surface introduced by this packet.
func TestHelpText_MentionsTUIIntervalEnvVar(t *testing.T) {
	if !strings.Contains(helpText, "PR_POOL_TUI_INTERVAL") {
		t.Fatal("helpText does not mention PR_POOL_TUI_INTERVAL")
	}
}

// resolveTUIInterval implements spec §11 (comp-8)'s env-vs-default half of
// the TUI poll interval precedence: unset -> the built-in default; a parsed
// value below the floor is clamped UP to the floor rather than rejected; a
// value that fails to parse as a duration is a usage error naming the bad
// value.
func TestResolveTUIInterval_FloorClamp(t *testing.T) {
	cases := []struct {
		name    string
		envVal  string
		want    time.Duration
		wantErr bool
	}{
		{"unset resolves to the built-in default", "", tuiIntervalDefault, false},
		{"below the floor clamps up to the floor", "50ms", tuiIntervalFloor, false},
		{"at or above the floor passes through", "2s", 2 * time.Second, false},
		{"malformed value is a usage error", "banana", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTUIInterval(tc.envVal)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveTUIInterval(%q) = %v, <nil>; want an error", tc.envVal, got)
				}
				if !strings.Contains(err.Error(), tc.envVal) {
					t.Fatalf("error %q does not name the bad value %q", err.Error(), tc.envVal)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTUIInterval(%q) unexpected error: %v", tc.envVal, err)
			}
			if got != tc.want {
				t.Fatalf("resolveTUIInterval(%q) = %v, want %v", tc.envVal, got, tc.want)
			}
		})
	}
}

// Unlike every other operator subcommand, runTUI NEVER fails when no core is
// running (ADR 0036): locateCore's error is deliberately ignored, and
// tui.Run (via a real SocketPoller, never a nil one) is called anyway --
// the poller decides when/whether a core is up, not this route. The real
// tui.Run needs a live terminal it does not have here, so this test stubs
// the tuiRun seam and confirms only that runTUI reaches the hand-off (never
// bails out early on locateCore's error) and relays a clean outcome as
// exitOK.
func TestRunTUI_NeverFailsOnNoCore(t *testing.T) {
	dir := shortDir(t)
	var gotOpts tui.Options
	called := false
	withStubTUIRun(t, func(opts tui.Options) error {
		called = true
		gotOpts = opts
		return nil
	})

	code := runTUI([]string{"--socket", filepath.Join(dir, "gone.sock")})
	if code != exitOK {
		t.Fatalf("runTUI with no core running = %d, want %d (never fails on no core, ADR 0036)", code, exitOK)
	}
	if !called {
		t.Fatal("runTUI returned without ever reaching the tui.Run hand-off")
	}
	if gotOpts.Poller == nil {
		t.Error("runTUI called tui.Run with a nil Poller; want a real SocketPoller even with no core discoverable")
	}
	if gotOpts.PollInterval != tuiIntervalDefault {
		t.Errorf("runTUI called tui.Run with PollInterval %v, want the resolved default %v", gotOpts.PollInterval, tuiIntervalDefault)
	}
}

// TestRunTUI_RelaysTUIRunFailure: when the hand-off itself fails (e.g. a
// real terminal was never attached), runTUI reports it and exits non-zero
// rather than swallowing it — ADR 0036's "never fails on no core" is about
// core presence specifically, not about every possible failure downstream.
func TestRunTUI_RelaysTUIRunFailure(t *testing.T) {
	withStubTUIRun(t, func(tui.Options) error {
		return errors.New("stub tui.Run failure")
	})
	dir := shortDir(t)
	code := runTUI([]string{"--socket", filepath.Join(dir, "gone.sock")})
	if code != conformance.ExitError {
		t.Fatalf("runTUI with a failing tui.Run = %d, want %d/ExitError", code, conformance.ExitError)
	}
}

// A usage error on `tui` exits 2, the same fail-fast contract every other
// operator subcommand follows (pg2-52rn).
func TestRunTUI_UsageErrors(t *testing.T) {
	if code := runTUI([]string{"--nope"}); code != conformance.ExitUsage {
		t.Fatalf("unknown flag exit = %d, want %d/usage", code, conformance.ExitUsage)
	}
	if code := runTUI([]string{"extra-positional"}); code != conformance.ExitUsage {
		t.Fatalf("positional exit = %d, want %d/usage", code, conformance.ExitUsage)
	}
}
