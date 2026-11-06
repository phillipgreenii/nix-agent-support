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

// reviewRequested raises for a team PR the operator owes the next step on
// (stored waiting-on is me): a live review request, a review of the operator's
// in progress (the acting reviewer), or a review of the operator's own that a
// push made stale. It reads the stored waiting-on and reason code and
// recomputes nothing; the summary is a variant chosen by the reason code.
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
	if v.OwnPR() {
		return nil, "an own pr"
	}
	if v.WaitingOn() != interpret.WaitingOnMe {
		return nil, "pr is waiting on " + v.waitingOnName()
	}
	summary := "review requested of me"
	switch v.Reason() {
	case interpret.ReasonActingReviewer:
		summary = "my review is in progress"
	case interpret.ReasonStaleReview:
		summary = "new commits since my review"
	}
	return []Candidate{v.candidate(r.Kind(), p.Severity, summary)}, ""
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

// ownNeedsAction raises for an own, open PR the operator owes the next step on
// (stored waiting-on is me) for a reason other than CI or the PR being an
// unfinished draft. pr.own-ci-failing owns CI, so one cause is never reported
// twice. It reads the stored waiting-on and reason code and recomputes nothing.
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
	if v.WaitingOn() != interpret.WaitingOnMe {
		return nil, "pr is waiting on " + v.waitingOnName()
	}
	reason := v.Reason()
	var cause string
	severity := p.Severity
	switch reason {
	case interpret.ReasonCIFailing:
		return nil, "the only cause is CI"
	case interpret.ReasonOwnDraft:
		return nil, "an own draft is not an item"
	case interpret.ReasonBotDisapproved:
		cause = "bot disapproval"
	case interpret.ReasonChangesRequested:
		cause = "changes requested"
	case interpret.ReasonConflict:
		cause = causeConflict
	case interpret.ReasonOpenThread:
		cause = "unresolved review thread"
	case interpret.ReasonApprovedReady:
		// Approved and ready to land: the panel's remaining reason once
		// nothing blocks the PR.
		cause = "approved and ready to land"
		if !p.SeverityConfigured {
			severity = SeverityLow
		}
	default:
		return nil, "no own-PR action for reason " + reason
	}
	c := v.candidate(r.Kind(), severity, "my PR needs action: "+cause)
	// A merge conflict is the entity itself being broken; review feedback and
	// readiness to land are not, so a changes-requested PR is never held back
	// by a stack.
	c.SelfBroken = reason == interpret.ReasonConflict
	return []Candidate{c}, ""
}
