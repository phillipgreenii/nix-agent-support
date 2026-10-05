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

// The anchor group (entity-change-flow design 7.3): all.closed, all.reopened,
// anchor.lazy, anchor.backfill, anchor.priority (priority.go) and adoption
// (adoption.go). The anchor always mirrors the PR: an open PR has an open
// anchor, a merged or closed PR has a closed anchor and closed children,
// however the anchor was closed, a person included. No rule here reads or
// records who closed a work item, and every rule is a pure function of the
// view: no tracker call, no clock.
//
// None of these rules is tied to one suppressible work kind, so Kind() is ""
// for all of them: suppressing a kind never stops the anchor from mirroring
// the PR. Every helper in this group is prefixed "anchor" because sibling
// rule packets add files to this package.

const (
	anchorRuleClosed   = "all.closed"
	anchorRuleReopened = "all.reopened"
	anchorRuleLazy     = "anchor.lazy"
	anchorRuleBackfill = "anchor.backfill"
	anchorRulePriority = "anchor.priority"
	anchorRuleAdoption = "adoption"
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalAllClosed, anchorClosedRule{})
	decide.Register(decide.EntityTypePR, decide.OrdinalAllReopened, anchorReopenedRule{})
	decide.Register(decide.EntityTypePR, decide.OrdinalAnchorLazy, anchorLazyRule{})
	decide.Register(decide.EntityTypePR, decide.OrdinalAnchorBackfill, anchorBackfillRule{})
	decide.Register(decide.EntityTypePR, decide.OrdinalAnchorPriority, anchorPriorityRule{})
	decide.Register(decide.EntityTypePR, decide.OrdinalAdoption, anchorAdoptionRule{})
}

// PR state as the anchor group reads it.
const (
	anchorPRStateOpen   = "open"
	anchorPRStateClosed = "closed"
	anchorPRStateMerged = "merged"
)

// anchorPRState normalizes the snapshot to "open", "closed" or "merged"; ""
// when the snapshot reports none of them (such a view matches no rule that
// needs to know).
func anchorPRState(s view.PRSnapshot) string {
	switch {
	case s.Merged || strings.EqualFold(s.State, anchorPRStateMerged):
		return anchorPRStateMerged
	case strings.EqualFold(s.State, anchorPRStateClosed):
		return anchorPRStateClosed
	case strings.EqualFold(s.State, anchorPRStateOpen):
		return anchorPRStateOpen
	}
	return ""
}

// anchorTerminalSkip is the liveness gate every work-creating rule and
// land.ready applies before anything else: a merged or closed PR is dead, so
// those rules skip it (not matched, carrying the pr_state fact) rather than
// create, reopen or update work that all.closed would close on the next run.
// It reports false for an open PR and for a snapshot that reports no state.
// Only the anchor group (all.closed, all.reopened, anchor.*, adoption) keeps
// evaluating a terminal PR, because mirroring and closing the anchor is their
// job. The design (7.3) gates by hidden and suppressed kind centrally and
// leaves every other condition to the rule, so this is a shared per-rule
// guard rather than a new precedence step; anchor.priority already gates the
// same way.
func anchorTerminalSkip(v *view.View) (decide.Result, bool) {
	st := anchorPRState(v.Snapshot)
	if st != anchorPRStateClosed && st != anchorPRStateMerged {
		return decide.Result{}, false
	}
	return anchorSkip(action.ReasonNotMatched, map[string]any{"pr_state": st}), true
}

func anchorSkip(reason string, facts map[string]any) decide.Result {
	return decide.Result{Skip: &action.Skip{Reason: reason, Facts: facts}}
}

func anchorTarget(id string) *string { return &id }

// ---- all.closed -------------------------------------------------------

// anchorClosedRule is all.closed (same as current; subscribes to closed,
// merged): a merged or closed PR closes the anchor and every open direct
// child of it. The cascade is type-blind: a child nobody classified (an
// improvised "Human: ..." bead filed under the anchor) is closed too. Children
// close before the anchor, so a retry after a partial failure still finds the
// anchor to hang the remaining closes on.
type anchorClosedRule struct{}

func (anchorClosedRule) ID() string          { return anchorRuleClosed }
func (anchorClosedRule) Kind() workitem.Kind { return "" }

func (anchorClosedRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	st := anchorPRState(v.Snapshot)
	if st != anchorPRStateClosed && st != anchorPRStateMerged {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"pr_state": st})
	}
	anchor, ok := in.Items.Anchor()
	if !ok {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"pr_state": st, "anchor": false})
	}

	kinds := map[string]workitem.Kind{}
	for _, k := range workitem.Kinds() {
		for _, it := range in.Items.ByKind(k) {
			kinds[it.ID] = k
		}
	}
	facts := func() map[string]any { return map[string]any{"pr_state": st} }
	reason := "PR " + st

	var acts []action.Action
	for _, l := range v.Links {
		if !anchorIsOpenChild(l, anchor.ID) {
			continue
		}
		acts = append(acts, action.Action{
			Op: action.OpClose, Kind: string(kinds[l.ID]), Target: anchorTarget(l.ID),
			Fields: action.Fields{Description: reason}, Facts: facts(),
		})
	}
	if anchor.Open() {
		acts = append(acts, action.Action{
			Op: action.OpClose, Kind: string(workitem.KindAnchor), Target: anchorTarget(anchor.ID),
			Fields: action.Fields{Description: reason}, Facts: facts(),
		})
	}
	if len(acts) == 0 {
		return anchorSkip(action.ReasonAlreadyHandled, map[string]any{"pr_state": st, "anchor": anchor.ID})
	}
	return decide.Result{Actions: acts}
}

// anchorIsOpenChild reports whether l is an open work item that is a direct
// child of the anchor. The view lists a PR's own work items with relation
// "work"; a link that names another parent in its metadata belongs to that
// parent, not to this anchor.
func anchorIsOpenChild(l view.Link, anchorID string) bool {
	if l.Type != "issue" || l.Relation != "work" || l.ID == anchorID || l.State == "closed" {
		return false
	}
	p := l.Metadata["parent"]
	return p == "" || p == anchorID
}

// ---- all.reopened -----------------------------------------------------

// anchorReopenedRule is all.reopened (differs from current: today the anchor
// is never reopened; subscribes to reopened): an open PR whose anchor is
// closed reopens the anchor, however it was closed, a person included. The
// child rules then re-evaluate against the open anchor. Nothing here looks at
// the anchor's close metadata.
type anchorReopenedRule struct{}

func (anchorReopenedRule) ID() string          { return anchorRuleReopened }
func (anchorReopenedRule) Kind() workitem.Kind { return "" }

func (anchorReopenedRule) Evaluate(in decide.Input) decide.Result {
	st := anchorPRState(in.View.Snapshot)
	if st != anchorPRStateOpen {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"pr_state": st})
	}
	anchor, ok := in.Items.Anchor()
	if !ok {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"anchor": false})
	}
	if anchor.Open() {
		return anchorSkip(action.ReasonAlreadyHandled, map[string]any{"anchor": anchor.ID})
	}
	return decide.Result{Actions: []action.Action{{
		Op: action.OpReopen, Kind: string(workitem.KindAnchor), Target: anchorTarget(anchor.ID),
		Facts: map[string]any{"pr_state": st},
	}}}
}

// ---- anchor.lazy ------------------------------------------------------

// anchorLazyRule is anchor.lazy (same as current; no routing subscription,
// runs inline): when a child is needed and no anchor exists, create the
// anchor (merge-request, title <repo>#<n>: <title>). It is a Finalizer so the
// create lands ahead of the children that name the $anchor placeholder, and
// it is never created eagerly: with no child create in the list, nothing
// happens.
type anchorLazyRule struct{}

func (anchorLazyRule) ID() string          { return anchorRuleLazy }
func (anchorLazyRule) Kind() workitem.Kind { return "" }

// Evaluate never produces an action itself; the work is Finalize's.
func (anchorLazyRule) Evaluate(decide.Input) decide.Result {
	return anchorSkip(action.ReasonNotMatched, nil)
}

func (anchorLazyRule) Finalize(in decide.Input, produced []action.Action) []action.Action {
	first := -1
	needed := map[string]bool{}
	for i, a := range produced {
		if a.Op == action.OpCreate && a.Kind == string(workitem.KindAnchor) {
			return produced // someone already creates it
		}
		if a.Fields.Parent == action.AnchorParent {
			if first < 0 {
				first = i
			}
			needed[a.Rule] = true
		}
	}
	if first < 0 {
		return produced
	}
	if _, ok := in.Items.Anchor(); ok {
		return produced
	}
	create := anchorCreate(in.View, anchorNeededBy(needed))
	out := make([]action.Action, 0, len(produced)+1)
	out = append(out, produced[:first]...)
	out = append(out, create)
	return append(out, produced[first:]...)
}

func anchorNeededBy(rules map[string]bool) []string {
	out := make([]string, 0, len(rules))
	for r := range rules {
		if r != "" {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// anchorCreate builds the anchor create from the PR snapshot. A new anchor
// is born with the conflict-priority nudge already applied (the tracker's
// default priority is the baseline), exactly as the sync it replaces created
// it, so the next run finds the pbase marker in place and does nothing.
func anchorCreate(v *view.View, neededBy []string) action.Action {
	s := v.Snapshot
	c := workitem.ContractFor(workitem.KindAnchor)
	var labels []string
	if v.Decorations.Relationship == "co-owned" {
		labels = append(labels, "co-owned")
	}
	f := action.Fields{
		Title:     s.Repo + "#" + strconv.Itoa(s.Number) + ": " + s.Title,
		IssueType: c.IssueType,
		Metadata:  anchorMirror(s),
	}
	f.Metadata["dedup_key"] = workitem.DedupKey(workitem.EntityRefFrom(v), workitem.KindAnchor, workitem.Context{})
	if mine, ok := anchorActsAsMine(v.Decorations.Relationship); ok && anchorHasConflict(v) {
		add, _, pri, set := anchorPriorityDelta(anchorDefaultPriority, nil, mine, true)
		labels = append(labels, add...)
		if set {
			f.Priority = anchorFormatPriority(pri)
		}
	}
	f.Labels = labels
	return action.Action{
		Op: action.OpCreate, Kind: string(workitem.KindAnchor), Fields: f, Rule: anchorRuleLazy,
		Facts: map[string]any{"needed_by": neededBy},
	}
}

// anchorMirror is the metadata the anchor mirrors from the PR snapshot (the
// contract table's keys other than dedup_key). repo and pr_number are always
// present; the others are present when the snapshot reports a value (draft
// always is).
func anchorMirror(s view.PRSnapshot) map[string]string {
	md := map[string]string{
		"repo":      s.Repo,
		"pr_number": strconv.Itoa(s.Number),
		"draft":     strconv.FormatBool(s.Draft),
	}
	state := anchorPRState(s)
	if state == "" {
		state = strings.ToLower(s.State)
	}
	for k, val := range map[string]string{
		"state": state, "branch": s.Branch, "base": s.Base, "author": s.Author, "url": s.URL,
	} {
		if val != "" {
			md[k] = val
		}
	}
	return md
}

// ---- anchor.backfill --------------------------------------------------

// anchorBackfillRule is anchor.backfill (same as current): an adopted anchor
// lacking repo/pr_number gets them written once. The design's rule table names
// no separate rule for the per-check drift correction of the anchor's
// mirrored metadata (state, draft, branch, base, author, url) that the sync
// it replaces performs on every check of an existing anchor; it is ported
// here, under this rule id, and writes only the values that differ.
type anchorBackfillRule struct{}

func (anchorBackfillRule) ID() string          { return anchorRuleBackfill }
func (anchorBackfillRule) Kind() workitem.Kind { return "" }

func (anchorBackfillRule) Evaluate(in decide.Input) decide.Result {
	anchor, ok := in.Items.Anchor()
	if !ok {
		return anchorSkip(action.ReasonNotMatched, map[string]any{"anchor": false})
	}
	want := anchorMirror(in.View.Snapshot)
	set := map[string]string{}
	backfilled, drifted := []string{}, []string{}
	for k, val := range want {
		have := anchor.Metadata[k]
		if have == val {
			continue
		}
		set[k] = val
		if k == "repo" || k == "pr_number" {
			if have == "" {
				backfilled = append(backfilled, k)
				continue
			}
			// A present-but-different repo/pr_number is a rename or transfer;
			// it is corrected like any other mirrored value.
		}
		drifted = append(drifted, k)
	}
	if len(set) == 0 {
		return anchorSkip(action.ReasonAlreadyHandled, map[string]any{"anchor": anchor.ID})
	}
	sort.Strings(backfilled)
	sort.Strings(drifted)
	return decide.Result{Actions: []action.Action{{
		Op: action.OpUpdate, Kind: string(workitem.KindAnchor), Target: anchorTarget(anchor.ID),
		Fields: action.Fields{Metadata: set},
		Facts:  map[string]any{"backfilled": backfilled, "drifted": drifted},
	}}}
}
