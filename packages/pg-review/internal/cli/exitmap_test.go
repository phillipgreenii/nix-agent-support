package cli

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/x/exitcode"
)

func TestExitCodeMapping(t *testing.T) {
	if got := ExitCode(nil); got != exitcode.OK {
		t.Errorf("nil: %d", got)
	}
	if got := ExitCode(errors.New("x")); got != exitcode.Error {
		t.Errorf("plain: %d", got)
	}
	if got := ExitCode(usageErrorf("x")); got != exitcode.Usage {
		t.Errorf("usage: %d", got)
	}
	if got := ExitCode(ExitErrorf(exitcode.Busy, "busy")); got != exitcode.Busy {
		t.Errorf("busy: %d", got)
	}
}
