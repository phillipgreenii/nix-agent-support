package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const progName = "pg-connector-issue-beads"

// errUsage wraps every argument-parse failure so run can map it to exit
// code 2 without confusing it with flag.ErrHelp (exit 0).
var errUsage = errors.New("usage")

// parseArgs parses the command's own arguments (os.Args[1:]): the registry
// command of an instance is `pg-connector-issue-beads --beads-dir DIR`, and
// the umbrella passes the command's arguments first and the JSON request on
// stdin (bead pg2-91y12, ADR 0062 amendment). Only --beads-dir DIR or
// --beads-dir=DIR is recognized; it returns the dir ("" when the flag is
// absent). The value must be an absolute path: unlike the env vars (left
// unvalidated for compatibility) a flag is new surface, and a relative path
// would resolve against whatever cwd the umbrella was launched from, the
// defect ErrWorkspaceNotConfigured exists to prevent. Giving the flag
// twice is not an error: the last one wins.
func parseArgs(args []string, stderr io.Writer) (beadsDir string, err error) {
	fs := flag.NewFlagSet(progName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [--beads-dir DIR]\n", progName)
	}
	var dir string
	given := false
	fs.Func("beads-dir", "absolute path of the bd tracker this instance serves (beats $PG_CONNECTOR_ISSUE_BEADS_DIR and $BEADS_DIR)", func(v string) error {
		given = true
		dir = v
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("%w: %s: unexpected argument %q; the only option is --beads-dir DIR", errUsage, progName, fs.Arg(0))
	}
	if !given {
		return "", nil
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("%w: %s: --beads-dir must not be empty", errUsage, progName)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: %s: --beads-dir %q must be an absolute path (a relative path would resolve against the caller's cwd)", errUsage, progName, dir)
	}
	return dir, nil
}
