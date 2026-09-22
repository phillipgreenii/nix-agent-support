package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr to a pipe for the duration of fn and
// returns everything written to it alongside fn's exit code. Both tests below
// exercise only runClose's flag-validation path, which returns before
// buildService is ever reached, so this never touches a real store or tmux
// server (the "Unit tests MUST be isolated" global constraint).
func captureStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	code := fn()
	_ = w.Close()
	os.Stderr = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out), code
}

// TestClose_rejectsUnknownReasonFlag: an unrecognized -reason must exit 2 and
// name the full vocabulary, without ever reaching buildService.
func TestClose_rejectsUnknownReasonFlag(t *testing.T) {
	out, code := captureStderr(t, func() int {
		return runClose([]string{"alpha", "-reason", "because"})
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr: %s)", code, out)
	}
	for _, want := range []string{"idle_ttl", "cap_eviction", "operator", "handler"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr %q must name the close-reason vocabulary (missing %q)", out, want)
		}
	}
}

// TestClose_purgeWithReasonWarns: --purge combined with a non-default reason
// must warn that the reason is discarded (closeWithReason skips the stamp on
// purge). Exercised via the pure decision function directly — purge=true with
// a valid non-default reason would otherwise fall through runClose into
// buildService (real config/store/tmux), which a unit test must not touch.
func TestClose_purgeWithReasonWarns(t *testing.T) {
	if got := purgeReasonWarning(true, "handler"); !strings.Contains(got, "reason discarded") {
		t.Errorf("purgeReasonWarning(purge, handler) = %q, want it to mention reason discarded", got)
	}
	if got := purgeReasonWarning(true, "operator"); got != "" {
		t.Errorf("purgeReasonWarning(purge, operator) = %q, want empty (operator is the default)", got)
	}
	if got := purgeReasonWarning(false, "handler"); got != "" {
		t.Errorf("purgeReasonWarning(no purge, handler) = %q, want empty (no purge, no warning)", got)
	}
}
