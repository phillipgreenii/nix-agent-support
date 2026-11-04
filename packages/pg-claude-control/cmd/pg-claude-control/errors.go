package main

import (
	"encoding/json"
	"fmt"
)

// Exit codes: this table is the control command's own; the recorder command's differ.
const (
	exitOK           = 0
	exitInternal     = 1 // reserved for unexpected errors
	exitUsage        = 2
	exitNoProcess    = 3
	exitNoControlMod = 4
	exitNotAccepting = 5
	exitRefused      = 6
	exitAmbiguous    = 7
	exitNoReply      = 8
	exitStayed       = 9
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
