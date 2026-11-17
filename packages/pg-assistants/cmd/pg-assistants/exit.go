package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/supervisor"
	"github.com/phillipgreenii/x/exitcode"
)

const exitHelp = `pg-assistants exit: ask the daemon to stop this session.

usage: pg-assistants exit

Run it from inside a pg-assistants session, as a command on its own (the
session is started with only that exact command pre-allowed), then end your
turn. It reads PG_ASSISTANT_ID and PG_ASSISTANTS_STATE_DIR, which the daemon
sets in every session, and writes <state dir>/exit-requests/<id>; on its next
tick the daemon stops the session gracefully and starts the next one.

Exit 0 when the request is written, 2 when either variable is missing or
invalid, 1 when the file cannot be written.
`

const (
	envAssistantID = "PG_ASSISTANT_ID"
	envStateDir    = "PG_ASSISTANTS_STATE_DIR"
)

// exitCmd is `pg-assistants exit`. The id is checked before anything is
// written: it becomes part of a file path.
func (c *cli) exitCmd(args []string) int {
	fs := c.newFlagSet("exit")
	pos, code, done := c.parse(fs, args, exitHelp)
	if done {
		return code
	}
	if len(pos) != 0 {
		fmt.Fprintln(c.stderr, "usage: pg-assistants exit")
		return exitcode.Usage
	}
	id, stateDir := c.getenv(envAssistantID), c.getenv(envStateDir)
	switch {
	case id == "":
		fmt.Fprintf(c.stderr, "pg-assistants exit: %s is not set: run this from inside a pg-assistants session\n", envAssistantID)
		return exitcode.Usage
	case !supervisor.ValidID(id):
		fmt.Fprintf(c.stderr, "pg-assistants exit: %s %q is not a valid id ([a-z0-9-]+)\n", envAssistantID, id)
		return exitcode.Usage
	case stateDir == "":
		fmt.Fprintf(c.stderr, "pg-assistants exit: %s is not set: run this from inside a pg-assistants session\n", envStateDir)
		return exitcode.Usage
	case !filepath.IsAbs(stateDir):
		fmt.Fprintf(c.stderr, "pg-assistants exit: %s %q is not an absolute path\n", envStateDir, stateDir)
		return exitcode.Usage
	}
	if err := supervisor.WriteExitRequest(stateDir, id, time.Now()); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants exit: %v\n", err)
		return exitcode.Error
	}
	fmt.Fprintf(c.stdout, "exit requested for %s; end your turn now\n", id)
	return exitcode.OK
}
