// Package rules holds the concrete pg-decider rules; each registers itself
// into the internal/decide registry from init().
package rules

import (
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const feedbackRuleID = "feedback.digest-changed"

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalFeedbackDigestChanged, feedbackDigestChanged{})
}

// feedbackDigestChanged is the feedback.digest-changed rule (entity-change-flow
// design 7.3): for a mine or co-owned PR it creates a process-feedback cycle
// for unaddressed review comments no earlier cycle covered, and updates an
// open cycle when more feedback arrived. A changed digest alone is not more
// feedback (S26); a team PR never gets a cycle (S16). It reads nothing but
// the view, and never who closed a cycle.
type feedbackDigestChanged struct{}

func (feedbackDigestChanged) ID() string { return feedbackRuleID }

func (feedbackDigestChanged) Kind() workitem.Kind { return workitem.KindProcessFeedback }

func (feedbackDigestChanged) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if skip, terminal := anchorTerminalSkip(v); terminal {
		return skip
	}
	rel := v.Decorations.Relationship
	if rel != "mine" && rel != "co-owned" {
		return feedbackSkip(action.ReasonNotMatched, map[string]any{"relationship": rel})
	}
	unaddressed := feedbackUnaddressed(v.Decorations.Dispositions)
	if len(unaddressed) == 0 {
		return feedbackSkip(action.ReasonNotMatched, map[string]any{"unaddressed": 0})
	}
	digest := fbsumDigest(unaddressed)

	if in.Items.OpenHumanParked(workitem.KindProcessFeedback) {
		return feedbackSkip(action.ReasonAlreadyHandled, map[string]any{"digest": digest, "parked": true})
	}

	covered := in.Items.CoveredComments()
	var uncovered []string
	for _, id := range unaddressed {
		if !covered[id] {
			uncovered = append(uncovered, id)
		}
	}

	cycles := in.Items.ByKind(workitem.KindProcessFeedback)
	var open *workitem.Item
	for i, c := range cycles {
		if !c.Open() {
			continue
		}
		if c.Digest() == digest {
			return feedbackSkip(action.ReasonAlreadyHandled, map[string]any{"digest": digest, "cycle": c.ID})
		}
		if open == nil {
			open = &cycles[i]
		}
	}

	if open != nil {
		if len(uncovered) == 0 {
			return feedbackSkip(action.ReasonAlreadyHandled, map[string]any{"digest": digest, "cycle": open.ID})
		}
		return feedbackUpdate(*open, digest, uncovered)
	}

	// No open cycle: a closed cycle (however closed) whose digest is the
	// current one covers the current set, as does coverage of every comment.
	for _, c := range cycles {
		if c.Digest() == digest {
			return feedbackSkip(action.ReasonAlreadyHandled, map[string]any{"digest": digest, "cycle": c.ID})
		}
	}
	if len(uncovered) == 0 {
		return feedbackSkip(action.ReasonAlreadyHandled, map[string]any{"digest": digest})
	}
	return feedbackCreate(in, digest, unaddressed, uncovered)
}

func feedbackSkip(reason string, facts map[string]any) decide.Result {
	return decide.Result{Skip: &action.Skip{Reason: reason, Facts: facts}}
}

// feedbackUnaddressed returns the sorted ids of comments whose effective
// disposition is open: the override when non-null, else the computed value.
func feedbackUnaddressed(ds []view.Disposition) []string {
	var out []string
	for _, d := range ds {
		eff := d.Computed
		if d.Override != nil {
			eff = *d.Override
		}
		if eff == "open" {
			out = append(out, d.CommentID)
		}
	}
	sort.Strings(out)
	return out
}

func feedbackCreate(in decide.Input, digest string, unaddressed, uncovered []string) decide.Result {
	v := in.View
	ref := workitem.EntityRefFrom(v)
	parent := action.AnchorParent
	if a, ok := in.Items.Anchor(); ok {
		parent = a.ID
	}
	contract := workitem.ContractFor(workitem.KindProcessFeedback)
	title := "process-feedback: " + v.Snapshot.Repo + "#" + strconv.Itoa(v.Snapshot.Number)
	return decide.Result{Actions: []action.Action{{
		Op:   action.OpCreate,
		Kind: string(workitem.KindProcessFeedback),
		Fields: action.Fields{
			Title:       title,
			IssueType:   contract.IssueType,
			Description: fbsumCycleDescription(v.Snapshot.Repo, v.Snapshot.Number, unaddressed),
			Parent:      parent,
			Labels:      []string{"mine", fbsumLabelPrefix + digest},
			Metadata: map[string]string{
				"repo":             v.Snapshot.Repo,
				"pr_number":        strconv.Itoa(v.Snapshot.Number),
				"branch":           v.Snapshot.Branch,
				"covered_comments": strings.Join(unaddressed, ","),
				"dedup_key":        workitem.DedupKey(ref, workitem.KindProcessFeedback, workitem.Context{Digest: digest}),
			},
		},
		Facts: map[string]any{"digest": digest, "unaddressed": unaddressed, "uncovered": uncovered},
	}}}
}

func feedbackUpdate(cycle workitem.Item, digest string, uncovered []string) decide.Result {
	set := map[string]bool{}
	for _, id := range strings.Split(cycle.Metadata["covered_comments"], ",") {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	for _, id := range uncovered {
		set[id] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	target := cycle.ID
	return decide.Result{Actions: []action.Action{{
		Op:     action.OpUpdate,
		Kind:   string(workitem.KindProcessFeedback),
		Target: &target,
		Fields: action.Fields{
			AddLabels:    []string{fbsumLabelPrefix + digest},
			RemoveLabels: fbsumStaleLabels(cycle.Labels, digest),
			Metadata:     map[string]string{"covered_comments": strings.Join(ids, ",")},
		},
		Facts: map[string]any{"digest": digest, "uncovered": uncovered},
	}}}
}
