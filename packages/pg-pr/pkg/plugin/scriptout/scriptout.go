// Package scriptout helps authors of external pg-pr providers expose
// their Go impl as a stdin/stdout JSON binary that pg-pr core exec's.
// Phase 0 stubs only; full protocol in Phase 1+.
package scriptout

import (
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ErrNotImplemented is returned by Phase 0 stubs.
var ErrNotImplemented = errors.New("scriptout: not implemented in this phase")

// ServeVCS wraps a vcs.Provider as a script-out subprocess.
// Phase 0 stub.
func ServeVCS(_ vcs.Provider) error { return ErrNotImplemented }

// ServeCICD wraps a cicd.Provider as a script-out subprocess.
// Phase 0 stub.
func ServeCICD(_ cicd.Provider) error { return ErrNotImplemented }

// ServeIssues wraps an issues.Provider as a script-out subprocess.
// Phase 0 stub.
func ServeIssues(_ issues.Provider) error { return ErrNotImplemented }
