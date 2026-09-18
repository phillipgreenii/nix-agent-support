// pg-connector-calendar-osx-bridge is the calendar capability's first
// Tier-2 backend: a thin, standalone executable speaking only the
// scriptout wire protocol (pkg/scriptout.ServeLoop), with no independent
// human-facing CLI identity — mirroring
// cmd/pg-connector-thread-slack/main.go's identical structure. It
// implements pkg/provider/calendar.Provider by talking ONLY to
// osx-bridge-api's local Unix-domain socket for the calendar service (see
// internal/client.go) — no direct EventKit/go-eventkit import anywhere in
// this package.
//
// It implements no pkg/provider.AuthChecker: osx-bridge-api requires no
// credential from its own client at all — TCC consent is the daemon's own
// concern, already handled [landed: pg2-p9ap3, pg2-tk57n] — mirroring
// pg-connector-thread-slack's identical "resolves no credential of its
// own at all" precedent. This binary's dispatch table therefore carries
// no auth_status entry, which pg-connector's own generic `auth status`
// fan-out (cmd/pg-connector/auth.go) already recognizes via the
// wire-level unknown_op sentinel and reports as "disabled: not
// applicable."
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-calendar-osx-bridge/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/calendar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Version is stamped at build time (mkGoApp's default versionPath,
// mirroring every existing Tier-2 backend's own convention).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds this backend's Provider (a socket-client-backed Backend) and
// its op-dispatch table, then hands the table to the Tier-1 core's
// generic serve loop.
func run() int {
	backend := internal.New(internal.NewSocketClient())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the calendar capability's table (list/
// list_events, no auth_status — see this file's package doc comment), then
// merges in the search and attention capabilities' tables (Backend
// additionally implements search.Provider/attention.Provider, asserted via
// type-check in internal/backend.go), then adds this backend's own
// capabilities entry via scriptout.AddCapabilities — AddCapabilities
// computes capabilities.ops straight from this table's own registered op
// names, so this backend never hand-types a second, separately maintained
// ops list [carry-over basis: cmd/pg-connector-issue-jira/main.go's
// identical newDispatchTable/capabilitiesBase merge pattern, bead
// pg2-fh2vh].
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := calendar.NewDispatchTable(backend)
	for op, handler := range search.NewDispatchTable(backend) {
		table[op] = handler
	}
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.CalendarSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"calendar":  schema.CalendarSchemaVersion,
			"search":    schema.SearchSchemaVersion,
			"attention": schema.AttentionSchemaVersion,
		},
		Version: Version,
	})
}
