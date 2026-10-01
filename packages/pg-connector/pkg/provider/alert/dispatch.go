// dispatch.go: builds the alert capability's own op-dispatch table, bound to
// a concrete Provider, mirroring pkg/provider/thread/dispatch.go's structure
// and error-passthrough convention. A Tier-2 alert backend's main() calls
// NewDispatchTable, merges attention.NewDispatchTable into it, and hands the
// result to scriptout.ServeLoop (INV-WIRE-1).
package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// resolveOptionalQuery resolves an alert query NAME against the request's
// config.queries. An empty name is NOT an error: it means "the entire firing
// set, unfiltered" and resolves to a nil expression. A non-empty name that is
// not defined reports query_not_recognized.
func resolveOptionalQuery(ctx context.Context, name string) (schema.QueryExpr, error) {
	if name == "" {
		return nil, nil
	}
	expr, ok := schema.ResolveQuery(scriptout.ConfigFromContext(ctx), name)
	if !ok {
		return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized,
			fmt.Sprintf("query %q is not defined in this backend's config.queries", name))
	}
	return expr, nil
}

// NewDispatchTable builds the alert capability's op-dispatch table for p:
// "show", "list", and "list_history" always; auth_status only when p also
// implements pkg/provider.AuthChecker (INV-AUTH-1). Every handler passes p's
// returned error straight through unwrapped.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"show": {
			SchemaVersion: schema.AlertSchemaVersion,
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
		"list": {
			SchemaVersion: schema.AlertSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Query   string `json:"query"`
					IDsOnly bool   `json:"ids_only"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list args: "+err.Error())
				}
				expr, err := resolveOptionalQuery(ctx, a.Query)
				if err != nil {
					return nil, err
				}
				return p.List(ctx, expr, a.IDsOnly)
			},
		},
		"list_history": {
			SchemaVersion: schema.AlertSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Since string `json:"since"`
					Until string `json:"until"`
					Query string `json:"query"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list_history args: "+err.Error())
				}
				since, err := time.Parse(time.RFC3339, a.Since)
				if err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "parse since (RFC3339): "+err.Error())
				}
				until, err := time.Parse(time.RFC3339, a.Until)
				if err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "parse until (RFC3339): "+err.Error())
				}
				expr, err := resolveOptionalQuery(ctx, a.Query)
				if err != nil {
					return nil, err
				}
				return p.ListHistory(ctx, since, until, expr)
			},
		},
	}

	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.AlertSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				if err := ac.CheckAuth(ctx); err != nil {
					return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: err.Error()}, nil
				}
				return scriptout.AuthStatus{State: scriptout.AuthOK}, nil
			},
		}
	}

	return table
}
