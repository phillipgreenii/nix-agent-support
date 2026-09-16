package main

import "fmt"

// notImplemented is the shared RunE body for every subcommand this packet
// introduces as an empty shell (run, serve, open, hide, unhide, wip,
// feedback, show, status, doctor, heartbeat, heartbeat-item,
// import-pg-pr-annotations, ledger). A later packet of this docket
// (pg2-2j5ac.32) replaces each one's RunE with its real implementation;
// until then the stub errors rather than silently succeeding, so a caller
// can tell "not built yet" from "ran and did nothing."
//
// Mirrors this repo's existing ErrNotImplemented convention (e.g.
// packages/pg-pr/pkg/beads/types.go, packages/pg-pr/pkg/provider/cicd/iface.go,
// packages/pg-connector/cmd/pg-connector-pr-github/internal/vcs/iface.go):
// "<name>: not implemented in this phase".
func notImplemented(name string) error {
	return fmt.Errorf("%s: not implemented in this phase", name)
}
