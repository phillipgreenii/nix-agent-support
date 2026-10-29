package rules

import (
	"encoding/json"
	"strconv"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const (
	reviewRuleID = "review.head-advanced"

	// reviewAnnotationConsumed is the decider-state key recording the SHA of
	// the last consumed force-review request (decider.<name>.<k>).
	reviewAnnotationConsumed = "decider.pr-decider.force_review_consumed"
	// reviewAnnotationForce is the one-shot flag the clear path removes.
	reviewAnnotationForce = "force_review"

	// reviewTransitionFact names, in an update's facts, the transition the
	// review rule plans; the apply step reads it to re-check the item live.
	reviewTransitionFact = "transition"
	// reviewTransitionRefreshHead is the transition of moving an unclaimed
	// review item to the current head.
	reviewTransitionRefreshHead = "refresh-head"
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalReviewHeadAdvanced, reviewHeadAdvanced{})
}

// reviewHeadAdvanced is the review.head-advanced rule (docs/behavior/pg-decider
// work-items.md, "review.head-advanced"): a qualifying PR (mine or co-owned,
// draft included; or team and not draft) may have several review-pr items over
// time, one per head that was reviewed or is waiting for review.
//
//   - an item of ANY state whose head_sha is the PR's current head means that
//     head is being reviewed or was reviewed: nothing is created (the test is
//     on metadata, never on the dedup key, because an update rewrites head_sha);
//   - else an UNCLAIMED item (status open, no assignee) is refreshed in place:
//     its head_sha, its dedup_key suffix and its title move to the current head,
//     so a burst of pushes before any claim yields one item;
//   - else one item is created for the current head;
//   - a claimed item for an older head is left alone, and a closed one is never
//     reopened: only this rule creates review items.
//
// The one-shot force-review flag refreshes an unclaimed item the same way;
// without one it creates a NEW item even when an item for the head exists, with
// a dedup key made unique by a suffix. The flag is consumed (the SHA recorded,
// then the flag cleared) only after every earlier action of this rule
// succeeded. It is a pure function of the view and never reads who closed an
// item.
type reviewHeadAdvanced struct{}

func (reviewHeadAdvanced) ID() string { return reviewRuleID }

func (reviewHeadAdvanced) Kind() workitem.Kind { return workitem.KindReviewPR }

func (reviewHeadAdvanced) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if skip, terminal := anchorTerminalSkip(v); terminal {
		return skip
	}
	rel := v.Decorations.Relationship
	if !reviewQualifies(rel, v.Snapshot.Draft) {
		return reviewSkip(action.ReasonNotMatched, map[string]any{"relationship": rel, "draft": v.Snapshot.Draft})
	}
	head := v.Snapshot.HeadSHA
	if head == "" {
		return reviewSkip(action.ReasonNotMatched, map[string]any{"head_sha": ""})
	}
	force := v.Annotations.ForceReview

	items := in.Items.ByKind(workitem.KindReviewPR)
	var atHead []workitem.Item
	var target *workitem.Item // the unclaimed item to refresh; one at the head wins
	for i := range items {
		it := items[i]
		if it.Metadata["head_sha"] == head {
			atHead = append(atHead, it)
		}
		if reviewUnclaimed(it) && (target == nil || it.Metadata["head_sha"] == head && target.Metadata["head_sha"] != head) {
			target = &items[i]
		}
	}

	if !force && len(atHead) > 0 {
		open := atHead[0]
		for _, it := range atHead {
			if it.Open() {
				open = it
				break
			}
		}
		reason := action.ReasonAlreadyHandled
		if open.Open() {
			reason = action.ReasonReviewPending
		}
		return reviewSkip(reason, map[string]any{"head_sha": head, "item": open.ID})
	}

	var actions []action.Action
	if target != nil {
		actions = append(actions, reviewRefresh(*target, items, v, head, force))
	} else {
		actions = append(actions, reviewCreate(in, items, head, force))
	}
	if force {
		actions = append(actions, reviewConsume(v, head)...)
	}
	return decide.Result{Actions: actions}
}

// reviewUnclaimed reports whether an item is free to be refreshed: its status
// is exactly open (not in_progress, deferred, blocked or closed) and it has no
// assignee.
func reviewUnclaimed(it workitem.Item) bool { return it.State == "open" && it.Assignee == "" }

// reviewContext is the key context of a review item for head, made unique by
// a nonce when another item's key already carries the suffix (a forced review
// at a head that has an item, or an inconsistent key), so apply never dedups
// the write away. self is the id of an item being refreshed, whose own key is
// not a collision.
func reviewContext(items []workitem.Item, self, head string) workitem.Context {
	used := map[string]bool{}
	for _, it := range items {
		if it.ID == self || it.DedupKey == "" {
			continue
		}
		if _, _, _, suffix, ok := workitem.ParseKey(it.DedupKey); ok {
			used[suffix] = true
		}
	}
	ctx := workitem.Context{HeadSHA: head}
	for n := 1; used[workitem.ContextSuffix(workitem.KindReviewPR, ctx)]; n++ {
		ctx.Nonce = "r" + strconv.Itoa(n)
	}
	return ctx
}

func reviewQualifies(relationship string, draft bool) bool {
	switch relationship {
	case "mine", "co-owned":
		return true
	case "team":
		return !draft
	}
	return false
}

func reviewSkip(reason string, facts map[string]any) decide.Result {
	return decide.Result{Skip: &action.Skip{Reason: reason, Facts: facts}}
}

func reviewItemMetadata(v *view.View, head string) map[string]string {
	return map[string]string{
		"repo":      v.Snapshot.Repo,
		"pr_number": strconv.Itoa(v.Snapshot.Number),
		"branch":    v.Snapshot.Branch,
		"head_sha":  head,
		"ownership": v.Decorations.Relationship,
	}
}

func reviewCreate(in decide.Input, items []workitem.Item, head string, forced bool) action.Action {
	v := in.View
	parent := action.AnchorParent
	if a, ok := in.Items.Anchor(); ok {
		parent = a.ID
	}
	md := reviewItemMetadata(v, head)
	md["dedup_key"] = workitem.CreateKey(workitem.EntityRefFrom(v), workitem.KindReviewPR, reviewContext(items, "", head))
	return action.Action{
		Op:   action.OpCreate,
		Kind: string(workitem.KindReviewPR),
		Fields: action.Fields{
			Title:     workitem.TitleWithHead(workitem.KindReviewPR, v.Snapshot.Repo, v.Snapshot.Number, head),
			IssueType: workitem.ContractFor(workitem.KindReviewPR).IssueType,
			Parent:    parent,
			Metadata:  md,
		},
		Facts: map[string]any{"head_sha": head, "forced": forced},
	}
}

// reviewRefresh moves an unclaimed item to the current head: head_sha, the
// dedup_key suffix and the title. It keeps the identity segment of the item's
// own key (id or node_id form), and keeps the whole key when the item is
// already at the head. The apply step re-reads the item live first and drops
// the write when a claim landed in the meantime (facts.transition).
func reviewRefresh(it workitem.Item, items []workitem.Item, v *view.View, head string, forced bool) action.Action {
	target := it.ID
	md := reviewItemMetadata(v, head)
	ref := workitem.EntityRefFrom(v)
	switch typ, ident, _, _, ok := workitem.ParseKey(it.DedupKey); {
	case ok && it.Metadata["head_sha"] == head:
		md["dedup_key"] = it.DedupKey
	case ok:
		// A refresh names the PR by its node_id when it has one, so it never
		// writes back the id form adoption.node-id rewrites in the same plan.
		if ref.NodeID != "" {
			ident = ref.NodeID
		}
		md["dedup_key"] = typ + ":" + ident + ":" + string(workitem.KindReviewPR) +
			workitem.ContextSuffix(workitem.KindReviewPR, reviewContext(items, it.ID, head))
	default:
		md["dedup_key"] = workitem.CreateKey(ref, workitem.KindReviewPR, reviewContext(items, it.ID, head))
	}
	return action.Action{
		Op:     action.OpUpdate,
		Kind:   string(workitem.KindReviewPR),
		Target: &target,
		Fields: action.Fields{
			Title:    workitem.TitleWithHead(workitem.KindReviewPR, v.Snapshot.Repo, v.Snapshot.Number, head),
			Metadata: md,
		},
		Facts: map[string]any{
			"head_sha": head, "reviewed_head_sha": it.Metadata["head_sha"], "forced": forced,
			reviewTransitionFact: reviewTransitionRefreshHead,
		},
	}
}

// reviewForceSHA is the head the operator requested the forced review at:
// annotations.force_review_sha from the raw view (string or null), falling
// back to the current head when the view carries none. internal/view does not
// decode the member, so it is read from View.Raw here.
func reviewForceSHA(v *view.View, head string) string {
	var raw struct {
		Annotations struct {
			ForceReviewSHA *string `json:"force_review_sha"`
		} `json:"annotations"`
	}
	if err := json.Unmarshal(v.Raw, &raw); err == nil {
		if s := raw.Annotations.ForceReviewSHA; s != nil && *s != "" {
			return *s
		}
	}
	return head
}

// reviewConsume records the consumed SHA, then clears the flag. Both require
// every earlier action of this rule to have been applied, and the clear is
// last, so a failed run leaves the flag in place and is safe to repeat.
func reviewConsume(v *view.View, head string) []action.Action {
	sha := reviewForceSHA(v, head)
	recKey, clrKey := reviewAnnotationConsumed, reviewAnnotationForce
	return []action.Action{
		{
			Op:            action.OpAnnotate,
			Target:        &recKey,
			Fields:        action.Fields{Value: &sha},
			Facts:         map[string]any{"force_review_sha": sha, "head_sha": head},
			RequiresPrior: true,
		},
		{
			Op:            action.OpAnnotate,
			Target:        &clrKey,
			Fields:        action.Fields{Clear: true},
			Facts:         map[string]any{"force_review_sha": sha},
			RequiresPrior: true,
		},
	}
}
