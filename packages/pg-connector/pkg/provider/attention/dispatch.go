// dispatch.go: builds the attention capability's own op-dispatch table,
// bound to a concrete Provider. The Tier-1 core's generic serve-loop entry
// point (pkg/scriptout.ServeLoop) is capability-agnostic; a future Tier-2
// backend or standalone plugin implementing attention.Source calls
// NewDispatchTable and hands the result to ServeLoop — this package builds
// no binary of its own (INV-WIRE-1), mirroring pkg/provider/pr.
// NewDispatchTable's/pkg/provider/ci.NewDispatchTable's exact structure and
// error-passthrough convention.
package attention

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the attention capability's op-dispatch table for
// p: list_attention always; auth_status only when p also implements
// pkg/provider.AuthChecker, asserted via a type-check rather than folded
// into the Provider interface (INV-AUTH-1). The list_attention handler
// passes p's returned error straight through unwrapped — a well-behaved
// Provider implementation is responsible for wrapping its own errors with
// the matching pkg/scriptout.Err* sentinel (e.g. ErrUnavailable); this
// table does no sentinel translation of its own.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"list_attention": {
			SchemaVersion: schema.AttentionSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				return p.ListAttention(ctx)
			},
		},
	}

	// A provider not implementing AuthChecker is not treated as a
	// forced/meaningless answer: the auth_status entry is simply omitted,
	// which pg-connector's own fan-out (cmd/pg-connector/auth.go) already
	// recognizes generically via the wire-level unknown_op sentinel and
	// reports as "disabled: not applicable" (INV-AUTH-1).
	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.AttentionSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				// auth_status always answers with a well-formed result
				// (never a wire-level error) — the AuthStatus.State field
				// itself carries success/failure, matching pg-connector's
				// existing fan-out convention (cmd/pg-connector/auth.go).
				if err := ac.CheckAuth(ctx); err != nil {
					return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: err.Error()}, nil
				}
				return scriptout.AuthStatus{State: scriptout.AuthOK}, nil
			},
		}
	}

	return table
}
