package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Error is a failed pg-connector call: the verb's arguments, the exit code it
// ended with, and the wire error fields it printed (when stdout carried a
// well-formed error envelope).
type Error struct {
	// Args are the arguments after the binary name.
	Args []string
	// ExitCode is the process exit code, or -1 when the child never started,
	// was signaled, or ended with its context.
	ExitCode int
	// Code is the wire error.code (for example "not_found"), when printed.
	Code string
	// Reason is the wire error.reason, when printed. The field is provided
	// here; the reason constants and the wire field itself are produced
	// elsewhere, so it is empty until a producer writes one.
	Reason string
	// Message is the wire error.message, or the raw stdout text when stdout
	// carried no error envelope.
	Message string
	// Cause is the underlying failure (a start error or a context error), when
	// there is one.
	Cause error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pg-connector %s: exit %d", strings.Join(e.Args, " "), e.ExitCode)
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, " [%s]", e.Reason)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, ": %v", e.Cause)
	}
	return b.String()
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.Cause }

// IsNotFound reports whether err is a *Error with the NotFound exit code.
func IsNotFound(err error) bool { return HasExitCode(err, ExitNotFound) }

// HasExitCode reports whether err is (or wraps) an *Error ending with code.
func HasExitCode(err error, code int) bool {
	var ce *Error
	return errors.As(err, &ce) && ce.ExitCode == code
}

// wireErrorEnvelope is the part of a failed call's stdout envelope this
// package reads.
type wireErrorEnvelope struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	} `json:"error"`
}

// newExitError builds the *Error for a call that ended with a non-accepted
// exit code, reading error.code/message/reason from stdout when present.
func newExitError(args []string, code int, stdout []byte) *Error {
	e := &Error{Args: args, ExitCode: code}
	var env wireErrorEnvelope
	if json.Unmarshal(stdout, &env) == nil && env.Error != nil {
		e.Code, e.Message, e.Reason = env.Error.Code, env.Error.Message, env.Error.Reason
		return e
	}
	e.Message = strings.TrimSpace(string(stdout))
	return e
}

// NewExitError builds the *Error for a call that ended with exit code code and
// printed stdout, reading error.code, message and reason from a well-formed
// error envelope. It is for a caller that runs the process itself.
func NewExitError(args []string, code int, stdout []byte) *Error {
	return newExitError(args, code, stdout)
}
