// pg-connector-issue-beads is the issue capability's beads Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity
// [design: §5 preamble]. It implements pkg/provider/issue.Provider against
// this workspace's own bd tracker by shelling out to the `bd` CLI
// (internal), the first Tier-2 Issue backend built [design: §2, §5.1].
//
// This binary builds its own op-dispatch table (pkg/provider/issue's
// show/create/comment/transition entries) and hands it to the Tier-1 core's
// generic serve-loop entry point — it is the binary that actually calls
// ServeLoop, unlike either sibling packet [design: §4.2]. It does NOT add
// an auth_status entry: internal.Backend does not implement
// pkg/provider.AuthChecker (see backend.go's doc comment for why), so this
// table never gains a scriptout.OpAuthStatus key. capabilities.ops (see
// newDispatchTable's scriptout.AddCapabilities call) is computed straight
// from this table's own registered keys, so it automatically never claims
// scriptout.OpAuthStatus either, rather than this binary claiming an op it
// would actually answer with unknown_op.
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring pg-connector-pr-github's own cmd/pg-connector-pr-github
// convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (a bd-CLI-backed Backend) and its
// op-dispatch table, then hands the table to the Tier-1 core's generic
// serve loop.
func run() int {
	backend := internal.New(internal.NewCLIRunner())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the issue capability's table (show/create/
// comment/transition) via the sibling "generic issue entity/capability"
// packet's NewDispatchTable, then adds this backend's own capabilities
// entry via scriptout.AddCapabilities — the concrete backing for that
// sibling packet's vocabulary.state check, which cites this backend's
// capabilities response but does not itself populate it [design: §4.3,
// §4.3 AC]. AddCapabilities computes capabilities.ops straight from this
// table's own registered op names, so this backend never hand-types a
// second, separately maintained ops list that could drift from what the
// table actually dispatches (bead pg2-fh2vh).
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := issue.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.IssueSchemaVersion, capabilitiesBase(backend))
}

// capabilitiesBase declares this backend's schemaVersions and its
// non-empty state vocabulary (bd's actual accepted --status values)
// [design: §4.3, §4.3 AC] — everything AddCapabilities needs except Ops,
// which it deliberately leaves unset for AddCapabilities to compute. It
// also advertises the resolved bd workspace directory (bead pg2-1q9c0,
// AC2) when one is configured, so `pg-connector config validate`'s
// capabilities fan-out can surface which tracker each issue-beads instance
// targets without needing to run a real op first. capabilities must always
// answer regardless of workspace configuration (Backend.Workspace's error
// is deliberately swallowed here, not surfaced as a capabilities failure)
// — an unconfigured workspace is a Show/Create/Comment/Transition-time
// error, not a health-check failure.
func capabilitiesBase(backend *internal.Backend) scriptout.CapabilitiesResponse {
	vocabulary := map[string]any{
		"state": internal.Vocabulary,
	}
	if dir, err := backend.Workspace(); err == nil && dir != "" {
		vocabulary["workspace_dir"] = dir
	}
	return scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"issue": schema.IssueSchemaVersion},
		Vocabulary:      vocabulary,
	}
}
