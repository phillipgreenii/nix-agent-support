// dispatch.go: builds the scm capability's own op-dispatch table, bound to
// a concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; the sibling
// "pg-connector-scm-git" backend's own main() calls NewDispatchTable and
// hands the result to ServeLoop — this package builds no binary of its own
// [design: §4.2, §4.7].
package scm

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the scm capability's op-dispatch table for p:
// worktree_add, worktree_remove, worktree_list, and branch_detect always;
// auth_status only when p also implements pkg/provider.AuthChecker,
// asserted via a type-check rather than folded into the Provider interface
// [design: §4.6] — the design's own scm backend does not implement it, so
// this entry is expected to stay absent there [design: §4.6, §4.7]. Every
// handler passes p's returned error straight through unwrapped — a
// well-behaved Provider implementation (built by the Tier-2 backend packet)
// is responsible for wrapping its own errors with the matching
// pkg/scriptout.Err* sentinel (e.g. ErrNotFound); this table does no
// sentinel translation of its own.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"worktree_add": {
			SchemaVersion: schema.ScmSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					BranchOrRef string `json:"branch_or_ref"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode worktree_add args: "+err.Error())
				}
				return p.WorktreeAdd(ctx, a.BranchOrRef)
			},
		},
		"worktree_remove": {
			SchemaVersion: schema.ScmSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Path string `json:"path"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode worktree_remove args: "+err.Error())
				}
				// WorktreeRemove has no success payload of its own (its Go
				// signature returns only error) — a nil result marshals to
				// a well-formed "result": null on the wire, distinct from
				// the error envelope.
				if err := p.WorktreeRemove(ctx, a.Path); err != nil {
					return nil, err
				}
				return nil, nil
			},
		},
		"worktree_list": {
			SchemaVersion: schema.ScmSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				return p.WorktreeList(ctx)
			},
		},
		"branch_detect": {
			SchemaVersion: schema.ScmSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Cwd string `json:"cwd"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode branch_detect args: "+err.Error())
				}
				return p.BranchDetect(ctx, a.Cwd)
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
			SchemaVersion: schema.ScmSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				// auth_status always answers with a well-formed result
				// (never a wire-level error) — the AuthStatus.State field
				// itself carries success/failure, matching pg-connector's
				// existing fan-out convention (cmd/pg-connector/auth.go).
				// A freedom-boundary choice: this scm-capability table
				// cannot distinguish CheckAuth's failure kinds without a
				// scm-specific auth-invalid sentinel taxonomy, so any
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
