// pg-connector-alert-grafana is the alert capability's Grafana Tier-2
// backend (bead pg2-rejc3): a thin, standalone executable speaking only the
// scriptout wire protocol (pkg/scriptout.ServeLoop), with no independent
// human-facing CLI identity. It implements pkg/provider/alert.Provider and
// pkg/provider/attention.Provider by reading a local Grafana's Alertmanager
// v2 alerts API (see internal/client.go), merging both dispatch tables in
// one binary (INV-ALERT-7).
//
// It implements no pkg/provider.AuthChecker (Grafana needs no auth), so the
// table carries no auth_status entry and `auth status` reports "disabled: not
// applicable" through the wire-level unknown_op sentinel.
//
// list_history (bead pg2-rwuhs, internal/history.go) enumerates Grafana's
// rules and reads each rule's state history; the parsing of its text field
// lives in the backend. The merged table therefore advertises list, show,
// list_history and list_attention (and no auth_status).
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-alert-grafana/internal"
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
	return scriptout.ServeLoop(newDispatchTable(internal.New(internal.NewHTTPClient())))
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
