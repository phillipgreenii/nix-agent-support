package main

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// actionable turns a stable error name and the daemon's or client's message
// into the one line a person can act on.
func actionable(name, msg, socket string) string {
	switch name {
	case wire.ErrDaemonOffline:
		return fmt.Sprintf("daemon offline: %s; start it with the launch agent or run \"pg-claude-code-recorder serve\"", msg)
	case wire.ErrUnavailable:
		return fmt.Sprintf("the daemon on %s is unavailable: %s; check its log, then retry", socket, msg)
	case wire.ErrBehind, wire.ErrBusy:
		return msg + "; the answer is partial, retry later or pass --allow-partial during the first backfill"
	case wire.ErrSchemaRebuildRequired:
		return msg + "; stop the launch agent, run \"pg-claude-code-recorder rebuild\" (it takes hours on a large index; the old file is kept as index.db.pre-rebuild), then start the launch agent again"
	case wire.ErrUsage, wire.ErrBadRequest:
		return msg + "; see pg-claude-code-recorder --help"
	}
	return msg
}

// emitError prints the error in the requested shape and returns its exit code.
// Without --json, an "error: <name>" line then the actionable line go to
// standard error; with --json, one object does.
func (a *app) emitError(jsonMode bool, name, msg string) int {
	return a.emitErrorFor(jsonMode, name, msg, "")
}

func (a *app) emitErrorFor(jsonMode bool, name, msg, socket string) int {
	code := wire.ExitFor(name)
	line := actionable(name, msg, socket)
	if jsonMode {
		b, _ := json.Marshal(struct {
			Error   string `json:"error"`
			Exit    int    `json:"exit"`
			Message string `json:"message"`
		}{name, code, line})
		_, _ = fmt.Fprintln(a.errw, string(b))
		return code
	}
	_, _ = fmt.Fprintf(a.errw, "error: %s\n%s\n", name, line)
	return code
}

// emitWireError prints a *wire.Error (or any error) and returns its exit code.
func (a *app) emitWireError(jsonMode bool, err error, socket string) int {
	if we, ok := err.(*wire.Error); ok {
		return a.emitErrorFor(jsonMode, we.Name, we.Message, socket)
	}
	return a.emitErrorFor(jsonMode, wire.ErrInternal, err.Error(), socket)
}
