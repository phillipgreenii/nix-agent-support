// pg-connector-mail-osx-bridge is the mail capability's first Tier-2
// backend: a thin, standalone executable speaking only the scriptout wire
// protocol (pkg/scriptout.ServeLoop), with no independent human-facing CLI
// identity — mirroring cmd/pg-connector-calendar-osx-bridge/main.go's
// identical structure. It implements pkg/provider/mail.Provider by talking
// ONLY to pg-osx-bridge-api's local Unix-domain socket for the mail service
// (see internal/client.go) — no direct osascript or Mail.app scripting, and
// no os/exec, anywhere in this package. There is NO delete operation: the
// dispatch table registers none (INV-MAIL-1).
//
// It implements no pkg/provider.AuthChecker: pg-osx-bridge-api requires no
// credential from its own client at all, so this binary's dispatch table
// carries no auth_status entry, which pg-connector's own generic `auth
// status` fan-out already recognizes via the wire-level unknown_op sentinel
// and reports as "disabled: not applicable."
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-mail-osx-bridge/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/mail"
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
// its op-dispatch table, then hands the table to the Tier-1 core's generic
// serve loop.
func run() int {
	backend := internal.New(internal.NewSocketClient())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

// newDispatchTable builds the mail capability's table (list, show,
// search_messages, mark_read, mark_unread, archive, unarchive,
// fetch_attachment; no auth_status — see this file's package doc comment),
// then merges in the search and attention capabilities' tables (the
// search capability's own op is "search", distinct from the mail
// capability's "search_messages", so the merge has no collision), then adds
// this backend's own capabilities entry via scriptout.AddCapabilities —
// AddCapabilities computes capabilities.ops straight from this table's own
// registered op names, so this backend never hand-types a second,
// separately maintained ops list.
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := mail.NewDispatchTable(backend)
	for op, handler := range search.NewDispatchTable(backend.SearchProvider()) {
		table[op] = handler
	}
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.MailSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"mail":      schema.MailSchemaVersion,
			"search":    schema.SearchSchemaVersion,
			"attention": schema.AttentionSchemaVersion,
		},
		Version: Version,
	})
}
