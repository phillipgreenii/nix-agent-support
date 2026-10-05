// exec.go: the one-shot exec of pg-desk and the classification of its exit
// code into this adapter's own 0/1 scheme.
//
// pg-desk is execed as a subprocess (the binary on ambient $PATH, never a Go
// import) with stdout and stderr captured separately, so the failure path
// can print nothing on this adapter's stdout and copy pg-desk's stderr.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// pgDeskBinary is the ambient $PATH name this adapter execs.
const pgDeskBinary = "pg-desk"

// execCmdFactory constructs the *exec.Cmd used to invoke pg-desk. Tests swap
// it to spawn a reentrant test-helper process.
var execCmdFactory = exec.CommandContext

type pgDeskResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// runPgDesk execs pg-desk with args. The returned error is non-nil only
// when the child could never be started (binary not on $PATH, permission
// denied, ...): there is then no exit code and no child stderr.
func runPgDesk(ctx context.Context, args []string) (pgDeskResult, error) {
	cmd := execCmdFactory(ctx, pgDeskBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	res := pgDeskResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if runErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.exitCode = exitErr.ExitCode()
		return res, nil
	}
	return pgDeskResult{}, fmt.Errorf("%s: cannot run: %w", pgDeskBinary, runErr)
}

// classifyExit translates pg-desk's `changes` exit codes into what
// pg-router's command-query runner expects of a source (it discards a
// source's WHOLE output on any non-zero exit): 0 (all sources ok) and 2
// (partial: records written, cursor advanced) -> ok, so every record is
// emitted with the degraded detail folded into item metadata; 3 (total
// failure: nothing logged, cursor not advanced) and anything else
// -> not ok. Mirroring 2 would make pg-router drop records pg-desk has
// already advanced past.
func classifyExit(exitCode int) bool {
	return exitCode == 0 || exitCode == 2
}
