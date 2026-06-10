package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
)

// Client is the tmux adapter. All operations target the dedicated -L socket
// (spec §3). run is injectable for tests; production uses execRun.
type Client struct {
	Socket string
	run    func(args ...string) ([]byte, error)
}

// NewClient returns a Client bound to socket, shelling out to the real tmux.
func NewClient(socket string) *Client {
	c := &Client{Socket: socket}
	c.run = c.execRun
	return c
}

func (c *Client) execRun(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).CombinedOutput()
}

func (c *Client) tmux(args ...string) ([]byte, error) {
	full := append([]string{"-L", c.Socket}, args...)
	out, err := c.run(full...)
	if err != nil {
		return out, fmt.Errorf("tmux %v: %w (%s)", full, err, bytes.TrimSpace(out))
	}
	return out, nil
}

// NewSession starts a detached session running argv, with env exported at the
// session level via -e (so the pane shell — the pid the nudger keys on —
// carries the markers, spec §8.1). env keys are sorted for deterministic argv.
func (c *Client) NewSession(name string, env map[string]string, argv []string) error {
	args := []string{"new-session", "-d", "-s", name}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, "--")
	args = append(args, argv...)
	_, err := c.tmux(args...)
	return err
}

// SendKeys sends literal key tokens (e.g. "Enter", "Escape") to the session.
func (c *Client) SendKeys(name string, keys ...string) error {
	args := append([]string{"send-keys", "-t", name}, keys...)
	_, err := c.tmux(args...)
	return err
}

// KillSession kills the session (force teardown).
func (c *Client) KillSession(name string) error {
	_, err := c.tmux("kill-session", "-t", name)
	return err
}

// HasSession reports liveness on this client's socket.
func (c *Client) HasSession(name string) bool {
	_, err := c.tmux("has-session", "-t", name)
	return err == nil
}

// ShowEnvironment returns the value of one session env var, or "" if unset.
func (c *Client) ShowEnvironment(name, key string) string {
	out, err := c.tmux("show-environment", "-t", name, key)
	if err != nil {
		return ""
	}
	// output is "KEY=value\n"; strip "KEY=".
	s := string(bytes.TrimSpace(out))
	if i := len(key) + 1; len(s) >= i && s[:i] == key+"=" {
		return s[i:]
	}
	return ""
}
