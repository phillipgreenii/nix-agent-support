package main

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/x/claudetrust"
	"github.com/phillipgreenii/x/exitcode"
)

const trustHelp = `pg-assistants trust: mark directories trusted in Claude's .claude.json.

usage: pg-assistants trust --claude-json PATH <cwd>...

An untrusted directory stops claude on its folder-trust dialog before any
hook runs, so an assistant started there never answers. This sets
.projects[<cwd>].hasTrustDialogAccepted for every <cwd>, keeping everything
else in the file, and writes nothing when all are trusted already. Each <cwd>
is written with its symlinks resolved, the key Claude looks up (one that does
not exist yet is written as given). The
home-manager module runs it for every assistant at activation; the daemon
also checks before each start.

Exit 0 when every directory is trusted, 1 printing the error otherwise, 2 on a
usage error. Every path MUST be absolute.
`

// trust is `pg-assistants trust --claude-json PATH <cwd>...`.
func (c *cli) trust(args []string) int {
	fs := c.newFlagSet("trust")
	path := fs.String("claude-json", "", "Claude's .claude.json (required, absolute)")
	cwds, code, done := c.parse(fs, args, trustHelp)
	if done {
		return code
	}
	const usage = "usage: pg-assistants trust --claude-json PATH <cwd>..."
	if *path == "" || len(cwds) == 0 {
		fmt.Fprintln(c.stderr, usage)
		return exitcode.Usage
	}
	for _, p := range append([]string{*path}, cwds...) {
		if !filepath.IsAbs(p) {
			fmt.Fprintf(c.stderr, "pg-assistants trust: %q is not an absolute path\n", p)
			return exitcode.Usage
		}
	}
	// Claude keys .projects by the resolved directory; one that does not
	// resolve (not created yet) keeps its path.
	keys := make([]string, len(cwds))
	for i, cwd := range cwds {
		keys[i] = config.ResolvePath(cwd)
	}
	if err := claudetrust.Ensure(*path, keys...); err != nil {
		fmt.Fprintf(c.stderr, "pg-assistants trust: %v\n", err)
		return exitcode.Error
	}
	return exitcode.OK
}

// trustFile is the daemon's supervisor.Trust over one .claude.json, through
// x/claudetrust.
//
// Ensure runs one at a time. claudetrust's read-merge-rename takes no lock,
// so two loops starting at once could each write the file from a read that
// lacks the other's directory, and one of them would lose its trust (and
// stall on the dialog). The mutex is held across that local file read and
// write: the one place the daemon holds a lock across I/O, accepted because
// it is a single small local file and is reached only before a start whose
// directory is not trusted yet. IsTrusted only reads, and the file is
// replaced by a rename, so it takes no lock.
type trustFile struct {
	path   string
	ensure func(path string, cwds ...string) error // claudetrust.Ensure; a test seam

	mu sync.Mutex // serialises ensure
}

func newTrustFile(path string) *trustFile {
	return &trustFile{path: path, ensure: claudetrust.Ensure}
}

// IsTrusted reports whether cwd is trusted in the file.
func (t *trustFile) IsTrusted(cwd string) (bool, error) {
	return claudetrust.IsTrusted(t.path, cwd)
}

// Ensure marks cwd trusted, one call at a time.
func (t *trustFile) Ensure(cwd string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ensure(t.path, cwd)
}
