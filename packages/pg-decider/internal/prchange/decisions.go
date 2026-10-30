package prchange

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/metrics"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The decider's rule ids that make up "adoption" in the run record.
var adoptionRules = map[string]bool{"adoption": true, "adoption.node-id": true}

// The outcome words of a decisions-counter series (pg-desk's closed list).
const (
	outcomePlanned = "planned"
	outcomeApplied = "applied"
	outcomeDeduped = "deduped"
	outcomeSkipped = "skipped"
	outcomeFailed  = "failed"
	actionSkip     = "skip"
)

// decision is one series of the decisions counter, with the number of times it
// happened in this run.
type decision struct {
	Rule, Action, Outcome, Reason string
	Count                         int
}

// parsePlanJSON reads the output of `pg-decider plan <type> <id> --json`.
func parsePlanJSON(stdout string) (*action.PlanResult, error) {
	var p action.PlanResult
	if err := json.Unmarshal([]byte(stdout), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// parseCounters finds the decider's run counters line (contract
// pg-decider.run-counters/v1) in its stderr. It is nil when there is none.
func parseCounters(stderr string) *metrics.Line {
	for _, l := range strings.Split(stderr, "\n") {
		if !strings.Contains(l, countersContract) {
			continue
		}
		var line metrics.Line
		if json.Unmarshal([]byte(strings.TrimSpace(l)), &line) == nil && line.Contract == countersContract {
			return &line
		}
	}
	return nil
}

// kindOf is the kind a planned action is counted under: its work-item kind, or
// "annotation" for an annotate action (which has none).
func kindOf(a action.Action) string {
	if a.Kind == "" {
		return "annotation"
	}
	return a.Kind
}

// plannedActions lists the plan's actions (kind and operation) and the count per
// kind.
func plannedActions(p *action.PlanResult) ([]PlannedAction, map[string]int) {
	acts := []PlannedAction{}
	byKind := map[string]int{}
	if p == nil {
		return acts, byKind
	}
	for _, a := range p.Actions {
		k := kindOf(a)
		acts = append(acts, PlannedAction{Rule: a.Rule, Kind: k, Op: string(a.Op)})
		byKind[k]++
	}
	return acts, byKind
}

// decisionsFor builds the decisions-counter series of a run.
//
// Plan mode: every action is a `planned` series under its operation, and every
// skip a `skipped` series under action `skip` with its reason.
//
// Apply mode: the skips are the same. The apply counters say, per rule, how
// many of its actions were applied, deduped, failed or skipped on a dependency;
// the plan read just before the apply says which operation each of the rule's
// actions has. The outcomes are assigned to the rule's actions in plan order
// (applied first, then deduped, failed and skipped); a rule whose actions all
// have one operation, the usual case, is therefore exact, and a rule that mixes
// operations in one run is attributed in that order. With no plan (it could not
// be read) or no counters line there is nothing to attribute, so no action
// series is recorded.
func decisionsFor(mode string, pre *action.PlanResult, counters *metrics.Line) []decision {
	if pre == nil {
		return nil
	}
	sums := map[[4]string]int{}
	add := func(rule, act, outcome, reason string, n int) {
		if n > 0 {
			sums[[4]string{rule, act, outcome, reason}] += n
		}
	}
	for _, s := range pre.Skipped {
		add(s.Rule, actionSkip, outcomeSkipped, s.Reason, 1)
	}
	switch mode {
	case ModePlan:
		for _, a := range pre.Actions {
			add(a.Rule, string(a.Op), outcomePlanned, "", 1)
		}
	case ModeApply:
		if counters == nil {
			break
		}
		ops := map[string][]string{}
		for _, a := range pre.Actions {
			ops[a.Rule] = append(ops[a.Rule], string(a.Op))
		}
		for rule, c := range counters.Rules {
			list := ops[rule]
			if len(list) == 0 {
				continue
			}
			var outcomes []string
			for _, o := range []struct {
				name string
				n    int
			}{{outcomeApplied, c.Applied}, {outcomeDeduped, c.Deduped}, {outcomeFailed, c.Failed}, {outcomeSkipped, c.Skipped}} {
				for i := 0; i < o.n; i++ {
					outcomes = append(outcomes, o.name)
				}
			}
			for i, o := range outcomes {
				op := list[len(list)-1]
				if i < len(list) {
					op = list[i]
				}
				add(rule, op, o, "", 1)
			}
		}
	}
	out := make([]decision, 0, len(sums))
	for k, n := range sums {
		out = append(out, decision{k[0], k[1], k[2], k[3], n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return strings.Join([]string{a.Rule, a.Action, a.Outcome, a.Reason}, "|") < strings.Join([]string{b.Rule, b.Action, b.Outcome, b.Reason}, "|")
	})
	return out
}

// ruleOutcomes is the per-rule outcome and skip reason of the run record.
func ruleOutcomes(mode string, pre *action.PlanResult, counters *metrics.Line) []RuleOutcome {
	out := []RuleOutcome{}
	if pre == nil {
		return out
	}
	for _, s := range pre.Skipped {
		out = append(out, RuleOutcome{Rule: s.Rule, Outcome: outcomeSkipped, Reason: s.Reason, Facts: s.Facts})
	}
	planned := map[string]int{}
	firstFacts := map[string]map[string]any{}
	var order []string
	for _, a := range pre.Actions {
		if planned[a.Rule] == 0 {
			order = append(order, a.Rule)
			firstFacts[a.Rule] = a.Facts
		}
		planned[a.Rule]++
	}
	for _, rule := range order {
		ro := RuleOutcome{Rule: rule, Outcome: outcomePlanned, Facts: firstFacts[rule]}
		if mode == ModeApply && counters != nil {
			if c, ok := counters.Rules[rule]; ok {
				ro.Counts = map[string]int{"planned": c.Planned, "applied": c.Applied, "deduped": c.Deduped, "failed": c.Failed, "skipped": c.Skipped}
				switch {
				case c.Failed > 0:
					ro.Outcome = outcomeFailed
				case c.Applied > 0:
					ro.Outcome = outcomeApplied
				case c.Deduped > 0:
					ro.Outcome = outcomeDeduped
				case c.Skipped > 0:
					ro.Outcome = outcomeSkipped
				}
			}
		}
		out = append(out, ro)
	}
	return out
}

// adoptionOutcome summarizes what the adoption rules did for the PR.
func adoptionOutcome(mode string, v *view.View, pre *action.PlanResult, counters *metrics.Line) *AdoptionRecord {
	ix := workitem.BuildIndex(v)
	adoptable := map[string]string{}
	for _, ad := range ix.Adoptable() {
		adoptable[ad.Item.ID] = ad.MatchedBy
	}
	rec := &AdoptionRecord{Outcome: "none", Candidates: []AnchorCandidate{}}
	for _, it := range ix.ByKind(workitem.KindAnchor) {
		by, isAdoptable := adoptable[it.ID]
		rec.Candidates = append(rec.Candidates, AnchorCandidate{ID: it.ID, State: it.State, Keyed: !isAdoptable, MatchedBy: by})
	}
	if pre == nil {
		return rec
	}
	acted := false
	for _, a := range pre.Actions {
		if adoptionRules[a.Rule] {
			acted = true
		}
	}
	switch {
	case acted && mode == ModePlan:
		rec.Outcome = outcomePlanned
	case acted && counters != nil:
		rec.Outcome = "adopted"
		for rule := range adoptionRules {
			if c, ok := counters.Rules[rule]; ok && c.Failed > 0 {
				rec.Outcome = outcomeFailed
			}
		}
	case acted:
		rec.Outcome = outcomePlanned
	default:
		for _, s := range pre.Skipped {
			if adoptionRules[s.Rule] && s.Facts != nil {
				if _, ok := s.Facts["unadoptable"]; ok {
					rec.Outcome = "unadoptable"
				}
			}
		}
	}
	return rec
}
