// pg-connector-ci-github-actions is the ci capability's GitHub Actions
// Tier-2 backend: a thin, standalone executable speaking only the
// scriptout wire protocol (pkg/scriptout.ServeLoop), with no independent
// human-facing CLI identity [design: §5 preamble]. It implements
// pkg/provider/ci.Provider against GitHub Actions by carrying over
// packages/pg-pr/pkg/provider/cicd/ghactions's existing client
// (ListRuns/GetLogs/RerunFailed) unchanged in its underlying GitHub calls
// [contract: carry-over basis], and adds its own AuthChecker via GitHub's
// existing env-then-gh-auth-token credential chain [design: §5.1, §4.6].
//
// This binary builds its own op-dispatch table (pkg/provider/ci's
// list_runs/get_logs/rerun_failed/auth_status entries, plus its own
// capabilities entry) and hands it to the Tier-1 core's generic serve-loop
// entry point, mirroring pg-connector-pr-github/main.go's own convention
// [design: §4.2].
package main

import (
	"context"
	"encoding/json"
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/ci"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring pg-connector-pr-github/main.go's own convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (ported GitHub Actions logic, no
// local store) and its op-dispatch table, then hands the table to the
// Tier-1 core's generic serve loop.
func run() int {
	backend := internal.New()
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the ci capability's table (list_runs/get_logs/
// rerun_failed, plus auth_status via backend's AuthChecker) via the
// sibling "generic ci entity/capability" packet's ci.NewDispatchTable, then
// adds this backend's own capabilities entry [design: §4.3].
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := ci.NewDispatchTable(backend)
	table[scriptout.OpCapabilities] = scriptout.OpHandler{
		SchemaVersion: schema.CISchemaVersion,
		Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return capabilitiesResponse(), nil
		},
	}
	return table
}

// capabilitiesResponse declares this backend's schemaVersions and the ops
// it answers [design: §4.3].
func capabilitiesResponse() scriptout.CapabilitiesResponse {
	return scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"ci": schema.CISchemaVersion},
		Ops:             []string{"list_runs", "get_logs", "rerun_failed", scriptout.OpAuthStatus, scriptout.OpCapabilities},
	}
}
