// Package route is Tier 3 of the mistake census (bead pg2-oisvb): it decides which
// ARTIFACT can actually fix each confirmed finding, ranks the findings, and renders
// them as one report.
//
// # Why routing is a table and not a judgement call
//
// The manual audit this tooling replaces did this by hand and did it
// inconsistently: seven findings went to instruction fixes, six to the
// approver/hook layer, two were stale — and the split had to be redone because the
// main-loop / subagent attribution was not available, so main-loop and subagent
// failures had been conflated. A rule written into the always-on user rules does
// NOT reliably reach a subagent, which is precisely how the subagent-dominated
// classes survived being "fixed". `is_sidechain` is what decides that row, and it
// is now in the index, so the decision is mechanical.
//
// # Exactly one route, always
//
// Every finding carries exactly one Route, including not-actionable. Nothing is
// emitted unrouted, because an unrouted finding is a finding nobody owns: it reads
// as work while belonging to no artifact, survives every review, and is still there
// next census. Where a finding genuinely needs a second artifact — an evenly split
// class needs both the user rules and the subagent brief — the primary route is
// still one value and the second is stated in AlsoNote.
package route

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
	"github.com/phillipgreenii/pg-ccaudit/internal/classify"
)

// Route is the artifact that can fix a finding.
type Route string

const (
	// RouteGlobalRule is ~/.claude/CLAUDE.md — the always-apply rules. The only
	// place a rule reaches `claude -p` workers as well as interactive sessions.
	RouteGlobalRule Route = "global-rule"
	// RouteWorkspaceRule is the repository's own CLAUDE.md.
	RouteWorkspaceRule Route = "workspace-rule"
	// RouteSkill is a procedure the agent follows once it knows to.
	RouteSkill Route = "skill"
	// RouteSlashCommand is a repeated multi-step invocation.
	RouteSlashCommand Route = "slash-command"
	// RouteSubagentPrompt is that subagent's brief or prompt template — the route
	// is_sidechain decides.
	RouteSubagentPrompt Route = "subagent-prompt-template"
	// RouteHook is mechanical enforcement: the agent cannot get it wrong.
	RouteHook Route = "hook"
	// RoutePermissionConfig is the approver rule set or allowlist.
	RoutePermissionConfig Route = "permission-config"
	// RouteNotActionable is an explicit close: infra flake, user preference, or a
	// feature working as designed.
	RouteNotActionable Route = "not-actionable"
)

// Routes is every route, in reporting order.
var Routes = []Route{
	RouteHook,
	RoutePermissionConfig,
	RouteGlobalRule,
	RouteSubagentPrompt,
	RouteWorkspaceRule,
	RouteSkill,
	RouteSlashCommand,
	RouteNotActionable,
}

// Preventability is how reliably a fix in this artifact actually prevents a
// recurrence. It is a WEIGHT in the ranking, so the numbers and their reasons are
// stated here rather than chosen at the call site:
//
//	hook                     1.00  mechanical; the agent cannot forget it
//	permission-config        0.90  mechanical, but a rule written too narrowly still misses
//	global-rule              0.60  always-on and reaches claude -p workers, but compliance is not enforced
//	subagent-prompt-template 0.60  reaches the subagent reliably; same compliance caveat
//	workspace-rule           0.50  same caveat, and only loaded inside that repository
//	skill                    0.40  only helps when the skill actually triggers
//	slash-command            0.40  only helps when someone invokes it
//	not-actionable           0.00  by definition
//
// The ordering is the load-bearing part, not the exact values: mechanical
// enforcement outranks instruction, always-on instruction outranks scoped
// instruction, and scoped instruction outranks opt-in tooling.
func Preventability(r Route) float64 {
	switch r {
	case RouteHook:
		return 1.00
	case RoutePermissionConfig:
		return 0.90
	case RouteGlobalRule, RouteSubagentPrompt:
		return 0.60
	case RouteWorkspaceRule:
		return 0.50
	case RouteSkill, RouteSlashCommand:
		return 0.40
	default:
		return 0.00
	}
}

// SubagentDominant is the share of occurrences at which a finding is treated as a
// subagent problem. 0.6 rather than 0.5 so a near-even split is reported as even
// (and gets an AlsoNote) instead of being routed on one occurrence's worth of
// margin.
const SubagentDominant = 0.6

// Kind separates the two halves of the census inside ONE ranked list.
type Kind string

const (
	// KindMistake is a Tier 1 candidate the semantic pass confirmed.
	KindMistake Kind = "mistake"
	// KindCommandFailure is an errored tool call — the half the previous census
	// could see. It is ranked in the SAME list, not a second one: two disjoint
	// lists are what let the cheap half dominate attention purely by being easier
	// to find.
	KindCommandFailure Kind = "command-failure"
)

// Finding is one ranked, routed group of occurrences sharing a signature.
type Finding struct {
	Kind      Kind   `json:"kind"`
	Signature string `json:"signature"`
	// Class is the Tier 2 class for a mistake finding; empty for a command failure,
	// which needs no semantic judgement to be a failure.
	Class  classify.Class `json:"class,omitempty"`
	Signal string         `json:"signal,omitempty"`

	Occurrences int `json:"occurrences"`
	Sessions    int `json:"sessions"`
	MainLoop    int `json:"main_loop"`
	Subagent    int `json:"subagent"`
	// WorstSession is the runaway discount: a signature firing 40 times inside ONE
	// session is one agent stuck in a loop, not a systemic problem.
	WorstSession int `json:"worst_session"`

	// CostMS is MEASURED wall time, never estimated — the span between the work and
	// the evidence for a mistake, and tool_use-to-tool_result elapsed for a command
	// failure. Zero means no span was measurable, not that it was free.
	CostMS int64 `json:"cost_ms"`

	// FirstSeen and LastSeen are what make "the rule we shipped worked" a testable
	// claim instead of an assumption (criterion 10).
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`

	Route      Route  `json:"route"`
	AlsoNote   string `json:"also_note,omitempty"`
	Prevention string `json:"prevention,omitempty"`
	// RouteHint is what the classifier suggested. Recorded next to the Route the
	// table actually chose, so a reader can see when the two disagreed instead of
	// having to trust that the hint was considered.
	RouteHint  string `json:"route_hint,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	Confidence string `json:"confidence,omitempty"`

	// Score is the ranking value. The formula is printed in the report header so a
	// reader can re-derive any row's position.
	Score float64 `json:"score"`
}

// Score is frequency x cost x preventability, spelled out.
//
//	score = occurrences  x  (1 + cost_ms/1000)  x  preventability(route)
//
// The `1 +` is explicit and load-bearing. Many real findings have no measurable
// span — a denied tool call is a single event, so there are not two timestamps to
// subtract — and multiplying by a bare zero would sink them below every finding
// that merely happened to be slow. Flooring the cost multiplier at 1 makes such a
// finding rank on frequency and preventability, which is all the evidence there is,
// rather than on a fabricated duration. Cost is in SECONDS so that a multi-second
// failure outweighs a millisecond one without a 1000x distortion.
func Score(f Finding) float64 {
	return float64(f.Occurrences) * (1.0 + float64(f.CostMS)/1000.0) * Preventability(f.Route)
}

// SubagentShare is the fraction of occurrences that happened in a subagent.
func (f Finding) SubagentShare() float64 {
	total := f.MainLoop + f.Subagent
	if total == 0 {
		return 0
	}
	return float64(f.Subagent) / float64(total)
}

// Decide picks the ONE route for a finding.
//
// Precedence, and it is precedence rather than a lookup because several rules can
// match one finding at once:
//
//  1. Not a mistake, or nothing an instruction can reach (tooling) — not-actionable.
//     Closed EXPLICITLY rather than dropped, so "we looked and there is nothing to
//     fix" is recorded instead of being indistinguishable from "we missed it".
//  2. The permission layer refused a correct action — permission-config. An
//     instruction cannot fix this and a rule proposed for it is wasted work.
//  3. Verification was skipped — hook. This is the one class where mechanical
//     enforcement is available: a gate that must run can be made to run.
//  4. An instruction CAUSED the error (guidance-defect) — the artifact that carries
//     that instruction. Only the classifier can know which, so its hint is honoured
//     here if it names a valid route; global-rule otherwise, since the always-apply
//     rules and stored memories are where this has actually happened.
//  5. Subagent-dominated — subagent-prompt-template. A rule in the always-on user
//     rules does not reliably reach a subagent.
//  6. Everything else instruction-shaped — global-rule, with the classifier's hint
//     honoured when it names a NARROWER instruction artifact (workspace rule,
//     skill, slash command). Narrowing on a hint is safe; widening is not, so a
//     hint is never allowed to promote a finding to hook or permission-config.
func Decide(f Finding, hint string) (Route, string) {
	switch f.Class {
	case classify.ClassNotAMistake:
		return RouteNotActionable, "no fix: Tier 1 flagged it structurally and the semantic pass found ordinary work"
	case classify.ClassToolingDefect:
		return RouteNotActionable, "infrastructure, not instruction — no artifact in the agent's control fixes it"
	case classify.ClassPermissionFriction:
		return RoutePermissionConfig, ""
	case classify.ClassVerificationMiss:
		return RouteHook, "a gate that must run can be made to run; an instruction to run it can be skipped"
	case classify.ClassGuidanceDefect:
		if r, ok := instructionRoute(hint); ok {
			return r, "routes BACK to the instruction that induced the error, not forward to a new one"
		}
		return RouteGlobalRule, "routes BACK to the instruction that induced the error, not forward to a new one"
	}

	if f.Signal == string(candidate.Denial) {
		return RoutePermissionConfig, ""
	}
	// candidate.HookRefusalBody gets NO shortcut of its own, and the omission is the
	// decision (bead pg2-v150u). Two wrong shortcuts are available and both are
	// tempting. A blanket RouteHook is wrong because the hook ALREADY EXISTS and
	// already fired — that is why there is a refusal to detect — so "add a hook" would
	// rank a solved problem at preventability 1.00. (RouteHook stays REACHABLE for
	// this signal, but only through the ClassVerificationMiss branch above, where Tier
	// 2 has explicitly judged that a gate which must run can be made to run.)
	// RoutePermissionConfig is wrong as a blanket default because the
	// class is mixed: the 2026-07-29 census found the `.git` block firing on a
	// `find . -maxdepth 4 -not -path` command and even on a CETA invocation, which is
	// approver-rule tuning, while the `sleep`-block class is a correct refusal of a
	// reflex an instruction should have prevented — 80 blocks across 80 distinct
	// sessions, one per session. Only Tier 2 can tell those apart, and the taxonomy
	// above already expresses both: ClassPermissionFriction routes the false positives
	// to permission-config, ClassGuidanceDefect routes the instruction cases back to
	// the instruction. Anything unclassified falls through to the subagent-share test
	// and then to global-rule, so the row is still routed — 100 of the 160 measured
	// refusals are in a subagent, which is exactly the split that decides it.
	if f.SubagentShare() >= SubagentDominant {
		return RouteSubagentPrompt, fmt.Sprintf("%d of %d occurrences were in a subagent; a rule in the always-on user rules does not reliably reach one",
			f.Subagent, f.MainLoop+f.Subagent)
	}
	r := RouteGlobalRule
	note := ""
	if hr, ok := instructionRoute(hint); ok {
		r = hr
	}
	if f.Subagent > 0 && f.SubagentShare() < SubagentDominant && f.MainLoop > 0 {
		note = fmt.Sprintf("split %d main-loop / %d subagent — the subagent brief needs the same change",
			f.MainLoop, f.Subagent)
	}
	return r, note
}

// instructionRoute accepts a classifier hint ONLY when it names an instruction
// artifact. A hint may narrow the route; it may never promote a finding to
// mechanical enforcement, because "add a hook" is a decision about what the machine
// forbids and that is not a per-finding judgement a classifier gets to make.
func instructionRoute(hint string) (Route, bool) {
	switch Route(strings.TrimSpace(strings.ToLower(hint))) {
	case RouteWorkspaceRule:
		return RouteWorkspaceRule, true
	case RouteSkill:
		return RouteSkill, true
	case RouteSlashCommand:
		return RouteSlashCommand, true
	case RouteGlobalRule:
		return RouteGlobalRule, true
	case RouteSubagentPrompt:
		return RouteSubagentPrompt, true
	default:
		return "", false
	}
}

// Rank sorts findings by score, descending, with a total order so two runs over the
// same data produce byte-identical reports.
func Rank(fs []Finding) []Finding {
	out := append([]Finding(nil), fs...)
	for i := range out {
		out[i].Score = Score(out[i])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Occurrences != b.Occurrences {
			return a.Occurrences > b.Occurrences
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Signature < b.Signature
	})
	return out
}

// FromClassifications groups confirmed mistakes into findings.
//
// Grouped by (class, signature) rather than by signature alone: the same structural
// shape can be two different problems — a typed turn after a failed Bash call is
// sometimes a correction and sometimes the next instruction — and merging them would
// average two findings into one nobody can act on.
func FromClassifications(cls []classify.Classification) []Finding {
	type key struct {
		class classify.Class
		sig   string
	}
	agg := map[key]*Finding{}
	sessions := map[key]map[string]int{}
	for _, c := range cls {
		// not-a-mistake candidates are NOT dropped. They are grouped and emitted as
		// not-actionable findings, because "we looked at this class and there is
		// nothing to fix" is a RESULT, and a result that is silently absent from the
		// report is indistinguishable from a class nobody examined. Preventability 0
		// puts them last, so they inform without competing for attention. This is the
		// explicit close the Tier 3 taxonomy asks for.
		if c.Candidate.Supplementary {
			// Acknowledgment candidates never carry a finding's weight — they are
			// corroboration for one. Counting them would double-count every mistake
			// the agent happened to admit and under-count every one it did not.
			continue
		}
		k := key{class: c.Class, sig: c.Candidate.Signature}
		f, ok := agg[k]
		if !ok {
			f = &Finding{
				Kind:       KindMistake,
				Signature:  c.Candidate.Signature,
				Class:      c.Class,
				Signal:     string(c.Candidate.Signal),
				Prevention: c.Prevention,
				Evidence:   c.Candidate.Excerpt,
				Confidence: c.Confidence,
				RouteHint:  c.RouteHint,
				FirstSeen:  c.Candidate.TS,
				LastSeen:   c.Candidate.TS,
			}
			agg[k] = f
			sessions[k] = map[string]int{}
		}
		f.Occurrences++
		f.CostMS += c.Candidate.SpanMS
		if c.Candidate.IsSidechain {
			f.Subagent++
		} else {
			f.MainLoop++
		}
		if c.Candidate.SessionID != "" {
			sessions[k][c.Candidate.SessionID]++
		}
		if ts := c.Candidate.TS; ts != "" {
			if f.FirstSeen == "" || ts < f.FirstSeen {
				f.FirstSeen = ts
			}
			if ts > f.LastSeen {
				f.LastSeen = ts
			}
		}
		// Keep the highest-confidence prevention wording, so the finding quotes the
		// classification that was most sure rather than whichever came last.
		if rankConfidence(c.Confidence) > rankConfidence(f.Confidence) && c.Prevention != "" {
			f.Prevention = c.Prevention
			f.Evidence = c.Candidate.Excerpt
			f.Confidence = c.Confidence
			f.RouteHint = c.RouteHint
		}
	}
	out := make([]Finding, 0, len(agg))
	for k, f := range agg {
		f.Sessions = len(sessions[k])
		for _, n := range sessions[k] {
			if n > f.WorstSession {
				f.WorstSession = n
			}
		}
		f.Route, f.AlsoNote = Decide(*f, f.RouteHint)
		out = append(out, *f)
	}
	return out
}

func rankConfidence(c string) int {
	switch c {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}
