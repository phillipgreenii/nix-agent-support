package read

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// showArgs are the wire arguments of show. Unknown extra keys are ignored, as in
// the old handler. Watch is read with feedop.WatchArg, so a value that is not a
// boolean is invalid_argument.
type showArgs struct {
	ID    string `json:"id"`
	Fresh bool   `json:"fresh"`
	Watch bool   `json:"-"`
}

func decodeShow(raw json.RawMessage) (showArgs, error) {
	var a showArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return showArgs{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode show args: "+err.Error())
	}
	watch, err := feedop.WatchArg(raw)
	if err != nil {
		return showArgs{}, err
	}
	a.Watch = watch
	return a, nil
}

// show answers one issue from the summary and detail groups.
func (h *handlers) show(ctx context.Context, raw json.RawMessage) (any, error) {
	a, err := decodeShow(raw)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(a.ID)
	switch {
	case key == "":
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "issue: id required")
	case !internal.IsJiraKey(key):
		// The shape is judged here so that no origin call is made for it.
		return nil, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("issue %q not found: %s", key, eventlog.NotJiraKeyText))
	case h.deps.Summary == nil || h.deps.Detail == nil:
		return nil, unavailable("the group services are not available")
	}
	id := groups.FoldRef(key)

	// known: the daemon already holds the issue, so a one-off watch applies
	// before the read and a forced read can start from the detail group.
	known := false
	if h.deps.Store != nil {
		if known, err = h.isKnown(ctx, id); err != nil {
			return nil, unavailable("read the stored summary of %s: %v", id, err)
		}
	}
	if a.Watch {
		if !h.canWatch() {
			return nil, errNoWatch()
		}
		if known {
			if err := h.watchOne(ctx, id, engine.WatchLate); err != nil {
				return nil, err
			}
		}
	}

	sres, dres, err := h.readGroups(ctx, key, id, a, known)
	if err != nil {
		return nil, err
	}
	if sres.Content.Gone {
		return nil, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("issue %s not found", id))
	}
	dg, ok := detailGroup(dres)
	switch {
	case !ok:
		return nil, unavailable("the detail group of %s was not answered", id)
	case dg.Status == groupsvc.Unavailable:
		cause := dg.Err
		if cause == nil {
			cause = fmt.Errorf("the %s group could not be fetched", groups.Detail)
		}
		return nil, h.wire(cause)
	}
	var detail groups.DetailContent
	if err := json.Unmarshal(dg.Row.Content, &detail); err != nil {
		return nil, unavailable("the stored %s group of %s is unreadable: %v", groups.Detail, id, err)
	}

	now := h.now()
	prov := provenanceOf([]servedGroup{
		{group: groups.Summary, row: sres.Row, fromOrigin: sres.FetchedNow, stale: sres.Stale},
		{group: groups.Detail, row: dg.Row, fromOrigin: dg.FromOrigin, stale: dg.Status == groupsvc.Stale},
	}, now)

	out := sres.Content.Issue
	out.Description = detail.Description
	out.Deps = append([]schema.IssueDependency(nil), detail.Deps...)
	out.StatusChangedAt = detail.StatusChangedAt
	out.OperatorUpdatedAt = detail.OperatorUpdatedAt
	if h.deps.Store != nil {
		ent, found, err := h.deps.Store.GetEntity(context.WithoutCancel(ctx), groups.EntityKind, id)
		if err != nil {
			return nil, unavailable("read the entity row of %s: %v", id, err)
		}
		if found {
			out.Version = ent.Version
		}
		// The stored watched flag rides beside the content, never in it: it is read
		// here AFTER a one-off watch of this call was applied, and reading it
		// changes nothing (no version, no row, no event).
		out.EntityMeta = feedop.EntityMetaOf(ent, found)
	}
	prov.applyIssue(&out)
	prov.record(ctx)
	return &out, nil
}

// readGroups reads the summary and the detail group through the two Ensure
// calls and returns both answers.
//
// A forced read (fresh: true) must reach the origin once per group. The detail
// group's Ensure first makes the summary, the primary group, current, and a
// forced one refetches it, so on an issue the daemon already holds the forced
// detail Ensure goes first and the summary is then read without the option (a
// local hit on what it just stored); asking the summary first as well would
// fetch it twice. An issue the daemon has never seen has nothing stored, so
// the summary read goes first (it is what stores and tracks it) and the detail
// is fetched once because nothing is stored for it, forced or not. That same
// first-sighting read is where the one-off watch of an unknown issue is
// applied, after the read stored it.
func (h *handlers) readGroups(ctx context.Context, key, id string, a showArgs, known bool) (summary.Result, groupsvc.EnsureResult, error) {
	if a.Fresh && known {
		dres, err := h.deps.Detail.Ensure(ctx, id, []string{groups.Detail}, groupsvc.EnsureOptions{Fresh: true})
		if err != nil {
			return summary.Result{}, groupsvc.EnsureResult{}, h.wire(err)
		}
		sres, err := h.deps.Summary.Ensure(ctx, key, summary.EnsureOptions{})
		if err != nil {
			return summary.Result{}, groupsvc.EnsureResult{}, h.wire(err)
		}
		return sres, dres, nil
	}
	sres, err := h.deps.Summary.Ensure(ctx, key, summary.EnsureOptions{Fresh: a.Fresh})
	if err != nil {
		return summary.Result{}, groupsvc.EnsureResult{}, h.wire(err)
	}
	if a.Watch && !known && !sres.Content.Gone {
		if err := h.watchOne(ctx, id, engine.WatchBaseline); err != nil {
			return summary.Result{}, groupsvc.EnsureResult{}, err
		}
	}
	// A summary this call just fetched is current, and a forced detail Ensure
	// would fetch it again.
	dres, err := h.deps.Detail.Ensure(ctx, id, []string{groups.Detail}, groupsvc.EnsureOptions{Fresh: a.Fresh && !sres.FetchedNow})
	if err != nil {
		return summary.Result{}, groupsvc.EnsureResult{}, h.wire(err)
	}
	return sres, dres, nil
}

// detailGroup finds the detail group's result.
func detailGroup(res groupsvc.EnsureResult) (groupsvc.GroupResult, bool) {
	for _, g := range res.Groups {
		if g.Group == groups.Detail {
			return g, true
		}
	}
	return groupsvc.GroupResult{}, false
}
