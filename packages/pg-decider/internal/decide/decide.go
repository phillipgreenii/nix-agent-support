package decide

import (
	"fmt"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// Decide turns a composite view into an ordered action list plus the rules
// that produced no action, each with its reason. It is a pure function of the
// view: running it twice on the same view returns identical results.
//
// Precedence is evaluated before any rule runs (design 7.3):
//
//  1. a hidden entity skips every registered rule (reason "hidden"); no rule
//     is evaluated and no action is produced;
//  2. a suppressed kind skips only the rules of that kind (reason
//     "suppressed"); every other rule, including a rule tied to no kind,
//     evaluates normally;
//  3. otherwise the rule runs.
//
// Actions follow rule ordinal, then the order the rule returned them. Both
// lists are non-nil.
func Decide(v *view.View, entityType string) action.PlanResult {
	return DecideWith(v, entityType, nil)
}

// DecideWith is Decide with the decider's configuration handed to every rule
// through Input.Config (focus.item reads bead_id_pattern and
// focus_priority_map from it). The configuration is data: DecideWith stays a
// pure function of the view and that data. A nil cfg is the zero Config.
func DecideWith(v *view.View, entityType string, cfg *config.Config) action.PlanResult {
	rules := RulesFor(entityType)
	res := action.PlanResult{Actions: []action.Action{}, Skipped: []action.Skip{}}
	if v == nil {
		return res
	}

	if v.Annotations.Hidden.Value {
		var facts map[string]any
		if r := v.Annotations.Hidden.Reason; r != nil {
			facts = map[string]any{"reason": *r}
		}
		for _, r := range rules {
			res.Skipped = append(res.Skipped, action.Skip{Rule: r.ID(), Reason: action.ReasonHidden, Facts: cloneFacts(facts)})
		}
		return res
	}

	suppressed := map[workitem.Kind]bool{}
	for _, k := range v.Annotations.Suppress {
		if k != "" {
			suppressed[workitem.Kind(k)] = true
		}
	}

	in := Input{View: v, Items: workitem.BuildIndex(v), EntityType: entityType, Config: cfg}
	var evaluated []Rule
	for _, r := range rules {
		if k := r.Kind(); k != "" && suppressed[k] {
			res.Skipped = append(res.Skipped, action.Skip{
				Rule: r.ID(), Reason: action.ReasonSuppressed, Facts: map[string]any{"kind": string(k)},
			})
			continue
		}
		evaluated = append(evaluated, r)
		out := r.Evaluate(in)
		if len(out.Actions) > 0 {
			for _, a := range out.Actions {
				if a.Rule == "" {
					a.Rule = r.ID()
				}
				res.Actions = append(res.Actions, a)
			}
			continue
		}
		res.Skipped = append(res.Skipped, skipFor(r, out.Skip))
	}

	for _, r := range evaluated {
		if f, ok := r.(Finalizer); ok {
			res.Actions = f.Finalize(in, res.Actions)
		}
	}
	if res.Actions == nil {
		res.Actions = []action.Action{}
	}
	res.Actions = anchorFirst(res.Actions)
	return res
}

// skipFor builds the Skip entry for a rule that produced no action. The
// reason must be one of the five in the closed vocabulary; a rule returning
// anything else is a programming error and panics, so the vocabulary cannot
// silently widen.
func skipFor(r Rule, s *action.Skip) action.Skip {
	if s == nil {
		return action.Skip{Rule: r.ID(), Reason: action.ReasonNotMatched}
	}
	if !validSkipReason(s.Reason) {
		panic(fmt.Sprintf("decide: rule %q returned skip reason %q outside the closed vocabulary %v", r.ID(), s.Reason, SkipReasons()))
	}
	return action.Skip{Rule: r.ID(), Reason: s.Reason, Facts: s.Facts}
}

// anchorFirst guarantees the list invariant of design 7.3: an anchor create
// precedes every action whose parent is the $anchor placeholder. Finalizers
// normally insert it in place; if one appended it, it is moved (once, keeping
// every other action's relative order) to just ahead of the first dependent.
func anchorFirst(actions []action.Action) []action.Action {
	firstDependent := -1
	for i, a := range actions {
		if a.Fields.Parent == action.AnchorParent {
			firstDependent = i
			break
		}
	}
	if firstDependent < 0 {
		return actions
	}
	create := -1
	for i, a := range actions {
		if a.Op == action.OpCreate && a.Kind == string(workitem.KindAnchor) {
			create = i
			break
		}
	}
	if create < 0 || create < firstDependent {
		return actions
	}
	out := make([]action.Action, 0, len(actions))
	out = append(out, actions[:firstDependent]...)
	out = append(out, actions[create])
	out = append(out, actions[firstDependent:create]...)
	out = append(out, actions[create+1:]...)
	return out
}

func cloneFacts(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
