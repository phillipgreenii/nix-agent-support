package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
	"github.com/phillipgreenii/x/exitcode"
	"github.com/phillipgreenii/x/tmux"
)

const attachHelp = `pg-assistants attach: attach to an assistant's tmux session.

usage: pg-assistants attach <id> [--config PATH]

Runs tmux -L pg-assistants attach -t =<id> on the assistants' own tmux
server, with TMUX removed from the environment so it also works from inside
your own tmux. Detach with C-b d; the assistant keeps running.

Exit 4 when the server has no session <id> (see pg-assistants status), 3 when
no assistants' tmux server is running, 7 when the configuration cannot be
loaded, 2 on a usage error or an id that is not [a-z0-9-]+.
`

// attach is `pg-assistants attach <id>`: it replaces the process with tmux
// attached to the session. The id is checked before anything else: it is
// passed to tmux as a target and must be [a-z0-9-]+.
func (c *cli) attach(ctx context.Context, args []string) int {
	fs := c.newFlagSet("attach")
	cfgPath := configFlag(fs)
	pos, code, done := c.parse(fs, args, attachHelp)
	if done {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(c.stderr, "usage: pg-assistants attach <id> [--config PATH]")
		return exitcode.Usage
	}
	id := pos[0]
	if !supervisor.ValidID(id) {
		fmt.Fprintf(c.stderr, "pg-assistants attach: %q is not a valid assistant id ([a-z0-9-]+)\n", id)
		return exitcode.Usage
	}
	cfg, code, ok := c.loadConfig("attach", *cfgPath)
	if !ok {
		return code
	}
	p := cfg.Paths
	conf := tmuxConfPath(p.StateDir)
	// x/tmux reports a server that is not running as an empty list, and a
	// tmux server exits with its last session, so an empty list is no server.
	names, err := tmux.Client{Bin: p.Tmux, Socket: tmuxSocket, Conf: conf, Run: c.run}.List(ctx)
	switch {
	case err != nil:
		fmt.Fprintf(c.stderr, "pg-assistants attach: %v\n", err)
		return exitcode.Unavailable
	case len(names) == 0:
		fmt.Fprintf(c.stderr, "pg-assistants attach: no tmux server is running on socket %s: is the daemon running? see pg-assistants status\n", tmuxSocket)
		return exitcode.Unavailable
	case !slices.Contains(names, id):
		fmt.Fprintf(c.stderr, "pg-assistants attach: no session %q on the %s tmux server: see pg-assistants status\n", id, tmuxSocket)
		return exitcode.NotFound
	}
	argv := []string{p.Tmux, "-L", tmuxSocket, "-f", conf, "attach", "-t", "=" + id}
	if err := c.exec(p.Tmux, argv, withoutVar(c.environ(), "TMUX")); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants attach: exec %s: %v\n", p.Tmux, err)
		return exitcode.Error
	}
	return exitcode.OK // only a test's exec returns
}

// withoutVar returns env without any entry for name.
func withoutVar(env []string, name string) []string {
	return slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, name+"=") })
}
