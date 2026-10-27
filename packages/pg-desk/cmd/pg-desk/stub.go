package main

import "fmt"

// notImplemented is the shared RunE body for every subcommand this packet
// introduces as an empty shell (run, serve, open, hide, unhide, wip,
// feedback, show, status, doctor, heartbeat, heartbeat-item, ledger).
// A later packet replaces each remaining one's RunE with a real
// implementation; until then the stub errors rather
// than silently succeeding, so a caller can tell "not built yet" from "ran
// and did nothing."
//
// Mirrors this repo's existing ErrNotImplemented convention (e.g.
// The retired PR tool's pkg/beads/types.go, the retired PR tool's pkg/provider/cicd/iface.go,
// packages/pg-connector/cmd/pg-connector-pr-github/internal/vcs/iface.go):
// "<name>: not implemented in this phase".
func notImplemented(name string) error {
	return fmt.Errorf("%s: not implemented in this phase", name)
}
