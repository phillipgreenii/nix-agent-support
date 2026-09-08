// Package attention declares the attention capability's provider
// interface — a small, capability-scoped Go interface (never named after a
// backend/system) (INV-CAP-1) that a Tier-2 backend or standalone plugin's
// concrete provider implements. It matches this repo's existing
// small-per-capability-interface convention (e.g. pr.Provider in
// packages/pg-connector/pkg/provider/pr) rather than one interface
// spanning multiple systems (INV-CAP-1).
//
// The design's own prose names this capability's interface "attention.
// Source" (as a concept), but this repo's OWN established convention for
// every other capability names its provider interface Provider inside a
// package named after the capability (pr.Provider, ci.Provider,
// issue.Provider, scm.Provider — never e.g. pr.Source). This package
// resolves that in favor of the repo's own established convention:
// package attention, interface Provider — matching every sibling
// capability and what naming_convention_test.go's own capabilityPackages
// list already anticipates. "attention.Source" remains an informal way to
// refer to this capability in prose/comments where useful, but the
// exported Go symbol is Provider.
//
// This package must never import a future daily-focus/df-survey package
// or type — enforced by cmd/pg-connector's own dependency-direction check
// (evaluateAttentionZeroImport in dependency_direction_test.go).
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package attention

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the attention capability's provider interface: a single,
// stateless read op. Attention is deliberately stateless — this interface
// MUST NOT gain any acknowledge/hide/unhide method: resolving the signal
// means doing the underlying thing; how a source clears itself is
// source-specific and happens out-of-band. A concrete backend MAY
// additionally implement pkg/provider.AuthChecker, asserted via a
// type-check rather than folded into this interface (INV-AUTH-1) — see
// NewDispatchTable in dispatch.go.
type Provider interface {
	// ListAttention returns everything that currently qualifies for
	// attention from this source — no id argument, no snapshot memory, no
	// cross-awareness of any consuming ritual.
	ListAttention(ctx context.Context) ([]schema.AttentionItem, error)
}
