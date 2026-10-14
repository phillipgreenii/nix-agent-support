package ciops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/answer"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// listRunsArgs are the arguments of list_runs.
type listRunsArgs struct {
	PRID  string `json:"pr_id"`
	Fresh bool   `json:"fresh"`
}

// listRuns answers list_runs from the store: the groups it reads are the PR's
// summary (its head, base and branch, which the runs group is judged against and
// fetched with) and runs. Both are read through groupsvc.Service.Ensure, so a
// fresh pair is answered locally, a stale one is fetched at interactive priority,
// stale content inside expiry is served with stale: true when the origin cannot
// answer, and fresh: true forces both to origin.
//
// The result is the []schema.CIRun the old backend returns: newest first (GitHub's
// order, which the group keeps), lower-case status and conclusion, the provider,
// head_sha, attempt, repo and the jobs of the current head's non-passing runs. Each
// element's pr_id is the id as the caller wrote it and its as_of is the runs
// group's fetched_at; stale and the annotations (served_from, age_seconds,
// groups[]) come from answer.ApplyRuns. A PR with no runs answers an empty list,
// which marshals as [] exactly as the old op does when gh lists none.
func (h *handler) listRuns(ctx context.Context, raw json.RawMessage) (any, error) {
	var a listRunsArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list_runs args: "+err.Error())
	}
	if blank(a.PRID) {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "list_runs: pr id is required")
	}
	res, err := h.deps.Engine.Ensure(ctx, a.PRID, []string{groups.Summary, groups.Runs}, groupsvc.EnsureOptions{Fresh: a.Fresh})
	if err != nil {
		return nil, wire("list_runs "+a.PRID, err)
	}
	var runsRes groupsvc.GroupResult
	found := false
	for _, g := range res.Groups {
		if g.Group == groups.Runs {
			runsRes, found = g, true
		}
	}
	if !found {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "list_runs: the engine answered no runs group")
	}
	if runsRes.Status == groupsvc.Unavailable {
		cause := runsRes.Err
		if cause == nil {
			cause = fmt.Errorf("no usable runs for %s", a.PRID)
		}
		return nil, wire("list_runs "+a.PRID, cause)
	}
	content, err := runs.Decode(runsRes.Row.Content)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("list_runs %s: stored runs unreadable: %v", a.PRID, err))
	}

	out := content.Runs
	if out == nil {
		out = []schema.CIRun{}
	}
	p := answer.From(res.Groups, h.now())
	answer.ApplyRuns(out, p)
	asOf := runsRes.Row.FetchedAt.UTC().Format(time.RFC3339)
	for i := range out {
		out[i].PRID = a.PRID
		out[i].AsOf = asOf
	}
	answer.Record(ctx, p, 0)
	return out, nil
}
