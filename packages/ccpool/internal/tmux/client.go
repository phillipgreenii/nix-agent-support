package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Client is the tmux adapter. All operations target the dedicated -L socket.
// run is injectable for tests; production uses execRun.
type Client struct {
	Socket   string
	run      func(args ...string) ([]byte, error)
	runStdin func(stdin string, args ...string) ([]byte, error)
}

// NewClient returns a Client bound to socket, shelling out to the real tmux.
func NewClient(socket string) *Client {
	c := &Client{Socket: socket}
	c.run = c.execRun
	c.runStdin = c.execRunStdin
	return c
}

func (c *Client) execRun(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).CombinedOutput()
}

func (c *Client) execRunStdin(stdin string, args ...string) ([]byte, error) {
	cmd := exec.Command("tmux", args...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
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
// carries the markers). Session-level -e is load-bearing: a marker placed only
// on the `claude` child is a grandchild of the pane shell and so invisible to a
// nudger that reads the pane pid's env. env keys are sorted for deterministic argv.
func (c *Client) NewSession(name, cwd string, env map[string]string, argv []string) error {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd) // session working directory (the project dir)
	}
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

// HasSession reports liveness on this client's socket. An empty target is never
// live: `tmux has-session -t ""` matches the first/any session on the socket, so
// querying it would falsely report live whenever any session exists (e.g. a
// hook-created row whose TmuxSession is still empty). Short-circuit instead.
func (c *Client) HasSession(name string) bool {
	if name == "" {
		return false
	}
	_, err := c.tmux("has-session", "-t", name)
	return err == nil
}

// PaneCurrentPath returns the live current working directory of the session's
// active pane (tmux display-message -p '#{pane_current_path}'), trimming the
// trailing newline tmux appends. Used to report the LIVE cwd in list --json,
// distinct from the launch cwd recorded in the store (spec: live session-location
// facets). Errors when the session is not live or the query fails; callers
// fall back to the launch cwd.
func (c *Client) PaneCurrentPath(name string) (string, error) {
	out, err := c.tmux("display-message", "-p", "-t", name, "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Paste delivers body to the session's input via bracketed paste, so multi-line
// and special-char prompts arrive as a single message. Verified against Claude
// Code 2.1.170: a pasted multi-line prompt produced ONE turn / one Stop, whereas
// raw send-keys of the same body would submit line-by-line, and `;`, `\` and
// key-name tokens would be reinterpreted. Caller sends Enter separately to submit.
func (c *Client) Paste(name, body string) error {
	const buf = "ccpool-paste"
	if _, err := c.runStdin(body, "-L", c.Socket, "load-buffer", "-b", buf, "-"); err != nil {
		return fmt.Errorf("tmux load-buffer: %w", err)
	}
	_, err := c.tmux("paste-buffer", "-p", "-d", "-b", buf, "-t", name)
	return err
}

// CapturePane returns the visible pane text of the session (tmux capture-pane
// -p). Used to verify a cancel actually interrupted the turn.
func (c *Client) CapturePane(name string) (string, error) {
	out, err := c.tmux("capture-pane", "-p", "-t", name)
	return string(out), err
}
