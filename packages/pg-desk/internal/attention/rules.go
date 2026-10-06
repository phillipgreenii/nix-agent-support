package attention

import (
	"fmt"
	"sort"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// Rule is one rule kind (Specification): a predicate over a read-only View of
// one entity. Rules are independent of one another and MUST NOT mutate
// anything.
type Rule interface {
	// Kind is the stable string id, for example "pr.review-requested". It is
	// the key of attention.rules.<kind> in the configuration and of the
	// suppress.<kind> annotation.
	Kind() string
	// DefaultSeverity is the severity used when the configuration sets none.
	DefaultSeverity() Severity
	// Raise returns the candidates the rule finds for v, at severity p.Severity. When
	// it returns none, why says in a short phrase why the rule did not apply
	// (shown by `attention explain`). A rule whose facts are missing or
	// degraded MUST return none (INV-ATTNEVAL-6).
	Raise(v *View, p RuleSettings) (cands []Candidate, why string)
}

var (
	rulesMu  sync.RWMutex
	ruleByID = map[string]Rule{}
)

// Register installs a Rule. It panics on a duplicate kind (like
// classify.Register and database/sql.Register); rule files call it from an
// init().
func Register(r Rule) {
	rulesMu.Lock()
	defer rulesMu.Unlock()
	if _, dup := ruleByID[r.Kind()]; dup {
		panic(fmt.Sprintf("attention: Register called twice for rule kind %q", r.Kind()))
	}
	ruleByID[r.Kind()] = r
}

// registeredRules returns every registered rule ordered by kind.
func registeredRules() []Rule {
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	out := make([]Rule, 0, len(ruleByID))
	for _, r := range ruleByID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind() < out[j].Kind() })
	return out
}

// RuleKinds returns every registered rule kind, sorted.
func RuleKinds() []string {
	rs := registeredRules()
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Kind()
	}
	return out
}

// causeConflict is the pr.own-needs-action cause that means the PR itself is
// broken.
const causeConflict = "merge conflict"

// Rule kinds of the initial rule set.
const (
	KindReviewRequested = "pr.review-requested"
	KindOwnCIFailing    = "pr.own-ci-failing"
	KindOwnNeedsAction  = "pr.own-needs-action"
)

func init() {
	Register(reviewRequested{})
	Register(ownCIFailing{})
	Register(ownNeedsAction{})
}

// reviewRequested raises for a team PR in panel team_awaiting_me: a live
// review request on the operator, not hard-blocked.
type reviewRequested struct{}

func (reviewRequested) Kind() string              { return KindReviewRequested }
func (reviewRequested) DefaultSeverity() Severity { return SeverityMedium }

func (r reviewRequested) Raise(v *View, p RuleSettings) ([]Candidate, string) {
	if v.Type != "pr" {
		return nil, "not a pr"
	}
	if v.Degraded {
		return nil, "interpretation degraded"
	}
	if v.Panel != interpret.PanelTeamAwaitingMe {
		return nil, "pr is not in panel team_awaiting_me"
	}
	return []Candidate{v.candidate(r.Kind(), p.Severity, "review requested of me")}, ""
}

// ownCIFailing raises for an own, open, non-draft PR whose CI rollup state is
// failure after the check_interpreters exclusions. It reads the rollup State,
// not the panel: it does not fire for none or pending, and it does not apply
// the review_exempt_checks softening (that only decides the panels).
type ownCIFailing struct{}

func (ownCIFailing) Kind() string              { return KindOwnCIFailing }
func (ownCIFailing) DefaultSeverity() Severity { return SeverityHigh }

func (r ownCIFailing) Raise(v *View, p RuleSettings) ([]Candidate, string) {
	if v.Type != "pr" {
		return nil, "not a pr"
	}
	if v.Degraded {
		return nil, "interpretation degraded"
	}
	if !v.OwnPR() {
		return nil, "not an own pr"
	}
	f, ok := v.PRFacts()
	if !ok {
		return nil, "stored facts unavailable"
	}
	switch {
	case !f.Open:
		return nil, "pr is not open"
	case f.Draft:
		return nil, "pr is a draft"
	case f.CIState != "failure":
		return nil, "ci rollup state is " + f.CIState
	}
	c := v.candidate(r.Kind(), p.Severity, "CI failing on my PR")
	c.SelfBroken = true
	return []Candidate{c}, ""
}

// ownNeedsAction raises for an own, open PR in panel mine_awaiting_me for a
// cause other than CI. It MUST NOT raise when CI, or a none rollup, is the
// only cause (pr.own-ci-failing owns CI, so one cause is never reported
// twice).
type ownNeedsAction struct{}

func (ownNeedsAction) Kind() string              { return KindOwnNeedsAction }
func (ownNeedsAction) DefaultSeverity() Severity { return SeverityMedium }

func (r ownNeedsAction) Raise(v *View, p RuleSettings) ([]Candidate, string) {
	if v.Type != "pr" {
		return nil, "not a pr"
	}
	if v.Degraded {
		return nil, "interpretation degraded"
	}
	if !v.OwnPR() {
		return nil, "not an own pr"
	}
	if v.Panel != interpret.PanelMineAwaitingMe {
		return nil, "pr is not in panel mine_awaiting_me"
	}
	f, ok := v.PRFacts()
	if !ok {
		return nil, "stored facts unavailable"
	}
	if !f.Open {
		return nil, "pr is not open"
	}
	var causes []string
	if v.Approvals.HumanChangesRequested {
		causes = append(causes, "changes requested")
	}
	if v.Approvals.BotVerdict == interpret.BotVerdictDisapproved {
		causes = append(causes, "bot disapproval")
	}
	if f.Conflict {
		causes = append(causes, causeConflict)
	}
	if f.UnresolvedThread {
		causes = append(causes, "unresolved review thread")
	}
	severity := p.Severity
	if len(causes) == 0 {
		// Approved and ready to land: the panel's remaining reason once
		// nothing blocks the PR. A CI-blocked PR (failure, or a none rollup)
		// is NOT ready, and its only cause is CI, which is not this rule's.
		if f.CIReviewState != "success" && f.CIReviewState != "pending" {
			return nil, "the only cause is CI"
		}
		if !v.Approvals.HumanApproved {
			return nil, "no cause other than CI"
		}
		causes = append(causes, "approved and ready to land")
		if !p.SeverityConfigured {
			severity = SeverityLow
		}
	}
	c := v.candidate(r.Kind(), severity, "my PR needs action: "+joinReasons(causes))
	// A merge conflict is the entity itself being broken; review feedback
	// and readiness to land are not. Only a conflict-only candidate is
	// self-broken, so a changes-requested PR is never held back by a stack.
	c.SelfBroken = len(causes) == 1 && causes[0] == causeConflict
	return []Candidate{c}, ""
}
