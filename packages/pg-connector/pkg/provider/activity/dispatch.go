// dispatch.go: builds the activity capability's own op-dispatch table, bound
// to a concrete Provider. The Tier-1 core's generic serve-loop entry point
// (pkg/scriptout.ServeLoop) is capability-agnostic; a Tier-2 backend
// implementing activity.Provider calls NewDispatchTable and hands the result
// to ServeLoop. This package builds no binary of its own. The structure and
// error-passthrough convention mirror pkg/provider/attention.NewDispatchTable.
package activity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpListActivity is the activity capability's one wire op. It aliases
// scriptout.OpListActivity, where the op's own call budget is keyed.
const OpListActivity = scriptout.OpListActivity

// NewDispatchTable builds the activity capability's op-dispatch table for p:
// list_activity always; auth_status only when p also implements
// pkg/provider.AuthChecker, asserted via a type-check rather than folded into
// the Provider interface. The list_activity handler decodes and validates the
// range args (before required; since optional and, when present, strictly
// earlier than before; each RFC3339), answering a bad range with
// scriptout.ErrInvalidArgument without reaching p. A Provider error is passed
// straight through unwrapped: a well-behaved Provider wraps its own errors
// with the matching scriptout.Err* sentinel (e.g. ErrUnavailable).
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		OpListActivity: {
			SchemaVersion: schema.ActivitySchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				since, before, err := decodeRange(args)
				if err != nil {
					return nil, err
				}
				return p.ListActivity(ctx, since, before)
			},
		},
	}

	// A provider not implementing AuthChecker simply has no auth_status entry;
	// the umbrella's fan-out already reports that generically as "not
	// applicable" via the wire-level unknown_op sentinel.
	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.ActivitySchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				// auth_status always answers with a well-formed result, never
				// a wire-level error; AuthStatus.State carries the outcome.
				if err := ac.CheckAuth(ctx); err != nil {
					return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: err.Error()}, nil
				}
				return scriptout.AuthStatus{State: scriptout.AuthOK}, nil
			},
		}
	}

	return table
}

// decodeRange decodes and validates a list_activity args payload. A zero
// since means the caller sent none.
func decodeRange(args json.RawMessage) (since, before time.Time, err error) {
	var a schema.ActivityListArgs
	if len(args) > 0 {
		if jerr := json.Unmarshal(args, &a); jerr != nil {
			return since, before, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list_activity args: "+jerr.Error())
		}
	}
	if a.Before == "" {
		return since, before, scriptout.WrapError(scriptout.ErrInvalidArgument, "list_activity: before is required")
	}
	before, perr := time.Parse(time.RFC3339, a.Before)
	if perr != nil {
		return since, before, scriptout.WrapError(scriptout.ErrInvalidArgument, "list_activity: before is not RFC3339: "+perr.Error())
	}
	if a.Since == "" {
		return since, before, nil
	}
	since, perr = time.Parse(time.RFC3339, a.Since)
	if perr != nil {
		return time.Time{}, before, scriptout.WrapError(scriptout.ErrInvalidArgument, "list_activity: since is not RFC3339: "+perr.Error())
	}
	if !since.Before(before) {
		return time.Time{}, before, scriptout.WrapError(scriptout.ErrInvalidArgument, "list_activity: since must be earlier than before")
	}
	return since, before, nil
}
