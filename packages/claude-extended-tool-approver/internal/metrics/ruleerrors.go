// Package metrics is the NAMED SINK for things the decision path observes but does
// not decide on. It exists because ADR 0043 separated "this rule does not apply"
// from "this rule could not determine": once a genuine failure is a distinct
// outcome, something has to be able to notice that one rule is failing
// SYSTEMATICALLY, and before ADR 0043 such failures folded into Abstain and were
// silent.
//
// The ADR names the requirement and forbids the cheap discharge of it: a stderr
// line alone does NOT satisfy it, because the hook is one short-lived process per
// tool call, so a per-process counter can never aggregate. This package therefore
// only ACCUMULATES; durability is the caller's half of the contract
// (asklog.RecordRuleErrors persists a snapshot beside the decision it belongs to).
package metrics

import (
	"sort"
	"sync"
)

// RuleErrorCount is one rule's failure tally within a snapshot window, plus the
// FIRST error text seen for it. First, not last: a systematically-failing resolver
// usually fails the same way every time, and the first sample is the one whose
// timing correlates with whatever changed.
type RuleErrorCount struct {
	Rule   string
	Count  int
	Sample string
}

// RuleErrors accumulates per-rule failure counts. Safe for concurrent use: the
// engine is reachable from recursive evaluation and nothing forbids a future caller
// from evaluating in parallel, and a counting sink that data-races would be worse
// than no sink at all.
type RuleErrors struct {
	mu      sync.Mutex
	counts  map[string]int
	samples map[string]string
}

func NewRuleErrors() *RuleErrors {
	return &RuleErrors{counts: map[string]int{}, samples: map[string]string{}}
}

// DefaultRuleErrors is the process-wide sink the engine uses unless one is
// injected. A package-level default is deliberate: a rule failure MUST be counted
// even on a path that never wired a sink up, so forgetting to inject one degrades
// to "recorded but not persisted" rather than to "silent".
var DefaultRuleErrors = NewRuleErrors()

// Record tallies one failure for rule. A nil err is still counted — the caller
// observed a failure outcome, and dropping it would make the count disagree with
// the decision path.
func (c *RuleErrors) Record(rule string, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[rule]++
	if _, seen := c.samples[rule]; !seen {
		msg := "<nil>"
		if err != nil {
			msg = err.Error()
		}
		c.samples[rule] = msg
	}
}

// Snapshot returns the accumulated tallies ordered by rule name. Ordering is part
// of the contract: the snapshot is persisted and diffed, so an unstable map order
// would make identical windows look different.
func (c *RuleErrors) Snapshot() []RuleErrorCount {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.counts) == 0 {
		return nil
	}
	out := make([]RuleErrorCount, 0, len(c.counts))
	for rule, n := range c.counts {
		out = append(out, RuleErrorCount{Rule: rule, Count: n, Sample: c.samples[rule]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// Total is the number of failures across all rules in the current window.
func (c *RuleErrors) Total() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.counts {
		n += v
	}
	return n
}

// Reset empties the window. Exists for tests and for a long-lived caller that
// flushes periodically; the hook process exits instead.
func (c *RuleErrors) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = map[string]int{}
	c.samples = map[string]string{}
}
