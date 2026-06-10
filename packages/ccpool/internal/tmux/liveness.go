// Package tmux adapts the tmux CLI. Plan 1 contains only liveness; the full
// send/new/kill adapter arrives in Plan 2. Every call targets the dedicated
// -L socket (spec §3) so pool sessions are isolated from default-socket tooling.
package tmux

import "os/exec"

// HasSession reports whether session `target` is alive on socket `socket`.
// A missing server or session both yield false; errors are swallowed because
// liveness is a best-effort reconcile, never a hard failure (spec §6/§15).
func HasSession(socket, target string) bool {
	return exec.Command("tmux", "-L", socket, "has-session", "-t", target).Run() == nil
}
