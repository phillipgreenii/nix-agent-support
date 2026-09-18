// pg-connector-thread-slack is the thread capability's Tier-2 backend: a
// thin, standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI
// identity — mirroring cmd/pg-connector-scm-git/main.go's identical
// structure. It implements pkg/provider/thread.Provider (show/list only —
// see pkg/provider/thread/iface.go's own doc comment for why) against a
// real Slack MCP by exec'ing `claude -p` as pure transport (this bead's
// own Objective; see internal/runner.go and internal/backend.go for the
// exact invocation/reply-validation shape).
//
// It implements no pkg/provider.AuthChecker: it resolves no credential of
// its own at all — "no Slack token is provisioned" is this bead's own
// binding decision, and claude/the Slack MCP resolve their own ambient
// configuration. This binary's dispatch table therefore carries no
// auth_status entry (see pkg/provider/thread.NewDispatchTable), which
// pg-connector's own generic `auth status` fan-out
// (cmd/pg-connector/auth.go) already recognizes via the wire-level
// unknown_op sentinel and reports as "disabled: not applicable" — no
// special-casing needed in this binary, mirroring
// cmd/pg-connector-scm-git/main.go's identical precedent for local git.
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-thread-slack/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/thread"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring every existing Tier-2 backend's own convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (a claude-p-backed Backend) and its
// op-dispatch table, then hands the table to the Tier-1 core's generic
// serve loop.
func run() int {
	backend := internal.New(internal.NewCLIRunner())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the thread capability's table (show/list — no
// auth_status, since Backend implements no AuthChecker) via
// pkg/provider/thread.NewDispatchTable, then adds this backend's own
// capabilities entry via scriptout.AddCapabilities. AddCapabilities
// computes capabilities.ops straight from this table's own registered op
// names (so the deliberate absence of auth_status above is automatically
// reflected, not separately restated), so this backend never hand-types a
// second, separately maintained ops list that could drift from what the
// table actually dispatches (bead pg2-fh2vh's precedent). Version is set
// to this binary's own ldflags-stamped build var.
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := thread.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.ThreadSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"thread": schema.ThreadSchemaVersion},
		Version:         Version,
	})
}
