package main

import "fmt"

// notImplemented is the shared RunE body for every subcommand this packet
// introduces as an empty shell (run, serve, open, hide, unhide, wip,
// feedback, show, status, doctor, heartbeat, heartbeat-item, ledger).
// import-pg-pr-annotations started as one of these stubs too; packet 9 of
// this docket (pg2-2j5ac.32) replaced its RunE with a real implementation
// (see import_pg_pr_annotations.go). A later packet replaces each
// remaining one's RunE the same way; until then the stub errors rather
// than silently succeeding, so a caller can tell "not built yet" from "ran
// and did nothing."
//
// Mirrors this repo's existing ErrNotImplemented convention (e.g.
// packages/pg-pr/pkg/beads/types.go, packages/pg-pr/pkg/provider/cicd/iface.go,
// packages/pg-connector/cmd/pg-connector-pr-github/internal/vcs/iface.go):
// "<name>: not implemented in this phase".
func notImplemented(name string) error {
	return fmt.Errorf("%s: not implemented in this phase", name)
}
