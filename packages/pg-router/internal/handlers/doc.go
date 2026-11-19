// Package handlers is the root of the folded pg-router-handlers member
// (packages/pg-router-handlers keeps only its default.nix, README and behavior
// docs): the ccpool-backed and command-backed participant implementations
// realizing INTF-HANDLER/INTF-SOURCE (`phillipgreenii-nix-agent-support` ADR
// 0065's "New module" decision). Its sources are cmd/pg-router-handler-ccpool,
// cmd/pg-router-handler-command and this subtree.
//
// The import-boundary rule (boundary_test.go, members.json) forbids this
// subtree from importing a router engine package or another member; the router
// engine MUST NOT import this subtree. The blank import below keeps the one-way
// dependency on the router's wire schemas explicit.
//
// The member's coverage bars live in coverage-thresholds.txt here and are
// enforced by the `pg-router-ccpool-handler-go-tests` flake check.
package handlers

import (
	_ "github.com/phillipgreenii/pg-router/schemas"
)
