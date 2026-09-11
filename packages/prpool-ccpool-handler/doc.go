// Package prpoolccpoolhandler is the module root for prpool-ccpool-handler,
// pr-pool's sibling module that realizes INTF-HANDLER/INTF-SOURCE for the
// ccpool-backed and command-backed participant kinds
// (docs/behavior/README.md; `phillipgreenii-nix-agent-support` ADR 0065's
// "New module" decision).
//
// This is a scaffold: no participant code has moved in yet (Task 5.2 onward
// per ADR 0065's "Move list"; see docs/behavior/README.md's Realization
// gaps). The blank import below is the only code here today, and it exists
// solely to make this module's one-way dependency on packages/pr-pool's wire
// schemas a real, `go mod tidy`-surviving dependency rather than a
// go.mod-only declaration — packages/pr-pool MUST NOT import this module
// back (docs/behavior/invariants.md's INV-CCH-1).
package prpoolccpoolhandler

import (
	_ "github.com/phillipgreenii/pr-pool/schemas"
)
