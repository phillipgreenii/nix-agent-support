package rules

import (
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalThreadActivity, threadActivityRule{})
}

// threadActivityExcerptMax is the longest excerpt (in characters) an
// annotation carries.
const threadActivityExcerptMax = 200

// threadActivityRule is thread-activity: for each new thread message the PR
// view shows, from an accepted source, on a thread whose parent link names
// this PR, and which the PR has not yet annotated, it proposes exactly one
// annotate action. It is annotate-only: it creates no work item, closes
// nothing and never opens or reopens a review item. A pure function of the
// view and the configured source list; it knows nothing about any source
// beyond the record's source label. It ships in plan mode: ApplyThreadActivityMode
// holds its actions back from apply until the operator flips thread_activity_mode.
type threadActivityRule struct{}

func (threadActivityRule) ID() string          { return action.ThreadActivityRuleID }
func (threadActivityRule) Kind() workitem.Kind { return "" }

func (threadActivityRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if v == nil || len(v.ThreadChanges) == 0 {
		return anchorSkip(action.ReasonNotMatched, nil)
	}
	if skip, terminal := anchorTerminalSkip(v); terminal {
		return skip
	}
	accepted := map[string]bool{}
	for _, s := range in.Config.ThreadActivitySourceList() {
		accepted[s] = true
	}
	annotated := v.Annotations.Decider[action.ThreadActivityDecider]

	var actions []action.Action
	seen := map[string]bool{}
	var unknown []int
	handled, other := 0, 0
	for _, rec := range v.ThreadChanges {
		if !rec.KnownVersion() {
			unknown = append(unknown, rec.Version)
			continue
		}
		if !accepted[rec.Source] || rec.Parent.Kind != "pr" || rec.Parent.ID != v.ID {
			other++
			continue
		}
		vk := action.ThreadActivityViewKey(rec.ThreadID, rec.MessageID)
		if _, done := annotated[vk]; done || seen[vk] {
			handled++
			continue
		}
		seen[vk] = true
		key := action.ThreadActivityKey(rec.ThreadID, rec.MessageID)
		val := action.ThreadActivityValue{
			ThreadID: rec.ThreadID, MessageID: rec.MessageID, Author: rec.Author,
			Timestamp: rec.Timestamp, Excerpt: threadActivityExcerpt(rec.Excerpt),
		}.String()
		actions = append(actions, action.Action{
			Op: action.OpAnnotate, Target: &key, Fields: action.Fields{Value: &val},
			Facts: threadActivityFacts(rec, unknown),
		})
	}
	if len(actions) > 0 {
		if len(unknown) > 0 { // facts built before a later unknown record: refresh them
			for i := range actions {
				actions[i].Facts["unknown_versions"] = intsToAny(unknown)
			}
		}
		return decide.Result{Actions: actions}
	}
	facts := map[string]any{}
	if len(unknown) > 0 {
		facts["unknown_versions"] = intsToAny(unknown)
	}
	reason := action.ReasonNotMatched
	if handled > 0 {
		reason = action.ReasonAlreadyHandled
		facts["annotated"] = handled
	}
	if len(facts) == 0 {
		facts = nil
	}
	return decide.Result{Skip: &action.Skip{Reason: reason, Facts: facts}}
}

func threadActivityFacts(rec view.ThreadChange, unknown []int) map[string]any {
	f := map[string]any{"thread_id": rec.ThreadID, "message_id": rec.MessageID, "source": rec.Source}
	if len(unknown) > 0 {
		f["unknown_versions"] = intsToAny(unknown)
	}
	return f
}

func intsToAny(in []int) []any {
	out := make([]any, len(in))
	for i, n := range in {
		out[i] = n
	}
	return out
}

// threadActivityExcerpt flattens whitespace and cuts the text to
// threadActivityExcerptMax characters.
func threadActivityExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= threadActivityExcerptMax {
		return s
	}
	r := []rune(s)
	return string(r[:threadActivityExcerptMax])
}

// HoldPlanOnlyRules removes the actions of the rules that ship in plan mode
// from res, so apply writes none of them, and records one skip per held rule
// (reason "not matched", facts naming the mode and how many actions were held
// back). Plan does not call it: plan lists the proposed actions. Today the
// only such rule is thread-activity, held until thread_activity_mode is
// "apply".
func HoldPlanOnlyRules(res action.PlanResult, cfg *config.Config) action.PlanResult {
	if cfg.ThreadActivityAppliesWrites() {
		return res
	}
	held := 0
	kept := make([]action.Action, 0, len(res.Actions))
	for _, a := range res.Actions {
		if a.Rule == action.ThreadActivityRuleID {
			held++
			continue
		}
		kept = append(kept, a)
	}
	if held == 0 {
		return res
	}
	res.Actions = kept
	res.Skipped = append(append([]action.Skip(nil), res.Skipped...), action.Skip{
		Rule: action.ThreadActivityRuleID, Reason: action.ReasonNotMatched,
		Facts: map[string]any{"mode": config.ThreadActivityModePlan, "held": held},
	})
	return res
}
