// dispatch.go: builds the ci capability's own op-dispatch table, bound to a
// concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; a sibling Tier-2 CI
// backend's own main() calls NewDispatchTable and hands the result to
// ServeLoop — this package builds no binary of its own [design: §4.2],
// mirroring pkg/provider/pr.NewDispatchTable's own convention.
package ci

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the ci capability's op-dispatch table for p:
// list_runs, get_logs, and rerun_failed always; auth_status only when p
// also implements pkg/provider.AuthChecker, asserted via a type-check
// rather than folded into the Provider interface [design: §4.6]. Every
// handler passes p's returned error straight through unwrapped — a
// well-behaved Provider implementation (built by the Tier-2 CI backend
// packet) is responsible for wrapping its own errors with the matching
// pkg/scriptout.Err* sentinel (e.g. ErrNotFound); this table does no
// sentinel translation of its own.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"list_runs": {
			SchemaVersion: schema.CISchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					PRID string `json:"pr_id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode list_runs args: "+err.Error())
				}
				return p.ListRuns(ctx, a.PRID)
			},
		},
		"get_logs": {
			SchemaVersion: schema.CISchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					RunID string `json:"run_id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode get_logs args: "+err.Error())
				}
				return p.GetLogs(ctx, a.RunID)
			},
		},
		"rerun_failed": {
			SchemaVersion: schema.CISchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					PRID string `json:"pr_id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "decode rerun_failed args: "+err.Error())
				}
				return nil, p.RerunFailed(ctx, a.PRID)
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
			SchemaVersion: schema.CISchemaVersion,
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
