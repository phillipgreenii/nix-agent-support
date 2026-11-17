// Package supervisor holds the daemon's tmux supervision settings.
package supervisor

// TmuxConfLine is the one line of the daemon's tmux.conf: a pane whose
// command has exited stays on screen as a dead pane, so the daemon can read
// its exit status and tail before it restarts the assistant.
const TmuxConfLine = "set -g remain-on-exit on"
