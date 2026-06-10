// Package tmux adapts the tmux CLI. Every call targets the dedicated -L socket
// (spec §3) so pool sessions are isolated from default-socket tooling.
package tmux

// HasSession is a convenience for liveness reconcile without holding a Client.
func HasSession(socket, target string) bool {
	return NewClient(socket).HasSession(target)
}
