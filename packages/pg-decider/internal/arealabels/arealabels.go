// Package arealabels adds config-driven area labels to a plan's merge-request
// anchor and review-pr / process-feedback child writes (bead pg2-fsrzf; parity
// with pg-desk sync's area_labels, bead pg2-lvoye).
//
// Apply is a post-step over a decided plan: the rules stay pure functions of
// the view, and the vocabulary stays deployment config (config.AreaLabels).
// Semantics, identical to pg-desk sync:
//
//   - A PR's area set is the sorted union of the labels of every rule whose
//     pattern matches the PR title (default) or head branch.
//   - A new anchor is created with the area set. A new review-pr or
//     process-feedback child is created with the area set plus any
//     area-vocabulary label its anchor already carries, so a label an operator
//     put on the anchor flows to its children.
//   - Labels are only ever ADDED. An existing anchor or child gains its missing
//     area labels on its next content write (an update action the rules
//     already emit); no action is ever created just for a label, and no label
//     is ever removed.
//   - With no area_labels configured the plan is returned unchanged.
package arealabels

import (
	"sort"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// Apply returns res with area labels added to the create and update actions it
// already holds. It never adds or removes an action and never mutates res.
func Apply(res action.PlanResult, v *view.View, cfg *config.Config) action.PlanResult {
	if v == nil || cfg == nil || len(cfg.AreaLabels) == 0 || len(res.Actions) == 0 {
		return res
	}
	area := cfg.AreaLabelsFor(v.Snapshot.Title, v.Snapshot.Branch)
	idx := workitem.BuildIndex(v)
	child := childSet(area, cfg, idx)

	want := func(kind string) []string {
		switch workitem.Kind(kind) {
		case workitem.KindAnchor:
			return area
		case workitem.KindProcessFeedback, workitem.KindReviewPR:
			return child
		}
		return nil
	}

	existing := map[string]workitem.Item{}
	for _, k := range []workitem.Kind{workitem.KindAnchor, workitem.KindProcessFeedback, workitem.KindReviewPR} {
		for _, it := range idx.ByKind(k) {
			existing[it.ID] = it
		}
	}

	out := res
	out.Actions = make([]action.Action, len(res.Actions))
	copy(out.Actions, res.Actions)
	updated := map[string]bool{}
	for i, a := range out.Actions {
		labels := want(a.Kind)
		if len(labels) == 0 {
			continue
		}
		switch a.Op {
		case action.OpCreate:
			out.Actions[i].Fields.Labels = merge(a.Fields.Labels, labels)
		case action.OpUpdate:
			if a.Target == nil || updated[*a.Target] {
				continue
			}
			it, ok := existing[*a.Target]
			if !ok {
				continue
			}
			updated[*a.Target] = true
			missing := subtract(labels, it.Labels)
			if len(missing) == 0 {
				continue
			}
			out.Actions[i].Fields.AddLabels = merge(a.Fields.AddLabels, missing)
		}
	}
	return out
}

// childSet is the label set a review-pr / process-feedback child carries: the
// PR's area set plus every area-vocabulary label the anchor already carries.
func childSet(area []string, cfg *config.Config, idx *workitem.Index) []string {
	set := map[string]bool{}
	for _, l := range area {
		set[l] = true
	}
	if anchor, ok := idx.Anchor(); ok {
		for _, l := range cfg.AreaVocabulary() {
			if anchor.HasLabel(l) {
				set[l] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// merge returns base followed by the entries of add not already in base, as a
// fresh slice.
func merge(base, add []string) []string {
	out := append([]string(nil), base...)
	have := map[string]bool{}
	for _, l := range base {
		have[l] = true
	}
	for _, l := range add {
		if !have[l] {
			out = append(out, l)
			have[l] = true
		}
	}
	return out
}

// subtract returns the entries of want absent from have, in want's order.
func subtract(want, have []string) []string {
	seen := map[string]bool{}
	for _, l := range have {
		seen[l] = true
	}
	var out []string
	for _, l := range want {
		if !seen[l] {
			out = append(out, l)
		}
	}
	return out
}
