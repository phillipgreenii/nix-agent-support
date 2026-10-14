package ciops

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// getLogsArgs are the arguments of get_logs.
type getLogsArgs struct {
	RunID string `json:"run_id"`
	Repo  string `json:"repo"`
}

// getLogsCall states what the one origin call of get_logs is for: a class 1 call
// (a caller is waiting) whose spend is booked as passthrough. The call is the old
// op's porcelain `gh run view --log --repo <repo> -- <run id>`: GitHub's REST
// endpoint for a run's logs answers a redirect to an archive, not the text the op
// returns, so the metered runner books the call's estimate (one core request).
func getLogsCall(ctx context.Context) context.Context {
	return metered.WithCall(ctx, ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposePassthrough})
}

// getLogs answers get_logs: a METERED PASS-THROUGH. The daemon performs the call
// itself so the governor sees and limits its spend, and stores nothing (no group,
// no entity, no job row). An empty run id and an invalid repo are
// invalid_argument (the backend refuses them before any gh call); a governor
// refusal or a rate-limit answer is unavailable naming the cap, the reserve or the
// pause (metered.WireError). The result is the raw log bytes, as the old handler
// returns them.
func (h *handler) getLogs(ctx context.Context, raw json.RawMessage) (any, error) {
	var a getLogsArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode get_logs args: "+err.Error())
	}
	if blank(a.RunID) {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "get_logs: run ID is required")
	}
	out, err := h.deps.Logs.GetLogs(getLogsCall(ctx), a.RunID, a.Repo)
	if err != nil {
		return nil, wire("get_logs "+a.RunID, err)
	}
	router.Annotate(ctx, "origin", false, 0, 0)
	return out, nil
}
