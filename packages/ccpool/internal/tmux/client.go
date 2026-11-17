// Package tmux adapts the shared x/tmux client to the shape ccpool's session
// code uses: no context argument, an environment map, and a HasSession that
// means "the pane's process is running". Every call targets the dedicated -L
// socket so pool sessions are isolated from default-socket tooling (this host
// runs agent tooling that injects input into idle Claude sessions).
package tmux

import (
	"context"
	"os/exec"
	"sort"

	"github.com/phillipgreenii/x/cmdrun"
	xtmux "github.com/phillipgreenii/x/tmux"
)

type (
	// PaneStatus is what the tmux server knows about a session's pane.
	PaneStatus = xtmux.PaneStatus
	// PaneInfo is one session's liveness and live cwd as read by ListPanes.
	PaneInfo = xtmux.PaneInfo
)

// Client is the tmux adapter. Socket names the -L socket.
type Client struct {
	Socket string
	x      xtmux.Client
}

// NewClient returns a Client bound to socket, shelling out to the real tmux.
//
// Sessions are created with remain-on-exit "failed", set in the same tmux
// invocation as the creation so it is in force before the process can exit: a
// crash then leaves its dead pane, exit status and output behind for
// PaneStatus and CaptureTail, while a clean exit (status 0, for example
// /exit sent by close) still destroys the session at once. "failed" and never
// "on" is load-bearing: HasSession is the one liveness signal most callers
// trust, and "on" would make a cleanly exited session look live forever.
//
// The user's own tmux configuration is still read, as ccpool always has.
func NewClient(socket string) *Client {
	return newClient(socket, nil)
}

func newClient(socket string, run cmdrun.Runner) *Client {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		bin = "tmux"
	}
	c := &Client{Socket: socket, x: xtmux.Client{Bin: bin, Socket: socket, UserConf: true, RemainOnExit: "failed"}}
	if run != nil {
		c.x.Run = run
	}
	return c
}

// NewSession starts a detached session running argv, with env exported at the
// session level via -e (so the pane shell, the pid the nudger keys on,
// carries the markers). Session-level -e is load-bearing: a marker placed only
// on the `claude` child is a grandchild of the pane shell and so invisible to a
// nudger that reads the pane pid's env. env keys are sorted for deterministic
// argv.
func (c *Client) NewSession(name, cwd string, env map[string]string, argv []string) error {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]string, 0, len(keys))
	for _, k := range keys {
		kv = append(kv, k+"="+env[k])
	}
	return c.x.NewSession(context.Background(), name, cwd, kv, argv)
}

// PaneStatus reports whether the session exists and, if so, how its pane's
// process ended. An empty name is no session.
func (c *Client) PaneStatus(name string) (PaneStatus, error) {
	if name == "" {
		return PaneStatus{}, nil
	}
	return c.x.PaneStatus(context.Background(), name)
}

// CaptureTail returns the last lines lines of the session's pane output,
// scrollback included, blank lines at both ends trimmed. CapturePane returns
// only the visible screen.
func (c *Client) CaptureTail(name string, lines int) (string, error) {
	return c.x.CaptureTail(context.Background(), name, lines)
}

// SendKeys sends literal key tokens (e.g. "Enter", "Escape") to the session.
func (c *Client) SendKeys(name string, keys ...string) error {
	return c.x.SendKeys(context.Background(), name, keys...)
}

// KillSession kills the session (force teardown); nil when it is already gone.
func (c *Client) KillSession(name string) error {
	return c.x.Kill(context.Background(), name)
}

// HasSession reports liveness on this client's socket: the session exists AND
// its pane's foreground process is actually still running. An empty target is
// never live, and an error reads as not live.
//
// A crashed session's pane survives on the socket (remain-on-exit failed), so
// "exists" alone would call a crashed session live and break every caller that
// treats this as "the process is running" (the reuse-live branch, the
// phantom-prune reap, the fast launch fail-fast). The exact-match has-session
// gates the pane query inside x/tmux, because display-message falls back to
// some other session for a target that does not exist.
func (c *Client) HasSession(name string) bool {
	if name == "" {
		return false
	}
	st, err := c.x.State(context.Background(), name)
	return err == nil && st.Exists && !st.Dead
}

// PaneCurrentPath returns the live current working directory of the session's
// active pane. Used to report the LIVE cwd in list --json, distinct from the
// launch cwd recorded in the store. Errors when the session is not live or the
// query fails; callers fall back to the launch cwd.
func (c *Client) PaneCurrentPath(name string) (string, error) {
	return c.x.PaneCurrentPath(context.Background(), name)
}

// ListPanes reads every session's liveness and active-pane path in ONE tmux
// launch, instead of HasSession's two plus PaneCurrentPath's one per session.
// A socket with no server running yields an empty map and no error; any other
// failure returns the error so the caller can fall back to per-session calls.
func (c *Client) ListPanes() (map[string]PaneInfo, error) {
	return c.x.ListPanes(context.Background())
}

// Paste delivers body to the session's input via bracketed paste, so
// multi-line and special-char prompts arrive as a single message. Verified
// against Claude Code 2.1.170: a pasted multi-line prompt produced one turn,
// whereas raw send-keys of the same body would submit line by line and
// reinterpret `;`, `\` and key-name tokens. The caller sends Enter separately.
func (c *Client) Paste(name, body string) error {
	return c.x.Paste(context.Background(), name, body)
}

// CapturePane returns the visible pane text of the session. Used to verify a
// cancel actually interrupted the turn.
func (c *Client) CapturePane(name string) (string, error) {
	return c.x.CapturePane(context.Background(), name)
}
