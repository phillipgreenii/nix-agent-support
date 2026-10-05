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
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalReviewHeadAdvanced, reviewHeadAdvanced{})
}

// reviewHeadAdvanced is the review.head-advanced rule (entity-change-flow
// design 7.3): a qualifying PR (mine or co-owned, draft included; or team and
// not draft) gets one review-pr item, reopened and refreshed whenever the PR
// head moves past the item's reviewed head. An unchanged head is no action
// however the item was closed. The one-shot force-review flag reopens or
// refreshes the item once, then is consumed: the SHA is recorded and the flag
// cleared, both gated on every earlier action of this rule having succeeded.
// It is a pure function of the view and never reads who closed an item.
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

	existing, found := in.Items.Find(workitem.KindReviewPR, workitem.Context{})
	var actions []action.Action
	switch {
	case !found:
		actions = append(actions, reviewCreate(in, head, force))
	case existing.Metadata["head_sha"] != head || force:
		if !existing.Open() {
			actions = append(actions, reviewReopen(existing, head, force))
		}
		actions = append(actions, reviewUpdate(existing, v, head, force))
	case existing.Open():
		return reviewSkip(action.ReasonReviewPending, map[string]any{"head_sha": head, "item": existing.ID})
	default:
		return reviewSkip(action.ReasonAlreadyHandled, map[string]any{"head_sha": head, "item": existing.ID})
	}

	if force {
		actions = append(actions, reviewConsume(v, head)...)
	}
	return decide.Result{Actions: actions}
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

func reviewCreate(in decide.Input, head string, forced bool) action.Action {
	v := in.View
	parent := action.AnchorParent
	if a, ok := in.Items.Anchor(); ok {
		parent = a.ID
	}
	md := reviewItemMetadata(v, head)
	md["dedup_key"] = workitem.DedupKey(workitem.EntityRefFrom(v), workitem.KindReviewPR, workitem.Context{})
	return action.Action{
		Op:   action.OpCreate,
		Kind: string(workitem.KindReviewPR),
		Fields: action.Fields{
			Title:     "review-pr: " + v.Snapshot.Repo + "#" + strconv.Itoa(v.Snapshot.Number),
			IssueType: workitem.ContractFor(workitem.KindReviewPR).IssueType,
			Parent:    parent,
			Metadata:  md,
		},
		Facts: map[string]any{"head_sha": head, "forced": forced},
	}
}

func reviewReopen(it workitem.Item, head string, forced bool) action.Action {
	target := it.ID
	return action.Action{
		Op:     action.OpReopen,
		Kind:   string(workitem.KindReviewPR),
		Target: &target,
		Facts:  map[string]any{"head_sha": head, "reviewed_head_sha": it.Metadata["head_sha"], "forced": forced},
	}
}

func reviewUpdate(it workitem.Item, v *view.View, head string, forced bool) action.Action {
	target := it.ID
	return action.Action{
		Op:     action.OpUpdate,
		Kind:   string(workitem.KindReviewPR),
		Target: &target,
		Fields: action.Fields{Metadata: reviewItemMetadata(v, head)},
		Facts:  map[string]any{"head_sha": head, "reviewed_head_sha": it.Metadata["head_sha"], "forced": forced},
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
