// Package lanestate is pg-desk's persisted PR lane state: the per-PR state, the
// lane-level state and the cumulative counters that the composed command (a
// short-lived process that handles one PR) writes after each run, and that
// `pg-desk serve` aggregates at scrape time. All of it lives in the existing
// meta key and value table (no schema bump, no ladder rung), the same home as
// the change-flow counters.
//
// Key layout (every key starts with "pr_lane."; none of them is the
// configuration key "pr_lane.mode", which keeps the rendered configuration as
// its one source):
//
//	pr_lane.runs.<stage>.<class>                          counter
//	pr_lane.decisions.<rule>|<action>|<outcome>|<reason>  counter
//	pr_lane.marked_inactive                               counter
//	pr_lane.state.mode                                    lane-level mode
//	pr_lane.state.flip_at                                 flip time (RFC 3339)
//	pr_lane.state.last_run_at                             last recorded run
//	pr_lane.state.lookup_items                            decider lookup count
//	pr_lane.state.lookup_ceiling                          decider lookup ceiling
//	pr_lane.pr.<normalized id>                            per-PR state (JSON)
package lanestate

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"

// Key names. KeyPRPrefix is followed by the normalized PR id.
const (
	KeyRunsPrefix      = "pr_lane.runs."
	KeyDecisionsPrefix = "pr_lane.decisions."
	KeyMarkedInactive  = "pr_lane.marked_inactive"
	KeyStateMode       = "pr_lane.state.mode"
	KeyStateFlipAt     = "pr_lane.state.flip_at"
	KeyStateLastRunAt  = "pr_lane.state.last_run_at"
	KeyStateLookupItem = "pr_lane.state.lookup_items"
	KeyStateLookupCeil = "pr_lane.state.lookup_ceiling"
	KeyPRPrefix        = "pr_lane.pr."
	// KeyPrefix is the prefix of every lane key.
	KeyPrefix = "pr_lane."
)

// The run counter's label vocabularies (design 6.1 and 9.2). The class set
// has no retry_exhausted: pg-router owns every retry clock.
var (
	Stages  = []string{"hydrate", "decide"}
	Classes = []string{
		"not_found", "transient", "degraded", "unhydrated",
		"unauthenticated", "hydrate_error", "decide_partial", "decide_failed",
	}
	// Modes are the lane modes, in the order the gauges list them.
	Modes = []string{config.PRLaneModeOff, config.PRLaneModePlan, config.PRLaneModeApply}
)

// The decisions counter's vocabularies. Rules and Reasons are pg-desk's COPY of
// the decider's own closed lists: Rules are the rule ids the decider registers
// (packages/pg-decider/internal/rules, each rule's ID method) and Reasons are
// the skip reasons of docs/behavior/pg-decider/plan-and-apply.md ("Skip
// reasons", constants in packages/pg-decider/internal/action). A drift test
// between this copy and the decider's lists belongs to the end-to-end gate; a
// recorded value outside a list is rejected, which keeps the cardinality
// bounded.
var (
	Rules = []string{
		"adoption", "adoption.node-id", "all.closed", "all.reopened",
		"anchor.backfill", "anchor.lazy", "anchor.priority",
		"conflict.present", "feedback.digest-changed", "fixci.failing-on-head",
		"focus.item", "land.ready", "review.head-advanced",
	}
	Actions  = []string{"create", "update", "reopen", "close", "annotate", "escalate", "skip"}
	Outcomes = []string{"planned", "applied", "deduped", "skipped", "failed"}
	Reasons  = []string{"hidden", "suppressed", "already handled", "review-pending", "not matched"}
)

// ActionSkip is the action value of a rule that produced no action; only it
// carries a reason.
const ActionSkip = "skip"

// Decision is one decisions-counter series.
type Decision struct {
	Rule, Action, Outcome, Reason string
}

// ZeroFilledDecisions is the set of decisions series a scrape emits at 0 before
// any run: for every rule, a skip with each reason (outcome skipped), and each
// other action with each outcome but skipped and no reason. A recorded series
// outside this set (an action with outcome skipped, for example) is still
// exported once it has a count.
func ZeroFilledDecisions() []Decision {
	var out []Decision
	for _, rule := range Rules {
		for _, reason := range Reasons {
			out = append(out, Decision{rule, ActionSkip, "skipped", reason})
		}
		for _, action := range Actions {
			if action == ActionSkip {
				continue
			}
			for _, outcome := range Outcomes {
				if outcome == "skipped" {
					continue
				}
				out = append(out, Decision{rule, action, outcome, ""})
			}
		}
	}
	return out
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
