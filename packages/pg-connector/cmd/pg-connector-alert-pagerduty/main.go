// pg-connector-alert-pagerduty is the alert capability's PagerDuty Tier-2
// backend (bead pg2-t79bc): a thin, standalone executable speaking only the
// scriptout wire protocol (pkg/scriptout.ServeLoop), with no independent
// human-facing CLI identity. It implements pkg/provider/alert.Provider,
// pkg/provider/attention.Provider and pkg/provider.AuthChecker by reading
// PagerDuty's REST API (see internal/client.go), merging both dispatch tables
// in one binary (INV-ALERT-7). The API key comes from the macOS Keychain with
// a PAGERDUTY_API_TOKEN fallback (internal/auth.go).
//
// The merged table advertises list, show, list_history (which answers
// unknown_op: history is not implemented for PagerDuty), list_attention and
// auth_status.
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-alert-pagerduty/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/alert"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath).
var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	return scriptout.ServeLoop(newDispatchTable(internal.New(internal.NewHTTPClient(), internal.NewTokenResolver(os.Getenv))))
}

// newDispatchTable builds the alert capability's table, merges in the
// attention capability's table, then adds the capabilities entry computed from
// the final table's own op names.
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := alert.NewDispatchTable(backend)
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.AlertSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"alert":     schema.AlertSchemaVersion,
			"attention": schema.AttentionSchemaVersion,
		},
		Version: Version,
	})
}
