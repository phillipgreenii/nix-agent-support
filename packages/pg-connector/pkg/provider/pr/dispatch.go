// dispatch.go: builds the pr capability's own op-dispatch table, bound to a
// concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; the sibling
// "pg-connector-pr-github" backend's own main() calls NewDispatchTable and
// hands the result to ServeLoop — this package builds no binary of its own
// [design: §4.2].
package pr

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the pr capability's op-dispatch table for p:
// show, categorize, and feedback_set always; auth_status only when p also
// implements pkg/provider.AuthChecker, asserted via a type-check rather
// than folded into the Provider interface [design: §4.6]. Every handler
// passes p's returned error straight through unwrapped — a well-behaved
// Provider implementation (built by the Tier-2 backend packet) is
// responsible for wrapping its own errors with the matching
// pkg/scriptout.Err* sentinel (e.g. ErrNotFound); this table does no
// sentinel translation of its own.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"show": {
			SchemaVersion: schema.SchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID string `json:"id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode show args: "+err.Error())
				}
				return p.Show(ctx, a.ID)
			},
		},
		"categorize": {
			SchemaVersion: schema.SchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID       string `json:"id"`
					Category string `json:"category"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode categorize args: "+err.Error())
				}
				return p.Categorize(ctx, a.ID, a.Category)
			},
		},
		"feedback_set": {
			SchemaVersion: schema.SchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID          string             `json:"id"`
					CommentID   string             `json:"comment_id"`
					Disposition schema.Disposition `json:"disposition"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode feedback_set args: "+err.Error())
				}
				return p.FeedbackSet(ctx, a.ID, a.CommentID, a.Disposition)
			},
		},
	}

	// A provider not implementing AuthChecker is not treated as a
	// forced/meaningless answer: the auth_status entry is simply omitted,
	// which pg-connector's own fan-out (cmd/pg-connector/auth.go) already
	// recognizes generically via the wire-level unknown_op sentinel and
	// reports as "disabled: not applicable" [design: §4.6].
	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.SchemaVersion,
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
