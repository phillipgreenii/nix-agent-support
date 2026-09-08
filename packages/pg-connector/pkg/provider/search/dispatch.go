// dispatch.go: builds the search capability's own op-dispatch table, bound
// to a concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; a future Tier-2 backend
// or standalone plugin implementing search.Provider calls NewDispatchTable
// and hands the result to ServeLoop — this package builds no binary of its
// own (INV-WIRE-1), mirroring pkg/provider/pr.NewDispatchTable's/
// pkg/provider/ci.NewDispatchTable's exact structure and error-passthrough
// convention.
package search

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the search capability's op-dispatch table for p:
// search always; auth_status only when p also implements
// pkg/provider.AuthChecker, asserted via a type-check rather than folded
// into the Provider interface (INV-AUTH-1). The search handler decodes the
// wire args {"query": "...", "fields": [...]} and passes p's returned
// error straight through unwrapped — a well-behaved Provider implementation
// is responsible for wrapping its own errors with the matching
// pkg/scriptout.Err* sentinel; this table does no sentinel translation of
// its own, and does no validation of the fields argument beyond decoding it
// (that responsibility sits entirely at the aggregation layer built by the
// sibling "search aggregation + CLI verb" packet).
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"search": {
			SchemaVersion: schema.SearchSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Query  string   `json:"query"`
					Fields []string `json:"fields"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode search args: "+err.Error())
				}
				return p.Search(ctx, a.Query, a.Fields)
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
			SchemaVersion: schema.SearchSchemaVersion,
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
