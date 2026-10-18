package ciops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// rerunArgs are the arguments of rerun_failed.
type rerunArgs struct {
	PRID string `json:"pr_id"`
}

// writeCall states what every origin call of the write is for: a class 2 call
// (the write invalidates the group it concerns) booked as purpose write, in the
// runs group.
var writeCall = ratefeed.Meta{
	Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite, Group: groups.Runs, BatchSize: 1,
}

// rerunFailed answers rerun_failed, a WRITE through the daemon. In order:
//
//  1. The PR is resolved to its node id (the one first-sighting origin call when
//     the alias table has never seen the reference), so two spellings of one PR
//     share one lock.
//  2. The write is serialized on the PR (groupsvc.Service.SerializeWrite); writes
//     to different PRs run in parallel and the wait for the lock ends at the
//     caller's deadline.
//  3. Inside the lock the PR's CURRENT head is obtained with Ensure(summary,
//     Fresh: true): the stored summary can be a minute old right after a push,
//     and this one summary fetch is the cost of not sending a duplicate rerun.
//  4. The runs are read from origin for the precondition (runs.Fetcher, class 2,
//     purpose write), outside the cache and never stored. A run of the current
//     head that is queued or in progress makes the answer a no-op success with no
//     request sent; this is also what makes a retry after an unknown outcome safe
//     (the first attempt's rerun shows as an active run).
//  5. Otherwise the newest run with a rerunnable conclusion is rerun by REST,
//     POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun-failed-jobs, through
//     the governor (class 2, purpose write). The old op's `gh run rerun <id>
//     --failed` named no repository; the repository is in the path here. No such
//     run is not_found, as today.
//  6. On success, and only then, the runs group is invalidated and its refresh
//     queued at class 2 (InvalidateAndQueue), so a refresh that started before
//     the write loses its commit and reports no change. A failed request
//     invalidates nothing.
//
// The result is nil, as the old op's.
func (h *handler) rerunFailed(ctx context.Context, raw json.RawMessage) (any, error) {
	var a rerunArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode rerun_failed args: "+err.Error())
	}
	if blank(a.PRID) {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "rerun_failed: pr id is required")
	}
	what := "rerun_failed " + a.PRID
	node, _, err := h.deps.Engine.Resolve(ctx, a.PRID)
	if err != nil {
		return nil, wire(what, err)
	}
	err = h.deps.Engine.SerializeWrite(ctx, node, func(ctx context.Context) error {
		return h.rerunLocked(ctx, a.PRID, node)
	})
	if err != nil {
		return nil, wire(what, err)
	}
	router.Annotate(ctx, "origin", false, 0, 0)
	return nil, nil
}

// rerunLocked is steps 3 to 6, run under the PR's write lock.
func (h *handler) rerunLocked(ctx context.Context, id, node string) error {
	res, err := h.deps.Engine.Ensure(ctx, node, []string{groups.Summary}, groupsvc.EnsureOptions{Fresh: true})
	if err != nil {
		return err
	}
	if len(res.Groups) == 0 || res.Groups[0].Status != groupsvc.Fresh {
		// A stale summary could name an old head: refuse rather than guess.
		cause := errors.New("the pull request's current head could not be read")
		if len(res.Groups) > 0 && res.Groups[0].Err != nil {
			cause = fmt.Errorf("the pull request's current head could not be read: %w", res.Groups[0].Err)
		}
		return cause
	}
	sum := res.Summary
	ref, err := entity.NewRef(sum.Repo, sum.Number)
	if err != nil {
		return fmt.Errorf("the pull request's repository is unusable: %w", err)
	}
	if sum.HeadRefOid == "" || sum.HeadRefName == "" {
		return errors.New("the pull request has no head to rerun")
	}
	repo := ref.NameWithOwner()

	outs := h.deps.Runs.Fetch(ctx, writeCall, []groups.Target{{
		NodeID: node, Ref: ref, Stamps: groups.StampsOf(sum), HeadRefName: sum.HeadRefName,
		State: sum.State, Terminal: summary.IsTerminal(sum.State),
	}})
	if len(outs) != 1 {
		return errors.New("the runs of the pull request could not be read")
	}
	if outs[0].Err != nil {
		return outs[0].Err
	}
	content, err := runs.Decode(outs[0].Content)
	if err != nil {
		return err
	}
	if runs.Active(content, sum.HeadRefOid) {
		// Already queued or in progress: the no-op answer. Nothing is sent and
		// nothing is invalidated.
		return nil
	}
	target, ok := newestRerunnable(content.Runs)
	if !ok {
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("no failed runs to rerun for %s", id))
	}
	if err := h.sendRerun(ctx, repo, target.ID); err != nil {
		return err
	}
	// The rerun was accepted: the invalidation MUST happen even when the caller's
	// deadline passed meanwhile.
	if err := h.deps.Engine.InvalidateAndQueue(context.WithoutCancel(ctx), node, groups.Runs); err != nil {
		return scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("the rerun of run %s was sent but the runs group could not be invalidated: %v", target.ID, err))
	}
	return nil
}

// newestRerunnable returns the first run (GitHub lists newest first) whose
// conclusion can be rerun: failure, timed_out, startup_failure or cancelled.
func newestRerunnable(rs []schema.CIRun) (schema.CIRun, bool) {
	for _, r := range rs {
		if runs.Rerunnable(r.Conclusion) {
			return r, true
		}
	}
	return schema.CIRun{}, false
}

// sendRerun asks GitHub to rerun the failed jobs of one run, by an
// explicit-repository REST request through the Feed (class 2, purpose write). A
// 4xx or 5xx answer is an error: 404 reads as not_found and 403 as an
// authentication failure, as metered.WireError classifies gh's own text for them.
func (h *handler) sendRerun(ctx context.Context, repo, runID string) error {
	if _, err := strconv.ParseUint(runID, 10, 64); err != nil {
		return fmt.Errorf("run id %q is not a number", runID)
	}
	endpoint := "repos/" + repo + "/actions/runs/" + runID + "/rerun-failed-jobs"
	resp, err := h.deps.Feed.REST(ctx, ratefeed.RESTCall{Meta: writeCall, Args: []string{endpoint, "--method", "POST"}})
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("GitHub gave no answer to the rerun request")
	}
	if resp.StatusCode < 400 {
		return nil
	}
	msg := fmt.Sprintf("POST %s: HTTP %d", endpoint, resp.StatusCode)
	if detail := strings.TrimSpace(string(resp.Body)); detail != "" {
		msg += ": " + truncate(detail, 200)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("%s: %w", msg, github.ErrGHAuthInvalid)
	}
	return errors.New(msg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
