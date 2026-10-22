package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
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
//
// remain-on-exit is set to "failed" on the new pane in the same tmux invocation
// as the creation, so it is in force before the process can possibly exit (the
// launch wait's fail-fast depends on that). It was added by pg2-olchz, a
// follow-up to pg2-5sirm's investigation: without it, the pane — and, since
// this is always a single-window session, the WHOLE session — is destroyed the
// instant the foreground `claude` process exits for ANY reason, so a crash
// leaves zero forensic trail (no stderr, no exit status, nothing to attach to).
// "failed" (never "on") is load-bearing: it only preserves the pane when the
// process's exit status is non-zero or it was signal-killed — a CLEAN exit
// (status 0, e.g. cancel_close.go's Close() sending /exit) still destroys the
// pane+session immediately. That matters because HasSession below is the one
// liveness signal nearly every caller trusts as "the process is actually
// running" (session.go's reuse-live branch, reap.go's phantom-prune, Close's
// fast waitGone path, and pg-router-ccpool-handler's active()/Live-based crash
// detection) — "on" would make every one of those see a crashed session as
// still live forever, silently breaking self-healing relaunch AND turning the
// fast "session exited before completing" failure into a full MaxWait timeout.
// "failed" leaves all of that untouched on the clean-exit path while still
// preserving the pane on a crash; see HasSession's own doc comment for the
// matching pane_dead check that keeps its semantics correct once a crashed
// pane sticks around.
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
	// remain-on-exit is set in the SAME tmux invocation as the creation (a
	// second command after a ";" separator), so the server runs both before it
	// can observe the pane's process exit: a payload that fails within
	// milliseconds still leaves its dead pane, status and output behind. As two
	// separate tmux calls it could already be gone (and its stderr with it).
	// Every caller-supplied argument is escaped so none can be read as that
	// separator.
	for i := range args {
		args[i] = escapeSemicolon(args[i])
	}
	args = append(args, ";", "set-option", "-t", name, "remain-on-exit", "failed")
	if _, err := c.tmux(args...); err != nil {
		return err
	}
	return nil
}

// escapeSemicolon protects an argument that ends in ";" from being read by tmux
// as a command separator: tmux splits a command line on arguments ending in an
// unescaped ";", and a "\;" ending is the escaped literal.
func escapeSemicolon(a string) string {
	if strings.HasSuffix(a, ";") {
		return a[:len(a)-1] + `\;`
	}
	return a
}

// PaneStatus is what the tmux server knows about a session's pane.
type PaneStatus struct {
	// Exists is false when the session is not on the socket at all (a process
	// that exited with status 0 destroys its session, since remain-on-exit is
	// "failed", and with it the last window).
	Exists bool
	// Dead is true when the session exists but its process has ended (kept by
	// remain-on-exit failed).
	Dead bool
	// Status is the process's exit status (valid when Dead and Signal == 0).
	Status int
	// Signal names the signal that ended the process, as tmux reports it (a
	// lowercase name such as "term" on current tmux, a number on older ones),
	// or "" when the process exited.
	Signal string
}

// PaneStatus reports whether the session exists and, if so, whether its pane's
// process has ended with which exit status or signal. has-session is queried
// first and gates the pane query for the reason HasSession documents:
// display-message does not resolve its target strictly.
func (c *Client) PaneStatus(name string) (PaneStatus, error) {
	if name == "" {
		return PaneStatus{}, nil
	}
	if _, err := c.tmux("has-session", "-t", name); err != nil {
		return PaneStatus{}, nil
	}
	out, err := c.tmux("display-message", "-p", "-t", name, "#{pane_dead} #{pane_dead_status} #{pane_dead_signal}")
	if err != nil {
		return PaneStatus{}, err
	}
	// Split on SINGLE spaces, never Fields: an empty status (a signal death) or
	// an empty signal (an exit) must keep its position. Absent or empty fields
	// read as 0.
	parts := strings.Split(strings.TrimRight(string(out), "\n"), " ")
	ps := PaneStatus{Exists: true, Dead: parts[0] == "1"}
	if len(parts) >= 2 {
		ps.Status, _ = strconv.Atoi(parts[1])
	}
	if len(parts) >= 3 {
		ps.Signal = strings.TrimSpace(parts[2])
	}
	return ps, nil
}

// CaptureTail returns the last lines lines of the session's pane output,
// scrollback included (capture-pane -p -S -<lines>), blank lines at both ends
// trimmed. CapturePane returns only the visible screen.
func (c *Client) CaptureTail(name string, lines int) (string, error) {
	if lines <= 0 {
		lines = 1
	}
	out, err := c.tmux("capture-pane", "-p", "-t", name, "-S", "-"+strconv.Itoa(lines))
	if err != nil {
		return "", err
	}
	// The capture is the whole screen plus scrollback: trim the blank rows
	// around the text so the tail is the output itself.
	all := strings.Split(strings.Trim(string(out), " \t\r\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
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

// HasSession reports liveness on this client's socket: the session exists AND
// its pane's foreground process is actually still running. An empty target is
// never live: `tmux has-session -t ""` matches the first/any session on the
// socket, so querying it would falsely report live whenever any session exists
// (e.g. a hook-created row whose TmuxSession is still empty). Short-circuit
// instead.
//
// Two tmux calls, deliberately in this order and NOT collapsed into one
// display-message query. `has-session -t <name>` is the ONLY one of the two
// with strict exact-match target resolution: given a name that does not exist
// on the socket, it errors (verified against tmux 3.6a). `display-message -p
// -t <name>` does NOT — given a non-existent target it silently falls back to
// some other session on the socket (observed: with exactly one real session
// present, `display-message -p -t <nonexistent> '#{pane_dead}'` exits 0 with
// that session's own pane_dead, never erroring), so querying pane_dead FIRST
// (or alone) would read a completely different, unrelated session's liveness
// and falsely report a brand-new external_id's tmux session as already live
// (caught live: made `ccpool new second` route as reuse_live against a
// same-socket "first" session that was the only one to actually exist yet).
// has-session's exact-match check must gate the pane_dead query, not the other
// way around.
//
// The pane_dead check itself exists because NewSession now sets
// remain-on-exit=failed (pg2-olchz): a crashed session's pane survives on the
// socket for forensic inspection instead of vanishing, so `has-session` alone
// would report a crashed session as live — breaking every caller that treats
// HasSession as "the process is running" (session.go's reuse-live branch and
// phantom-prune reap would never relaunch a crashed session; pg-router-ccpool-
// handler's active()/Live check would wait the full MaxWait instead of
// failing fast). A pane_dead=1 reply is the "exists but not live" case.
func (c *Client) HasSession(name string) bool {
	if name == "" {
		return false
	}
	if _, err := c.tmux("has-session", "-t", name); err != nil {
		return false
	}
	out, err := c.tmux("display-message", "-p", "-t", name, "#{pane_dead}")
	if err != nil {
		return false
	}
	return strings.TrimRight(string(out), "\n") != "1"
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
//
// The tmux buffer name is per-call-unique (target session name + a uuid nonce),
// never a shared constant. paste-buffer's -d deletes the buffer immediately
// after use, so two concurrent Paste calls sharing one buffer name race:
// load-buffer B can overwrite load-buffer A's body before paste-buffer -d A
// runs, delivering the WRONG prompt to session A with no error at all, while
// the other side gets a loud "no buffer" error once it's already been deleted
// (pg2-xz6es). The name still carries the session name for readability when
// debugging a stray buffer.
func (c *Client) Paste(name, body string) error {
	buf := fmt.Sprintf("ccpool-paste-%s-%s", name, uuid.NewString())
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
