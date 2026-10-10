package rules

import (
	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// anchorAdoptionRule is the adoption rule (ported): existing work items that
// carry no dedup_key, matched by exact title (anchors by their <repo>#<n>:
// prefix) or by node_id when the tracker exposes it, are adopted by writing
// the dedup_key onto them. The key is written in the <type>:<id>:<kind> form
// for every match, node_id matches included: rewriting an adopted key to the
// node_id form is a separate rule. A closed item is adopted like an open one,
// and a parked (human-labeled) item is adopted but never re-labeled: the
// action writes the dedup_key metadata and nothing else.
//
// It runs on every reconcile, so the first reconcile per entity after cutover
// adopts everything, and it is idempotent: an item that already has a
// dedup_key is not adoptable and yields no action.
type anchorAdoptionRule struct{}

func (anchorAdoptionRule) ID() string          { return anchorRuleAdoption }
func (anchorAdoptionRule) Kind() workitem.Kind { return "" }

func (anchorAdoptionRule) Evaluate(in decide.Input) decide.Result {
	e := workitem.EntityRefFrom(in.View)
	var acts []action.Action
	var unadoptable []string
	for _, ad := range in.Items.Adoptable() {
		if ad.Kind == workitem.KindFocusItem {
			continue
		}
		ctx, ok := anchorAdoptionContext(ad.Item, ad.Kind)
		if !ok {
			unadoptable = append(unadoptable, ad.Item.ID)
			continue
		}
		key := workitem.DedupKey(e, ad.Kind, ctx)
		acts = append(acts, action.Action{
			Op: action.OpUpdate, Kind: string(ad.Kind), Target: anchorTarget(ad.Item.ID),
			Fields: action.Fields{Metadata: map[string]string{"dedup_key": key}},
			Facts:  map[string]any{"matched_by": ad.MatchedBy, "dedup_key": key},
		})
	}
	if len(acts) > 0 {
		return decide.Result{Actions: acts}
	}
	var facts map[string]any
	if len(unadoptable) > 0 {
		facts = map[string]any{"unadoptable": unadoptable}
	}
	return anchorSkip(action.ReasonNotMatched, facts)
}

// anchorAdoptionContext reads the dedup-key context a keyless item carries:
// the fbsum: label digest of a process-feedback item, the head_sha of a
// fix-ci item, the branch/head_sha/base/base_sha tuple of a resolve-conflict
// item. ok is false when a value the kind's key needs is missing, because a
// key with an empty context part would name no real context. review-pr and the
// anchor key on the PR alone.
func anchorAdoptionContext(it workitem.Item, k workitem.Kind) (workitem.Context, bool) {
	md := it.Metadata
	switch k {
	case workitem.KindProcessFeedback:
		c := workitem.Context{Digest: it.Digest()}
		return c, c.Digest != ""
	case workitem.KindFixCI:
		c := workitem.Context{HeadSHA: md["head_sha"]}
		return c, c.HeadSHA != ""
	case workitem.KindResolveConflict:
		c := workitem.Context{Branch: md["branch"], HeadSHA: md["head_sha"], Base: md["base"], BaseSHA: md["base_sha"]}
		return c, c.Branch != "" && c.HeadSHA != "" && c.Base != "" && c.BaseSHA != ""
	}
	return workitem.Context{}, true
}
