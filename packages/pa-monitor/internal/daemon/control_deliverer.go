package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

// ControlCommand is the one command pa-monitor sends through: submit, interrupt
// and stop all go to a running Claude by pg-claude-control, which resolves the
// target through the registry and writes one line into the Claude's inbox. It
// does not depend on the recorder, so a recorder outage never stops a nudge.
const ControlCommand = "pg-claude-control"

// controlTimeout bounds one pg-claude-control invocation.
const controlTimeout = 15 * time.Second

// CommandRunner runs name with args and stdin and returns the command's standard
// error text with its error (a test seam; execRunner in production).
type CommandRunner func(ctx context.Context, name string, stdin string, args ...string) (stderr string, err error)

func execRunner(ctx context.Context, name string, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	return errb.String(), err
}

// controlDeliverer delivers a nudge by `pg-claude-control submit <pid>`.
type controlDeliverer struct {
	run     CommandRunner
	command string
}

var _ nudger.Deliverer = (*controlDeliverer)(nil)

func newControlDeliverer() *controlDeliverer {
	return &controlDeliverer{run: execRunner, command: ControlCommand}
}

// Deliver submits text to the Claude with pid. A failure carries the command's
// own error name and message (no_such_process, no_control_mod, ...).
func (d *controlDeliverer) Deliver(ctx context.Context, targetPID int, text string) error {
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	stderr, err := d.run(ctx, d.command, text, "submit", "pid:"+strconv.Itoa(targetPID))
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("pg-claude-control submit pid %d: exit %d: %s", targetPID, ee.ExitCode(), msg)
	}
	return fmt.Errorf("pg-claude-control submit pid %d: %s", targetPID, msg)
}
