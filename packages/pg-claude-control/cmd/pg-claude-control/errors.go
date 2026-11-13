package main

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/x/exitcode"
)

// Exit codes: the shared x/exitcode codes where the meaning is generic, app codes 20 to 63 for the
// rest. Before the shared codes: no_such_process was 3, no_control_mod 4, mod_not_accepting 5,
// refused_by_mod 6, ambiguous_target 7, no_reply 8 and session_stayed 9.
const (
	exitOK           = exitcode.OK
	exitInternal     = exitcode.Error // reserved for unexpected errors
	exitUsage        = exitcode.Usage
	exitNoProcess    = exitcode.NotFound    // not a running Claude
	exitNoControlMod = exitcode.Unavailable // the Claude has no control mod
	exitNotAccepting = exitcode.Busy        // the inbox has no reader, or its buffer is full
	exitNoReply      = exitcode.Timeout     // no reply within --timeout

	// App-specific codes (range 20 to 63).
	exitStayed    = 20 // stop: exit was sent but the Claude is still running
	exitRefused   = 21 // the mod refused a request that carried a reply path
	exitAmbiguous = 22 // a name or session id matches several Claudes
)

// Stable error names (the --json "error" field; scripts match on these).
const (
	errUsage        = "usage"
	errNoProcess    = "no_such_process"
	errNoControlMod = "no_control_mod"
	errNotAccepting = "mod_not_accepting"
	errRefused      = "refused_by_mod"
	errAmbiguous    = "ambiguous_target"
	errNoReply      = "no_reply"
	errStayed       = "session_stayed"
	errInternal     = "internal"
)

var exitFor = map[string]int{
	errUsage:        exitUsage,
	errNoProcess:    exitNoProcess,
	errNoControlMod: exitNoControlMod,
	errNotAccepting: exitNotAccepting,
	errRefused:      exitRefused,
	errAmbiguous:    exitAmbiguous,
	errNoReply:      exitNoReply,
	errStayed:       exitStayed,
	errInternal:     exitInternal,
}

// cmdError is a failure with a stable name and an actionable message.
type cmdError struct {
	name string
	msg  string
}

func (e *cmdError) Error() string { return e.name + ": " + e.msg }

func fail(name, format string, a ...any) *cmdError {
	return &cmdError{name: name, msg: fmt.Sprintf(format, a...)}
}

// emitError prints the error in the requested shape and returns its exit code. Without --json an
// "error: <name>" line then the message go to standard error; with --json one object does.
func (a *app) emitError(jsonMode bool, e *cmdError) int {
	code := exitFor[e.name]
	if jsonMode {
		b, _ := json.Marshal(struct {
			Error   string `json:"error"`
			Exit    int    `json:"exit"`
			Message string `json:"message"`
		}{e.name, code, e.msg})
		_, _ = fmt.Fprintln(a.errw, string(b))
		return code
	}
	_, _ = fmt.Fprintf(a.errw, "error: %s\n%s\n", e.name, e.msg)
	return code
}
