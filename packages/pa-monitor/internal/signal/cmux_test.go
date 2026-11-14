package signal_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// TestRunCmux_EnrichesStderr verifies the shared exec seam (used by the
// CmuxSignaler and the cmuxstatus reporter) folds a failing subprocess's stderr
// into the error instead of a bare "exit status N".
func TestRunCmux_EnrichesStderr(t *testing.T) {
	_, err := signal.RunCmux(context.Background(), "sh", "-c", "echo detail-on-stderr 1>&2; exit 3")
	if err == nil {
		t.Fatal("expected an error from the failing subprocess")
	}
	if !strings.Contains(err.Error(), "detail-on-stderr") {
		t.Errorf("error %q does not include the captured stderr", err.Error())
	}
}

// TestEnrichCmdErr_IncludesStderr verifies that a failed subprocess error is
// augmented with the command's captured stderr. exec.ExitError.Error() renders
// only "exit status N" and drops Stderr, so a cmux failure logged "exit status
// 1" with no clue WHY. The enriched error must surface cmux's own stderr so the
// failing path is diagnosable.
func TestEnrichCmdErr_IncludesStderr(t *testing.T) {
	// A real *exec.ExitError with populated Stderr, exactly as .Output() yields.
	_, err := exec.CommandContext(context.Background(), "sh", "-c", "echo cmux-boom >&2; exit 1").Output()
	if err == nil {
		t.Fatal("expected a non-nil error from a command that exits 1")
	}
	got := signal.EnrichCmdErrForTest(err)
	if !strings.Contains(got.Error(), "cmux-boom") {
		t.Errorf("enriched error = %q, want it to include the captured stderr 'cmux-boom'", got.Error())
	}
	if !strings.Contains(got.Error(), "exit status 1") {
		t.Errorf("enriched error = %q, want it to preserve the exit status", got.Error())
	}
}

// TestEnrichCmdErr_PassesThroughNonExitError verifies non-ExitError errors
// (context timeout, binary-not-found) are returned unchanged.
func TestEnrichCmdErr_PassesThroughNonExitError(t *testing.T) {
	base := errors.New("context deadline exceeded")
	if got := signal.EnrichCmdErrForTest(base); got != base {
		t.Errorf("non-ExitError should pass through unchanged; got %v, want %v", got, base)
	}
}

// stubEnv returns a LookupEnv whose values come from m.
func stubEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}
