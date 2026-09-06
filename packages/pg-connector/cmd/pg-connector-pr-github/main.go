// pg-connector-pr-github is the pr capability's GitHub Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity
// [design: §5 preamble]. It implements pkg/provider/pr.Provider against
// GitHub by carrying over pg-pr's existing GitHub logic unchanged
// (internal/github) and adds its own AuthChecker via GitHub's existing
// env-then-gh auth token credential chain [design: §5.1, §4.6, §9].
//
// This binary builds its own op-dispatch table (pkg/provider/pr's
// show/categorize/feedback_set/auth_status entries, plus its own
// capabilities entry) and hands it to the Tier-1 core's generic serve-loop
// entry point — it is the binary that actually calls ServeLoop, unlike
// either sibling packet [design: §4.2].
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring pg-connector/default.nix's own cmd/pg-connector convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (ported GitHub logic + a fresh local
// store) and its op-dispatch table, then hands the table to the Tier-1
// core's generic serve loop.
func run() int {
	backend := internal.New(github.New(), internal.NewStore(internal.DefaultStorePath()))
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the pr capability's table (show/categorize/
// feedback_set, plus auth_status via backend's AuthChecker) via the
// sibling "generic pr entity/capability" packet's NewDispatchTable, then
// adds this backend's own capabilities entry via scriptout.AddCapabilities
// — the concrete backing for that sibling packet's vocabulary check, which
// cites this backend's capabilities response but does not itself populate
// it [design: §4.3, §6.1]. AddCapabilities computes capabilities.ops
// straight from this table's own registered op names, so this backend
// never hand-types a second, separately maintained ops list that could
// drift from what the table actually dispatches (bead pg2-fh2vh).
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := pr.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.SchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"pr": schema.SchemaVersion},
		Vocabulary: map[string]any{
			"category": internal.Vocabulary,
		},
	})
}
