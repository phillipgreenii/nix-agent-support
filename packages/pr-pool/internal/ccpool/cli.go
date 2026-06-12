package ccpool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

// ErrCancelUnconfirmed is returned by Cancel when `ccpool cancel` exits 6
// (the interrupt could not be confirmed — the turn may still be running).
var ErrCancelUnconfirmed = errors.New("ccpool cancel unconfirmed")

// exitCoder is satisfied by *exec.ExitError (and test fakes).
type exitCoder interface{ ExitCode() int }

// CLIRunner is the Phase-1 Runner: it shells out to the `ccpool` binary on PATH.
// run is injectable for tests (zero real processes), exactly like ccpool's
// tmux.Client. The launch-flag fields come from config and are emitted on
// `ccpool new` per the agreed contract (ccpool N2 — see pg2-7mnq.3).
type CLIRunner struct {
	Effort    string
	Model     string
	Dangerous bool
	run       func(args []string) ([]byte, error)
}

func NewCLIRunner(cfg config.Config) *CLIRunner {
	c := &CLIRunner{Effort: cfg.Effort, Model: cfg.Model, Dangerous: cfg.Dangerous}
	c.run = func(args []string) ([]byte, error) {
		return exec.Command("ccpool", args...).CombinedOutput()
	}
	return c
}

func (c *CLIRunner) ccpool(args ...string) ([]byte, error) {
	out, err := c.run(args)
	if err != nil {
		return out, fmt.Errorf("ccpool %v: %w (%s)", args, err, bytes.TrimSpace(out))
	}
	return out, nil
}

// Ensure: ccpool new <name> --cwd <cwd> --env K=V… --dangerously-skip-permissions
// --effort <effort> [--model <model>]. env keys sorted for deterministic argv.
func (c *CLIRunner) Ensure(_ context.Context, name, cwd string, env map[string]string) error {
	args := []string{"new", name, "--cwd", cwd}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+env[k])
	}
	if c.Dangerous {
		args = append(args, "--dangerously-skip-permissions")
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	_, err := c.ccpool(args...)
	return err
}

// Send: ccpool reply <name> <prompt> <mode-flag>.
func (c *CLIRunner) Send(_ context.Context, name, prompt string, mode SendMode) error {
	flag := "--no-wait"
	switch mode {
	case ModeInterrupt:
		flag = "--interrupt"
	case ModeQueue:
		flag = "--queue-message"
	}
	_, err := c.ccpool("reply", name, prompt, flag)
	return err
}

func (c *CLIRunner) Cancel(_ context.Context, name string) error {
	_, err := c.ccpool("cancel", name)
	if err != nil {
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 6 {
			return fmt.Errorf("%w: %s", ErrCancelUnconfirmed, name)
		}
		return err
	}
	return nil
}

func (c *CLIRunner) Close(_ context.Context, name string) error {
	_, err := c.ccpool("close", name)
	return err
}

// List: ccpool list --all --json.
func (c *CLIRunner) List(_ context.Context) ([]Session, error) {
	out, err := c.ccpool("list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var sessions []Session
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, fmt.Errorf("ccpool list --json decode: %w", err)
	}
	return sessions, nil
}
