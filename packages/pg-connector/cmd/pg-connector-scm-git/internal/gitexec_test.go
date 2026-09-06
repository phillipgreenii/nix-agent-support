package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// gitStubExitingWithStderr puts an executable named `git` on PATH that
// writes stderrMsg to its standard error and exits with exitCode — the
// same shape ghexec_test.go's ghStubExitingWithStderr establishes for the
// gh-based backends, applied to `git` for this one.
func gitStubExitingWithStderr(t *testing.T, exitCode int, stderrMsg string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'GITSTUBEOF' >&2\n" + stderrMsg + "\nGITSTUBEOF\nexit " + fmt.Sprint(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestExecRunner_Run_ErrorMessage_CapsStderr is the regression test for
// bead pg2-332z8 #26: before TruncateForFold, execRunner.Run folded git's
// ENTIRE captured stderr into the returned error with no bound — a
// runaway or unexpectedly verbose git failure could produce an unbounded
// error string.
func TestExecRunner_Run_ErrorMessage_CapsStderr(t *testing.T) {
	huge := strings.Repeat("e", scriptout.MaxFoldedOutputBytes*3)
	gitStubExitingWithStderr(t, 1, huge)

	r := NewExecRunner()
	_, err := r.Run(context.Background(), "", "status")
	if err == nil {
		t.Fatal("expected error from the failing git stub")
	}
	if got := len(err.Error()); got > scriptout.MaxFoldedOutputBytes+256 {
		t.Fatalf("error message is %d bytes; stderr fold was not capped", got)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation marker in the error, got a %d-byte message", len(err.Error()))
	}
}
