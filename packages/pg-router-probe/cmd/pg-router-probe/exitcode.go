// exitcode.go: the exit-code carrier this binary's own RunE bodies use to
// signal one of the three non-zero, non-generic exit codes the design's
// own "Exit codes" paragraph specifies (2 usage error, 3 total failure, 4
// partial) — see main.go's run() for how it is unwrapped. 0 is always a
// nil error; there is no exitCodeError for it.
package main

import "fmt"

// exitCodeError carries an explicit process exit code plus an optional
// message already meant for stderr (main.go's run() prints it verbatim,
// once, rather than cobra's own default error formatting).
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string {
	if e.msg == "" {
		return "pg-router-probe: exit"
	}
	return e.msg
}

func usageErrorf(format string, args ...any) error {
	return &exitCodeError{code: 2, msg: fmt.Sprintf(format, args...)}
}

func totalFailureErrorf(format string, args ...any) error {
	return &exitCodeError{code: 3, msg: fmt.Sprintf(format, args...)}
}

func partialErrorf(format string, args ...any) error {
	return &exitCodeError{code: 4, msg: fmt.Sprintf(format, args...)}
}
