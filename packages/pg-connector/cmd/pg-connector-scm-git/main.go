// pg-connector-scm-git is the scm capability's local-git Tier-2 backend: a
// thin, standalone, single-instance executable speaking only the
// scriptout wire protocol (pkg/scriptout.ServeLoop), with no independent
// human-facing CLI identity [design: §5 preamble]. It implements
// pkg/provider/scm.Provider (this backend's own internal.Provider) against
// real local git plumbing — worktrees and cwd->branch resolution, no
// remote sync concept [design: §4.7].
//
// It implements no pkg/provider.AuthChecker: local git has no remote
// credentials concept at all [design: §4.6, §4.7]. This binary's
// dispatch table therefore carries no auth_status entry (see
// pkg/provider/scm.NewDispatchTable), which pg-connector's own generic
// `auth status` fan-out (cmd/pg-connector/auth.go) already recognizes via
// the wire-level unknown_op sentinel and reports as "disabled: not
// applicable" — no special-casing needed in this binary.
//
// This binary builds its own op-dispatch table (pkg/provider/scm's
// worktree_add/worktree_remove/worktree_list/branch_detect entries, plus
// its own capabilities entry) and hands it to the Tier-1 core's generic
// serve-loop entry point — it is the binary that actually calls
// ServeLoop, unlike either sibling packet [design: §4.2].
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-scm-git/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/scm"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring pg-connector-pr-github/main.go's own convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (real-git-backed) and its
// op-dispatch table, then hands the table to the Tier-1 core's generic
// serve loop.
func run() int {
	backend := internal.New(internal.NewExecRunner())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the scm capability's table (worktree_add/
// worktree_remove/worktree_list/branch_detect — no auth_status, since
// backend implements no AuthChecker) via the sibling "generic scm
// entity/capability" packet's NewDispatchTable, then adds this backend's
// own capabilities entry via scriptout.AddCapabilities. AddCapabilities
// computes capabilities.ops straight from this table's own registered op
// names (so the deliberate absence of auth_status above is automatically
// reflected, not separately restated) — this backend never hand-types a
// second, separately maintained ops list that could drift from what the
// table actually dispatches (bead pg2-fh2vh).
func newDispatchTable(backend *internal.Provider) scriptout.DispatchTable {
	table := scm.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.ScmSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"scm": schema.ScmSchemaVersion},
	})
}
