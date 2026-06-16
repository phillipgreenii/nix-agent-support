// Package tmux adapts the tmux CLI. Every call targets the dedicated -L socket
// (spec §3) so pool sessions are isolated from default-socket tooling.
package tmux

// HasSession is a convenience for liveness reconcile without holding a Client.
func HasSession(socket, target string) bool {
	return NewClient(socket).HasSession(target)
}

// PaneCurrentPath is a convenience for resolving a session's live pane cwd
// without holding a Client, mirroring HasSession. Used as the injected
// path-resolver in list --json so the renderer stays pure.
func PaneCurrentPath(socket, target string) (string, error) {
	return NewClient(socket).PaneCurrentPath(target)
}
