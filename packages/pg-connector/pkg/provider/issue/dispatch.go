// dispatch.go: builds the issue capability's own op-dispatch table, bound
// to a concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; a sibling Tier-2 issue
// backend's own main() calls NewDispatchTable and hands the result to
// ServeLoop — this package builds no binary of its own, mirroring
// pkg/provider/pr/dispatch.go's identical structure.
package issue

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the issue capability's op-dispatch table for p:
// show, create, comment, and transition always; auth_status only when p
// also implements pkg/provider.AuthChecker, asserted via a type-check
// rather than folded into the Provider interface. Every handler passes p's
// returned error straight through unwrapped — a well-behaved Provider
// implementation (built by a Tier-2 issue backend packet) is responsible
// for wrapping its own errors with the matching pkg/scriptout.Err*
// sentinel (e.g. ErrNotFound); this table does no sentinel translation of
// its own.
//
// comment and transition report no result value of their own (Provider's
// Comment/Transition methods return only error) — a successful call's wire
// response therefore carries a null result, which is still a well-formed
// response for the targeted-op exit-code scheme's purposes.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"show": {
			SchemaVersion: schema.IssueSchemaVersion,
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
		"create": {
			SchemaVersion: schema.IssueSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var input IssueInput
				if err := scriptout.Decode(args, &input); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode create args: "+err.Error())
				}
				return p.Create(ctx, input)
			},
		},
		"comment": {
			SchemaVersion: schema.IssueSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID   string `json:"id"`
					Body string `json:"body"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode comment args: "+err.Error())
				}
				if err := p.Comment(ctx, a.ID, a.Body); err != nil {
					return nil, err
				}
				return nil, nil
			},
		},
		"transition": {
			SchemaVersion: schema.IssueSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID          string `json:"id"`
					TargetState string `json:"target_state"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode transition args: "+err.Error())
				}
				if err := p.Transition(ctx, a.ID, a.TargetState); err != nil {
					return nil, err
				}
				return nil, nil
			},
		},
	}

	// A provider not implementing AuthChecker is not treated as a
	// forced/meaningless answer: the auth_status entry is simply omitted,
	// which pg-connector's own fan-out (cmd/pg-connector/auth.go) already
	// recognizes generically via the wire-level unknown_op sentinel and
	// reports as "disabled: not applicable".
	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.IssueSchemaVersion,
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
