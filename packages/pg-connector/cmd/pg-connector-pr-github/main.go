// pg-connector-pr-github is the pr capability's GitHub Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity
// (actors.md's ACTOR-BACKEND). It implements pkg/provider/pr.Provider against
// GitHub by carrying over pg-pr's existing GitHub logic unchanged
// (internal/github) and adds its own AuthChecker via GitHub's existing
// env-then-gh auth token credential chain (INV-AUTH-1). It keeps no
// backend-local store (statelessness, D3) — every op is a live GitHub
// read, with no categorize/feedback_set write path (retired by bead
// pg2-2j5ac.28.7: category/disposition are re-derived by pg-desk's
// interpreter rather than persisted here).
//
// This binary builds its own op-dispatch table (pkg/provider/pr's
// show/list/files/commits/auth_status entries, plus its own capabilities
// entry) and hands it to the Tier-1 core's generic serve-loop entry point
// — it is the binary that actually calls ServeLoop, unlike either sibling
// packet (INV-WIRE-1).
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring pg-connector/default.nix's own cmd/pg-connector convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (ported GitHub logic, no local
// store) and its op-dispatch table, then hands the table to the Tier-1
// core's generic serve loop.
func run() int {
	backend := internal.New(github.New())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the pr capability's table (show/list/files/
// commits, plus auth_status via backend's AuthChecker) via the sibling
// "generic pr entity/capability" packet's NewDispatchTable, then merges in
// the search capability's table (search, plus its own auth_status entry —
// functionally identical to pr's, since both type-assert the same backend;
// harmless to overwrite) built by pkg/provider/search.NewDispatchTable
// (bead pg2-8hcnx: this backend's SearchPRs already existed but was never
// wired to the cross-capability "search" op), then adds this backend's own
// capabilities entry via scriptout.AddCapabilities. AddCapabilities
// computes capabilities.ops straight from this table's own registered op
// names, so this backend never hand-types a second, separately maintained
// ops list that could drift from what the table actually dispatches (bead
// pg2-fh2vh). Version is set to this binary's own ldflags-stamped build
// var, so capabilities is now the wire exposure for the version this
// binary otherwise had no way to report (bead pg2-a8uf2). This backend
// declares no vocabulary (its category vocabulary was retired with
// categorize by bead pg2-2j5ac.28.7).
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := pr.NewDispatchTable(backend)
	for op, handler := range search.NewDispatchTable(backend) {
		table[op] = handler
	}
	// attention (list_attention, plus its own auth_status entry —
	// functionally identical to pr's/search's, since all three
	// type-assert the same backend; harmless to overwrite) built by
	// pkg/provider/attention.NewDispatchTable (bead pg2-7wqkr: this
	// backend's own ListAttention porting the mine-vs-team NeedsAttention
	// CONCEPT from packages/pg-pr/internal/snapshot/attention.go).
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.PRSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"pr":        schema.PRSchemaVersion,
			"search":    schema.SearchSchemaVersion,
			"attention": schema.AttentionSchemaVersion,
		},
		Version: Version,
	})
}
