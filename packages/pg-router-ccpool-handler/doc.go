// Package pgrouterccpoolhandler is the module root for pg-router-ccpool-handler,
// pg-router's sibling module that realizes INTF-HANDLER/INTF-SOURCE for the
// ccpool-backed and command-backed participant kinds
// (docs/behavior/README.md; `phillipgreenii-nix-agent-support` ADR 0065's
// "New module" decision).
//
// The module was scaffolded empty (Task 5.1); Task 5.2/5.3 (folded together
// per the operator's 2026-09-11 decision recorded on docket pg2-oju6w's Task
// 5.2 packet) then moved every participant implementation in and gave it a
// real wire-facing CLI entrypoint (cmd/pg-router-ccpool-handler). The blank
// import below predates that and still exists solely to make this module's
// one-way dependency on packages/pg-router's wire schemas a real,
// `go mod tidy`-surviving dependency rather than a go.mod-only declaration —
// packages/pg-router MUST NOT import this module back (docs/behavior/
// invariants.md's INV-CCH-1).
package pgrouterccpoolhandler

import (
	_ "github.com/phillipgreenii/pg-router/schemas"
)
