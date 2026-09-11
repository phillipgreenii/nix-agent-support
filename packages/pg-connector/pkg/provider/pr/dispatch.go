// dispatch.go: builds the pr capability's own op-dispatch table, bound to a
// concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; the sibling
// "pg-connector-pr-github" backend's own main() calls NewDispatchTable and
// hands the result to ServeLoop — this package builds no binary of its own
// (INV-WIRE-1).
package pr

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the pr capability's op-dispatch table for p:
// show, list, files, and commits always; auth_status only when p also
// implements pkg/provider.AuthChecker, asserted via a type-check rather
// than folded into the Provider interface (INV-AUTH-1). Every handler
// passes p's returned error straight through unwrapped — a well-behaved
// Provider implementation (built by the Tier-2 backend packet) is
// responsible for wrapping its own errors with the matching
// pkg/scriptout.Err* sentinel (e.g. ErrNotFound); this table does no
// sentinel translation of its own.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"show": {
			SchemaVersion: schema.PRSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID string `json:"id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode show args: "+err.Error())
				}
				return p.Show(ctx, a.ID)
			},
		},
		// list resolves args.query against the request's own config.queries
		// block (via scriptout.ConfigFromContext) CENTRALLY, here, rather
		// than inside every backend's own p.List — so query_not_recognized
		// is reported identically by every pr backend, with
		// no duplicated resolution logic across them [freedom boundary].
		// cursor is decoded but deliberately unused/unvalidated: design's
		// own binding decision is that cursor "MUST always be null in this
		// packet" — a caller sending a non-null cursor is simply ignored
		// rather than rejected, since rejecting it would require this
		// packet to invent its own validation error for a field phase 8
		// (changes) actually owns.
		"list": {
			SchemaVersion: schema.PRSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Query   string  `json:"query"`
					Cursor  *string `json:"cursor"`
					IDsOnly bool    `json:"ids_only"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list args: "+err.Error())
				}
				expr, ok := schema.ResolveQuery(scriptout.ConfigFromContext(ctx), a.Query)
				if !ok {
					return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized,
						fmt.Sprintf("query %q is not defined in this backend's config.queries", a.Query))
				}
				return p.List(ctx, expr, a.IDsOnly)
			},
		},
		// files/commits are targeted ops (bead pg2-2j5ac.28.2), matching
		// show's existing convention (id-keyed, resolved to the one owning
		// backend) rather than list's fan-out scheme.
		"files": {
			SchemaVersion: schema.PRSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID string `json:"id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode files args: "+err.Error())
				}
				return p.Files(ctx, a.ID)
			},
		},
		"commits": {
			SchemaVersion: schema.PRSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID string `json:"id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode commits args: "+err.Error())
				}
				return p.Commits(ctx, a.ID)
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
			SchemaVersion: schema.PRSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				// auth_status always answers with a well-formed result
				// (never a wire-level error) — the AuthStatus.State field
				// itself carries success/failure, matching pg-connector's
				// existing fan-out convention (cmd/pg-connector/auth.go).
				// A freedom-boundary choice: this pr-capability table
				// cannot distinguish CheckAuth's failure kinds without a
				// pr-specific auth-invalid sentinel taxonomy, so any
				// CheckAuth error is reported as the generic AuthMissing
				// state with the underlying error folded into Detail.
				if err := ac.CheckAuth(ctx); err != nil {
					return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: err.Error()}, nil
				}
				return scriptout.AuthStatus{State: scriptout.AuthOK}, nil
			},
		}
	}

	return table
}
