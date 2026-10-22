// Package fail is pg-wrap's one failure shape: every failure pg-wrap detects
// exits before the payload with Code and a pg-wrap[<stage>]: message.
package fail

import (
	"fmt"
	"strconv"
)

// Code is the fixed exit status of every failure pg-wrap detects. It is none of
// 1, 9, 124 or 125-127, so a caller can tell it from a payload's status and from
// common launcher codes.
const Code = 97

// Stage names where in a run a failure happened.
const (
	StageSpec = "spec"
	StageEnv  = "env"
	StageExec = "exec"
)

// Layer returns the stage for layer i (0-based, outermost first).
func Layer(i int) string { return "layer " + strconv.Itoa(i) }

// Error is a failure pg-wrap detected, rendered as pg-wrap[<stage>]: <reason>.
type Error struct {
	Stage  string
	Reason string
}

func (e *Error) Error() string { return "pg-wrap[" + e.Stage + "]: " + e.Reason }

// New builds an Error for stage with a formatted reason.
func New(stage, format string, args ...any) *Error {
	return &Error{Stage: stage, Reason: fmt.Sprintf(format, args...)}
}
