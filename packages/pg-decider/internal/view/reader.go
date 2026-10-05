package view

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// pgDeskBinary is the ambient $PATH name that is exec'd.
const pgDeskBinary = "pg-desk"

// ExecCommand constructs the *exec.Cmd used to invoke pg-desk. Tests swap it
// to spawn a reentrant test-helper process.
var ExecCommand = exec.CommandContext

// Read execs `pg-desk <typ> show <id> --json` and parses its stdout. Every
// failure (cannot start, non-zero exit, unparseable output, wrong contract)
// is an error whose text names pg-desk.
func Read(ctx context.Context, typ, id string) (*View, error) {
	cmd := ExecCommand(ctx, pgDeskBinary, typ, "show", id, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = "no diagnostic on stderr"
			}
			return nil, fmt.Errorf("%s %s show %s exited %d: %s", pgDeskBinary, typ, id, exitErr.ExitCode(), detail)
		}
		return nil, fmt.Errorf("%s: cannot run: %w", pgDeskBinary, err)
	}
	v, err := Parse(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s %s show %s: %w", pgDeskBinary, typ, id, err)
	}
	return v, nil
}
