// pg-connector-pr-github is the pr capability's GitHub Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity
// (actors.md's ACTOR-BACKEND). It implements pkg/provider/pr.Provider against
// GitHub by carrying over pg-pr's existing GitHub logic unchanged
// (internal/github) and adds its own AuthChecker via GitHub's existing
// env-then-gh auth token credential chain (INV-AUTH-1).
//
// This binary builds its own op-dispatch table (pkg/provider/pr's
// show/categorize/feedback_set/auth_status entries, plus its own
// capabilities entry) and hands it to the Tier-1 core's generic serve-loop
// entry point — it is the binary that actually calls ServeLoop, unlike
// either sibling packet (INV-WIRE-1).
package main

import (
	"fmt"
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
//
// DefaultStorePath now reports a missing/unresolvable $HOME rather than
// silently falling back to a cwd-relative store path (finding A18); a
// failure here means this backend cannot even locate its own store, so it
// fails loudly on stderr with a generic exit(1) before ever entering
// ServeLoop's wire protocol — there is no wire response to carry this in,
// since no request has been read yet.
func run() int {
	storePath, err := internal.DefaultStorePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg-connector-pr-github: %v\n", err)
		return 1
	}
	backend := internal.New(github.New(), internal.NewStore(storePath))
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the pr capability's table (show/categorize/
// feedback_set, plus auth_status via backend's AuthChecker) via the
// sibling "generic pr entity/capability" packet's NewDispatchTable, then
// adds this backend's own capabilities entry via scriptout.AddCapabilities
// — the concrete backing for that sibling packet's vocabulary check, which
// cites this backend's capabilities response but does not itself populate
// it (interfaces.md's vocabulary note). AddCapabilities computes capabilities.ops
// straight from this table's own registered op names, so this backend
// never hand-types a second, separately maintained ops list that could
// drift from what the table actually dispatches (bead pg2-fh2vh). Version
// is set to this binary's own ldflags-stamped build var, so capabilities
// is now the wire exposure for the version this binary otherwise had no
// way to report (bead pg2-a8uf2).
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := pr.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.PRSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"pr": schema.PRSchemaVersion},
		Vocabulary: map[string]any{
			"category": internal.Vocabulary,
		},
		Version: Version,
	})
}
