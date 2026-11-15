package signal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// CmuxSignaler names the cmux terminal environment. It classifies nothing and
// delivers nothing: the terminal host comes from the recorder's host fact and
// the bridge registry is keyed by workspace id. It remains only as a named
// entry in the signaler list that the startup binary check walks.
type CmuxSignaler struct{}

func (c *CmuxSignaler) Name() string { return "cmux" }

// RequiredBinaries reports the executables the cmux integration needs: the
// `cmux` CLI is run by the cmux status reporter (through RunCmux).
func (c *CmuxSignaler) RequiredBinaries() []string { return []string{"cmux"} }

// RunCmux is the single cmux-CLI exec seam: it runs the subprocess, returns its
// stdout, and augments a failure with the captured stderr (enrichCmdErr) so a
// failing `cmux` surfaces its cause instead of a bare "exit status N". The
// cmuxstatus reporter routes through it, so it neither duplicates the exec nor drops the stderr enrichment.
func RunCmux(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return out, enrichCmdErr(err)
}

// enrichCmdErr augments an *exec.ExitError with the subprocess's captured
// stderr. exec.ExitError.Error() renders only "exit status N" and drops
// Stderr, so a failing `cmux` call surfaced as a bare "exit status 1" with no
// clue why.
// .Output() populates ExitError.Stderr, so we fold it into the message.
// Non-ExitError errors (context timeout, binary-not-found) pass through.
func enrichCmdErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err
}
