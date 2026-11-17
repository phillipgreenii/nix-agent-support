// Package supervisor is the daemon's supervision of its assistants: Run and
// its loops (one per assistant, one per orphan session, the orphan sweep and
// the status heartbeat), the launch command of a session, the status board
// and file, and the exit-request and last-exit files in the state directory.
// The decision record is ADR 0128.
package supervisor

// TmuxConfLine is the one line of the daemon's tmux.conf: a pane whose
// command has exited stays on screen as a dead pane, so the daemon can read
// its exit status and tail before it restarts the assistant.
const TmuxConfLine = "set -g remain-on-exit on"
