package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/x/exitcode"
)

// controlBin is the pg-claude-control command (found on PATH).
const controlBin = "pg-claude-control"

// pg-claude-control exit codes ccpool branches on: the shared x/exitcode codes plus the command's
// own app code (its README table). Before the shared codes these were 3, 4, 5 and 9.
const (
	ctlNoSuchProcess = exitcode.NotFound    // not a running Claude
	ctlNoControlMod  = exitcode.Unavailable // the Claude has no control mod
	ctlNotAccepting  = exitcode.Busy        // the inbox has no reader, or its buffer is full
	ctlSessionStayed = 20                   // stop: exit was sent but the process is still running
)

// controlRunFunc runs the control command and reports its exit code and stderr.
// A seam: production execs the binary; tests answer in process.
type controlRunFunc func(ctx context.Context, args []string, stdin string) (exit int, stderr string, err error)

// controlClient is session.Control over the pg-claude-control command. Control
// does not depend on the recorder: a recorder outage leaves it working.
type controlClient struct {
	run controlRunFunc
}

func newControlClient() *controlClient { return &controlClient{run: execControl} }

func execControl(ctx context.Context, args []string, stdin string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, controlBin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, "", nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), strings.TrimSpace(stderr.String()), nil
	}
	return -1, "", err
}

func (c *controlClient) do(ctx context.Context, stdin string, args ...string) error {
	exit, stderr, err := c.run(ctx, args, stdin)
	if err != nil {
		// The command is not installed or could not start: control is not reachable.
		return fmt.Errorf("%w: %v", session.ErrNoControl, err)
	}
	switch exit {
	case 0:
		return nil
	case ctlNoSuchProcess, ctlNoControlMod, ctlNotAccepting:
		return fmt.Errorf("%w: pg-claude-control exit %d: %s", session.ErrNoControl, exit, stderr)
	case ctlSessionStayed:
		return fmt.Errorf("%w: pg-claude-control %s: exit %d: %s", session.ErrSessionStayed, args[0], exit, stderr)
	}
	return fmt.Errorf("pg-claude-control %s: exit %d: %s", args[0], exit, stderr)
}

func sessionTarget(id string) string { return "session:" + id }

func (c *controlClient) Submit(ctx context.Context, id, text string) error {
	return c.do(ctx, text, "submit", sessionTarget(id))
}

func (c *controlClient) Interrupt(ctx context.Context, id string) error {
	return c.do(ctx, "", "interrupt", sessionTarget(id))
}

func (c *controlClient) Run(ctx context.Context, id, command string) error {
	args := append([]string{"run", sessionTarget(id)}, strings.Fields(command)...)
	return c.do(ctx, "", args...)
}

func (c *controlClient) Stop(ctx context.Context, id string) error {
	return c.do(ctx, "", "stop", sessionTarget(id))
}
