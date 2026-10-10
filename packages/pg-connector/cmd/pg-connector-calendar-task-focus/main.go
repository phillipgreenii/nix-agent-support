// pg-connector-calendar-task-focus is the calendar and attention capabilities'
// Tier-2 backend for the pg-task-focus daemon (bead pg2-t7me1.4): a thin,
// standalone executable speaking only the scriptout wire protocol
// (pkg/scriptout.ServeLoop), with no independent human-facing CLI identity.
// It implements pkg/provider/calendar.Provider and pkg/provider/attention.
// Provider by reading the daemon's loopback HTTP API (see internal/client.go),
// merging both dispatch tables in one binary, the precedent being
// pg-connector-calendar-osx-bridge and pg-connector-alert-grafana. It is
// registered under connector.calendar and, independently, under
// attention.sources.
//
// It is launched once per call (see internal/backend.go's package doc comment
// for the reasoning) and writes a start row and a final row per call to its own
// event log (internal/eventlog), so a call the umbrella kills at its deadline
// is still attributable.
//
// It implements no pkg/provider.AuthChecker (the daemon is loopback only and
// takes no credential), so the table carries no auth_status entry and `auth
// status` reports "disabled: not applicable" through the wire-level unknown_op
// sentinel. The merged table advertises list, list_events and list_attention.
package main

import (
	"os"
	"time"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-calendar-task-focus/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-calendar-task-focus/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/calendar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath, mirroring
// every existing Tier-2 backend's own convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (an HTTP-client-backed Backend) and its
// op-dispatch table, then hands the table to the Tier-1 core's generic serve
// loop.
func run() int {
	backend := internal.New(internal.NewHTTPClient(os.Getenv("TRACEPARENT")))
	return scriptout.ServeLoop(instrument(newDispatchTable(backend), os.Getenv))
}

// instrument wraps table so every call writes a start row and a final row to
// this backend's own rotating event log (package eventlog explains the
// ownership contract). It lives apart from newDispatchTable so the wiring tests
// that drive newDispatchTable never write to the real state home. With the log
// disabled or unresolvable it returns table unchanged.
func instrument(table scriptout.DispatchTable, getenv func(string) string) scriptout.DispatchTable {
	return eventlog.Instrument(table, eventlog.SinkFromEnv(getenv), Version, time.Now)
}

// newDispatchTable builds the calendar capability's table (list/list_events, no
// auth_status), merges in the attention capability's table (list_attention),
// then adds this backend's own capabilities entry via scriptout.AddCapabilities,
// which computes capabilities.ops straight from the final table's own op names
// so this backend never hand-types a second, separately maintained ops list.
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := calendar.NewDispatchTable(backend)
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.CalendarSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"calendar":  schema.CalendarSchemaVersion,
			"attention": schema.AttentionSchemaVersion,
		},
		Version: Version,
	})
}
