package rules

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalConflictPresent, conflictRule{})
}

// conflictRule is conflict.present (design 7.3, S13): one resolve-conflict work
// item per conflict context (head branch, head commit, base branch, base
// commit; S26). An item for the current context, open or closed however it was
// closed, is never recreated or reopened; a different context is a different
// conflict and gets a new item. Once the PR is mergeable again, an open
// unclaimed item is closed and a claimed one is left for its worker.
//
// base_sha is show-only (set by pg-desk's hydration), so a base-commit move
// while the PR stays conflicting is seen at the entity's next hydration: the
// decider role is bound to every pr.* kind, so the next routed item re-derives
// the context. No separate base_changed record or reconcile hook exists.
type conflictRule struct{}

func (conflictRule) ID() string          { return "conflict.present" }
func (conflictRule) Kind() workitem.Kind { return workitem.KindResolveConflict }

func (conflictRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if v == nil || (v.Decorations.Relationship != "mine" && v.Decorations.Relationship != "co-owned") {
		return conflictNotMatched(nil)
	}
	snap := v.Snapshot
	switch {
	case strings.EqualFold(snap.Mergeable, "CONFLICTING"):
		return conflictPresent(in)
	case strings.EqualFold(snap.Mergeable, "MERGEABLE"):
		return conflictCleared(in)
	}
	// UNKNOWN or unreported mergeability is neither a conflict nor a clearing.
	return conflictNotMatched(map[string]any{"mergeable": snap.Mergeable})
}

func conflictNotMatched(facts map[string]any) decide.Result {
	return decide.Result{Skip: &action.Skip{Reason: action.ReasonNotMatched, Facts: facts}}
}

func conflictStr(s string) *string { return &s }

// conflictPresent handles a conflicting PR.
func conflictPresent(in decide.Input) decide.Result {
	v := in.View
	snap := v.Snapshot
	ctx := workitem.Context{HeadSHA: snap.HeadSHA, Branch: snap.Branch, Base: snap.Base, BaseSHA: snap.BaseSHA}
	// A context with an empty member cannot be told apart from another
	// conflict, so no item is created for it.
	if ctx.HeadSHA == "" || ctx.Branch == "" || ctx.Base == "" || ctx.BaseSHA == "" {
		return conflictNotMatched(map[string]any{"reason": "incomplete conflict context"})
	}
	if item, ok := in.Items.Find(workitem.KindResolveConflict, ctx); ok {
		return decide.Result{Skip: &action.Skip{
			Reason: action.ReasonAlreadyHandled,
			Facts:  map[string]any{"item": item.ID, "head_sha": ctx.HeadSHA, "base_sha": ctx.BaseSHA},
		}}
	}

	c := workitem.ContractFor(workitem.KindResolveConflict)
	parent := action.AnchorParent
	if a, ok := in.Items.Anchor(); ok {
		parent = a.ID
	}
	key := workitem.DedupKey(workitem.EntityRefFrom(v), workitem.KindResolveConflict, ctx)
	return decide.Result{Actions: []action.Action{{
		Op:   action.OpCreate,
		Kind: string(workitem.KindResolveConflict),
		Fields: action.Fields{
			Title:       fmt.Sprintf("resolve-conflict: %s#%d", snap.Repo, snap.Number),
			IssueType:   c.IssueType,
			Description: conflictDescription(snap.Repo, snap.Number, ctx),
			Parent:      parent,
			Labels:      c.Labels,
			Metadata: map[string]string{
				"repo":      snap.Repo,
				"pr_number": strconv.Itoa(snap.Number),
				"branch":    ctx.Branch,
				"head_sha":  ctx.HeadSHA,
				"base":      ctx.Base,
				"base_sha":  ctx.BaseSHA,
				"dedup_key": key,
			},
		},
		Facts: map[string]any{"head_sha": ctx.HeadSHA, "base": ctx.Base, "base_sha": ctx.BaseSHA},
	}}}
}

// conflictCleared handles a mergeable PR: every open unclaimed resolve-conflict
// item is closed; claimed ones are left for their worker.
func conflictCleared(in decide.Input) decide.Result {
	var acts []action.Action
	var claimed []string
	for _, it := range in.Items.ByKind(workitem.KindResolveConflict) {
		if !it.Open() {
			continue
		}
		if it.Assignee != "" {
			claimed = append(claimed, it.ID)
			continue
		}
		acts = append(acts, action.Action{
			Op: action.OpClose, Kind: string(workitem.KindResolveConflict), Target: conflictStr(it.ID),
			Fields: action.Fields{Description: "The merge conflict is cleared"},
			Facts:  map[string]any{"item": it.ID},
		})
	}
	if len(acts) > 0 {
		return decide.Result{Actions: acts}
	}
	if len(claimed) > 0 {
		return conflictNotMatched(map[string]any{"claimed": claimed})
	}
	return conflictNotMatched(nil)
}

// conflictDescription is the rebase instruction: no backend reports the
// conflicting files today, so only the base is named.
func conflictDescription(repo string, number int, c workitem.Context) string {
	return fmt.Sprintf("%s#%d conflicts with %s (base commit %s). Rebase %s onto %s, resolve the conflicts and push.",
		repo, number, c.Base, c.BaseSHA, c.Branch, c.Base)
}
