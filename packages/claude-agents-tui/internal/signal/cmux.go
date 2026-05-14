package signal

import (
	"context"
	"os"
	"os/exec"
)

// CmuxSignaler sends keys to the cmux surface hosting a process.
// RunCmd and LookupEnv are injectable for tests; nil values fall back to
// exec.CommandContext and os.LookupEnv respectively.
type CmuxSignaler struct {
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)
}

func (c *CmuxSignaler) Name() string { return "cmux" }

func (c *CmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.RunCmd != nil {
		return c.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

func (c *CmuxSignaler) lookupEnv(key string) (string, bool) {
	if c.LookupEnv != nil {
		return c.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// Detect returns true when claude-agents-tui is itself running inside cmux.
// Outside cmux the signaler is silently inert.
func (c *CmuxSignaler) Detect(pid int) bool {
	_ = pid // cmux's socket is instance-global; reachability depends on the caller, not the target.
	v, _ := c.lookupEnv("CMUX_WORKSPACE_ID")
	return v != ""
}

// Send injects text followed by Enter into the cmux surface hosting pid.
func (c *CmuxSignaler) Send(pid int, text string) error {
	// TODO Task 4-6: enumerate, match, send.
	_ = pid
	_ = text
	return ErrNotImplemented
}
