// dispatch.go: builds the calendar capability's own op-dispatch table,
// bound to a concrete Provider. The Tier-1 core's generic serve-loop entry
// point (pkg/scriptout.ServeLoop) is capability-agnostic; a Tier-2 calendar
// backend's own main() calls NewDispatchTable and hands the result to
// ServeLoop — this package builds no binary of its own (INV-WIRE-1),
// mirroring pkg/provider/thread/dispatch.go's identical structure and
// error-passthrough convention.
package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the calendar capability's op-dispatch table for
// p: "list" and "list_events" always; auth_status only when p also
// implements pkg/provider.AuthChecker, asserted via a type-check rather
// than folded into the Provider interface (INV-AUTH-1). Every handler
// passes p's returned error straight through unwrapped — a well-behaved
// Provider implementation (built by a Tier-2 calendar backend packet) is
// responsible for wrapping its own errors with the matching
// pkg/scriptout.Err* sentinel (e.g. ErrUnavailable); this table does no
// sentinel translation of its own, except for its own args-decoding
// failures (invalid_argument) below.
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		// list resolves args.query against the request's own
		// config.queries block (via scriptout.ConfigFromContext)
		// CENTRALLY, here, rather than inside every backend's own p.List
		// — so query_not_recognized is reported identically by every
		// calendar backend, with no duplicated resolution logic across
		// them [freedom boundary], mirroring
		// pkg/provider/thread/dispatch.go's identical "list" entry. The
		// resolved schema.QueryExpr's own elements are NOT free text: per
		// Provider.List's own doc comment (iface.go), each element MUST be
		// a Go time.ParseDuration-parseable duration string ("how far
		// ahead of NOW to look"); when query carries more than one
		// element, the LARGEST parsed duration wins; a malformed element
		// MUST answer scriptout.ErrInvalidArgument, never silently ignored
		// or defaulted. That parsing itself is p.List's own concern (this
		// table only resolves the caller-facing query NAME centrally) —
		// stated here per this docket's own acceptance criteria, so the
		// convention is documented at the dispatch-table entry as well as
		// on the interface method itself.
		"list": {
			SchemaVersion: schema.CalendarSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Query   string `json:"query"`
					IDsOnly bool   `json:"ids_only"`
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
		// list_events is its own, distinctly-named wire op — never reusing
		// "list" above — with wire-envelope JSON args keys
		// start/end/calendar (RFC3339 strings for the first two; calendar
		// a plain string, empty when unset), pinned by this docket's own
		// Contract precisely so a later packet's scriptout.Invoke call
		// needs no further guesswork. A malformed (non-RFC3339) start/end
		// answers scriptout.ErrInvalidArgument, mirroring "list"'s own
		// decode-failure convention above.
		"list_events": {
			SchemaVersion: schema.CalendarSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Start    string `json:"start"`
					End      string `json:"end"`
					Calendar string `json:"calendar"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list_events args: "+err.Error())
				}
				start, err := time.Parse(time.RFC3339, a.Start)
				if err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "parse start (RFC3339): "+err.Error())
				}
				end, err := time.Parse(time.RFC3339, a.End)
				if err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "parse end (RFC3339): "+err.Error())
				}
				return p.ListEvents(ctx, start, end, a.Calendar)
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
			SchemaVersion: schema.CalendarSchemaVersion,
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
