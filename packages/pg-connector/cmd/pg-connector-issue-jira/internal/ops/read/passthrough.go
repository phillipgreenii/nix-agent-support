package read

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// passthrough returns the five ops that go to the origin and store nothing:
// the handlers of the shared dispatch tables over the governed backend. Every
// origin call such a handler makes is class 1 (a caller is waiting) with the
// purpose passthrough, and passes the governor because the backend's runner is
// the governed *origin.Runner. Nothing they return is stored: there is no group,
// no entity row and no query row behind them, so no stale answer to fall back to
// either.
//
// With no Backend each op answers unavailable, so the set of handlers is the
// same either way.
func (h *handlers) passthrough() map[string]scriptout.OpHandler {
	ops := []string{OpChildren, OpSearch, OpListActivity, OpDeps, OpAuthStatus}
	out := make(map[string]scriptout.OpHandler, len(ops))
	if h.deps.Backend == nil {
		for _, op := range ops {
			out[op] = scriptout.OpHandler{SchemaVersion: versionOf(op), Handle: func(context.Context, json.RawMessage) (any, error) {
				return nil, unavailable("the backend is not available")
			}}
		}
		return out
	}
	be := h.deps.Backend
	issues := issue.NewDispatchTable(be)
	tables := map[string]scriptout.DispatchTable{
		OpChildren:     issues,
		OpDeps:         issues,
		OpAuthStatus:   issues,
		OpSearch:       search.NewDispatchTable(be),
		OpListActivity: activity.NewDispatchTable(be),
	}
	for _, op := range ops {
		handler, ok := tables[op][op]
		if !ok {
			// The backend implements every interface that adds these ops, so this
			// is a build-time mistake, never a runtime condition.
			panic("read: the shared dispatch table has no " + op + " handler")
		}
		if op == OpAuthStatus {
			handler = h.watchAuth(handler)
		}
		out[op] = h.metered(handler)
	}
	return out
}

// versionOf is the schema version an op is served at.
func versionOf(op string) int {
	switch op {
	case OpSearch:
		return schema.SearchSchemaVersion
	case OpListActivity:
		return schema.ActivitySchemaVersion
	}
	return schema.IssueSchemaVersion
}

// passthroughCall states what every origin call of these ops is for.
func passthroughCall(ctx context.Context) context.Context {
	return origin.WithCall(ctx, origin.Call{
		Class: governor.ClassInteractive, Purpose: governor.PurposePassthrough, BatchSize: 1,
	})
}

// metered runs op with its origin calls booked as pass-through spend and leaves
// served_from: "origin" on the router's carrier (the answer is never from the
// store).
func (h *handlers) metered(op scriptout.OpHandler) scriptout.OpHandler {
	return scriptout.OpHandler{SchemaVersion: op.SchemaVersion, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
		res, err := op.Handle(passthroughCall(ctx), raw)
		router.Annotate(ctx, servedFromOrigin, false, 0, 0)
		return res, err
	}}
}

// watchAuth wraps the auth_status handler so the daemon's authentication
// tracker follows its answer: an OK answer records the acting identity it
// reports (an answer that reports none leaves the recorded one alone), and a
// failed one marks the daemon unauthenticated. A failure that is only the 429
// pause is the origin being rate limited, not a credential problem, and marks
// nothing.
func (h *handlers) watchAuth(op scriptout.OpHandler) scriptout.OpHandler {
	return scriptout.OpHandler{SchemaVersion: op.SchemaVersion, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
		res, err := op.Handle(ctx, raw)
		if err != nil || h.deps.Auth == nil {
			return res, err
		}
		st, ok := res.(scriptout.AuthStatus)
		if !ok {
			return res, err
		}
		switch {
		case st.State == scriptout.AuthOK:
			if st.Detail != "" {
				h.deps.Auth.SetIdentity(st.Detail)
			}
		case !paused(st.Detail):
			h.deps.Auth.MarkUnauthenticated(errors.New(string(st.State) + ": " + st.Detail))
		}
		return res, err
	}}
}

// paused reports whether a failure text is the paused-origin error.
func paused(detail string) bool {
	return strings.Contains(detail, origin.ErrPaused.Error())
}
