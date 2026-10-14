package changes

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// stamped reports whether the group's rows record the head and base they were
// fetched for (the summary rules' Stamped groups).
func stamped(group string) bool {
	return group == groups.Files || group == groups.Commits || group == groups.Runs
}

// DetectGroup is the group seam's body (groupsvc.Config.OnGroupStoring): it runs
// inside the transaction that stored next. Only a stored refresh reaches it (a
// refresh that started before a write invalidation loses its commit in the
// group engine and never gets here).
//
// A nil prev is the group's baseline and adds no fields, but still satisfies the
// barrier for an awaited group when the fetch counts. A fetch counts for an
// awaited group when it started at or after the group's Since and, for the
// stamped groups, was fetched for the head and base the pending row records.
// With a pending row the group's fields merge into it; without one a non-empty
// result appends at once.
func (e *Engine) DetectGroup(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info groupsvc.RefreshInfo) error {
	if next.Kind != LogKindPR || next.Group == groups.Summary {
		return nil
	}
	node := next.ID
	origin := e.originFor(node, next.Group, info.Purpose)

	var res classify.Result
	if prev != nil {
		var err error
		if res, err = classify.Group(next.Group, prev.Content, next.Content, e.bodyCap); err != nil {
			return fmt.Errorf("changes: classify %s of %s: %w", next.Group, node, err)
		}
	}

	p, has, err := tx.GetPending(ctx, LogKindPR, node)
	if err != nil {
		return err
	}
	if !has {
		return e.appendNow(ctx, tx, node, res, origin)
	}
	pres, aw, err := pendingResult(p)
	if err != nil {
		return err
	}
	kept := aw[:0:0]
	for _, a := range aw {
		counts := a.Group == next.Group && !info.Started.Before(a.Since) &&
			(!stamped(next.Group) || (next.FetchedForHead == p.HeadSHA && next.FetchedForBase == p.BaseSHA))
		if !counts {
			kept = append(kept, a)
		}
	}
	return e.putPending(ctx, tx, p, classify.Merge(pres, res), kept)
}
