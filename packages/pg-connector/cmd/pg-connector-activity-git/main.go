// pg-connector-activity-git is a capability-only Tier-2 backend: a thin,
// standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity.
// It reads the operator's git commits straight from local clones and
// implements ONLY the activity capability's list_activity op. It is
// registered only under the top-level activity.sources key, never under a
// connector.<type> key, and implements no pkg/provider.AuthChecker, so its
// dispatch table carries no auth_status entry.
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-activity-git/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's versionPath).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend and hands its dispatch table to the Tier-1 core's
// generic serve loop.
func run() int {
	backend := internal.New(internal.Options{})
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the activity capability's table (list_activity
// only) and adds this backend's capabilities entry. Ops is left unset: it is
// computed from the table, so the absence of auth_status is reflected
// automatically.
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := activity.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.ActivitySchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"activity": schema.ActivitySchemaVersion},
		Vocabulary: map[string]any{
			"activity_kinds": internal.ActivityKinds,
		},
		Version: Version,
	})
}
