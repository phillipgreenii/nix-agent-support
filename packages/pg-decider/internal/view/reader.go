package view

import (
	"context"
	"fmt"
	"strings"
)

// pgDeskBinary is the ambient $PATH name that is exec'd.
const pgDeskBinary = "pg-desk"

// Exec runs pg-desk. Tests swap it for an in-process double.
var Exec Runner = ExecRunner

// Read execs `pg-desk <typ> show <id> --json` and parses its stdout. Every
// failure (cannot start, non-zero exit, unparseable output, wrong contract)
// is an error whose text names pg-desk.
func Read(ctx context.Context, typ, id string) (*View, error) {
	res := Exec(ctx, Call{Name: pgDeskBinary, Args: []string{typ, "show", id, "--json"}})
	if res.StartErr != nil {
		return nil, fmt.Errorf("%s: cannot run: %w", pgDeskBinary, res.StartErr)
	}
	if res.Code != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = "no diagnostic on stderr"
		}
		return nil, fmt.Errorf("%s %s show %s exited %d: %s", pgDeskBinary, typ, id, res.Code, detail)
	}
	v, err := Parse([]byte(res.Stdout))
	if err != nil {
		return nil, fmt.Errorf("%s %s show %s: %w", pgDeskBinary, typ, id, err)
	}
	return v, nil
}
