package rules

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// anchor.priority is a port of the conflict-priority nudge pg-desk's
// internal/sync performs (and pg-pr's before it): on the first conflicting
// check the anchor's pre-conflict priority is stashed in a pbase:<n> label and
// the priority is nudged (mine and co-owned toward higher priority, team toward
// lower); a repeated conflicting check does nothing; once the conflict clears
// the baseline is restored and the marker dropped. The design's rule table is
// the single source for rule behavior, so the team lowering stays even though a
// team PR is otherwise left alone.

const (
	// anchorPbasePrefix stashes the pre-conflict priority on the anchor.
	anchorPbasePrefix = "pbase:"
	// anchorDefaultPriority is the tracker's default priority for an item
	// created with none; it seeds the baseline when the view reports no
	// priority for the anchor.
	anchorDefaultPriority = 2
)

var anchorPriorityRE = regexp.MustCompile(`^P?([0-4])$`)

// anchorParsePriority reads "P0".."P4" (or a bare 0-4).
func anchorParsePriority(s string) (int, bool) {
	m := anchorPriorityRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// anchorFormatPriority renders p clamped into [0,4] as "P<n>", the form the
// tracker connector accepts.
func anchorFormatPriority(p int) string {
	if p < 0 {
		p = 0
	}
	if p > 4 {
		p = 4
	}
	return "P" + strconv.Itoa(p)
}

func anchorParsePbase(labels []string) (int, bool) {
	for _, l := range labels {
		if rest, ok := strings.CutPrefix(l, anchorPbasePrefix); ok {
			if n, err := strconv.Atoi(rest); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// anchorNudged returns the conflict-adjusted priority, clamped to [0,4]: mine
// and co-owned raise (toward 0), team lowers (toward 4).
func anchorNudged(p int, actsAsMine bool) int {
	if actsAsMine {
		if p > 0 {
			return p - 1
		}
		return 0
	}
	if p < 4 {
		return p + 1
	}
	return 4
}

// anchorPriorityDelta is the pure decision half of the pbase nudge: given the
// anchor's current priority and labels, whether the PR is acted on as mine
// (mine or co-owned; team otherwise) and whether it currently conflicts, it
// returns the label and priority mutations needed.
func anchorPriorityDelta(cur int, labels []string, actsAsMine, conflict bool) (add, remove []string, priority int, set bool) {
	baseline, hasBaseline := anchorParsePbase(labels)
	switch {
	case conflict && !hasBaseline:
		add = []string{anchorPbasePrefix + strconv.Itoa(cur)}
		if desired := anchorNudged(cur, actsAsMine); desired != cur {
			return add, nil, desired, true
		}
		return add, nil, 0, false
	case !conflict && hasBaseline:
		remove = []string{anchorPbasePrefix + strconv.Itoa(baseline)}
		if cur != baseline {
			return nil, remove, baseline, true
		}
		return nil, remove, 0, false
	}
	return nil, nil, 0, false // a repeated conflicting check, or nothing to undo
}

// anchorActsAsMine maps the relationship to the nudge direction; ok is false
// for a relationship the rule does not know.
func anchorActsAsMine(relationship string) (mine, ok bool) {
	switch relationship {
	case "mine", "co-owned":
		return true, true
	case "team":
		return false, true
	}
	return false, false
}

// anchorHasConflict ports the conflict test of the sync it replaces: either
// mergeability CONFLICTING or merge state DIRTY. UNKNOWN is not a conflict.
// The merge state is not a decoded snapshot member, so it is read from the
// view's raw JSON.
func anchorHasConflict(v *view.View) bool {
	if v.Snapshot.Mergeable == "CONFLICTING" {
		return true
	}
	var raw struct {
		Snapshot struct {
			MergeStateStatus string `json:"merge_state_status"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(v.Raw, &raw) == nil {
		return raw.Snapshot.MergeStateStatus == "DIRTY"
	}
	return false
}

// anchorCurrentPriority is the anchor's current priority. pg-desk's composite
// view reports it as links[].priority (docs/behavior/pg-desk/show.md, omitted
// when the stored snapshot has none); the decoded view.Link has no such
// member, so it is read from the raw link (links[].priority, "P2" or 2), then
// from the link metadata's "priority"; when the view reports none the tracker
// default seeds it.
func anchorCurrentPriority(v *view.View, anchor workitem.Item) int {
	var raw struct {
		Links []struct {
			ID       string          `json:"id"`
			Priority json.RawMessage `json:"priority"`
		} `json:"links"`
	}
	if json.Unmarshal(v.Raw, &raw) == nil {
		for _, l := range raw.Links {
			if l.ID != anchor.ID || len(l.Priority) == 0 {
				continue
			}
			var s string
			if json.Unmarshal(l.Priority, &s) != nil {
				s = strings.TrimSpace(string(l.Priority))
			}
			if p, ok := anchorParsePriority(s); ok {
				return p
			}
		}
	}
	if p, ok := anchorParsePriority(anchor.Metadata["priority"]); ok {
		return p
	}
	return anchorDefaultPriority
}

// anchorPriorityRule is anchor.priority (same as current; subscribes to
// mergeability_changed, reconcile).
type anchorPriorityRule struct{}

func (anchorPriorityRule) ID() string          { return anchorRulePriority }
func (anchorPriorityRule) Kind() workitem.Kind { return "" }

func (anchorPriorityRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if st := anchorPRState(v.Snapshot); st == anchorPRStateClosed || st == anchorPRStateMerged {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"pr_state": st})
	}
	anchor, ok := in.Items.Anchor()
	if !ok {
		// A new anchor is created with the nudge applied (anchor.lazy).
		return anchorSkip(action.ReasonNotMatched, map[string]any{"anchor": false})
	}
	conflict := anchorHasConflict(v)
	mine, known := anchorActsAsMine(v.Decorations.Relationship)
	if conflict && !known {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"relationship": v.Decorations.Relationship})
	}
	cur := anchorCurrentPriority(v, anchor)
	add, remove, pri, set := anchorPriorityDelta(cur, anchor.Labels, mine, conflict)
	if len(add) == 0 && len(remove) == 0 && !set {
		if _, marked := anchorParsePbase(anchor.Labels); marked {
			return anchorSkip(action.ReasonAlreadyHandled, map[string]any{"anchor": anchor.ID, "conflict": conflict})
		}
		return anchorSkip(action.ReasonNotMatched, map[string]any{"conflict": conflict})
	}
	f := action.Fields{AddLabels: add, RemoveLabels: remove}
	facts := map[string]any{"conflict": conflict, "from": anchorFormatPriority(cur)}
	if set {
		f.Priority = anchorFormatPriority(pri)
		facts["to"] = f.Priority
	}
	return decide.Result{Actions: []action.Action{{
		Op: action.OpUpdate, Kind: string(workitem.KindAnchor), Target: anchorTarget(anchor.ID),
		Fields: f, Facts: facts,
	}}}
}
