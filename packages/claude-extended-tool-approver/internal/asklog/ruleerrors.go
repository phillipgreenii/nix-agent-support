package asklog

import (
	"fmt"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/metrics"
)

// RecordRuleErrors persists one window of per-rule GENUINE failures (ADR 0043) beside
// the call they were observed on.
//
// It is the durable half of the sink: internal/metrics counts, this writes. The split
// exists because the counting has to happen deep in the decision path (the engine's
// chokepoint, which must not know about SQLite) while the write has to happen once,
// at the edge, after the decision is made.
//
// An EMPTY snapshot writes nothing. That is deliberate and load-bearing for how the
// table reads: a row means "this rule failed", so the absence of rows for a rule is
// the evidence that it is healthy. Writing zero-count rows would bury the signal in
// one row per rule per call.
//
// Failures here are REPORTED, never propagated into the decision: the hook has
// already decided by the time this runs, and losing an observability row must not be
// able to change or delay a permission verdict.
func RecordRuleErrors(s *Store, input *hookio.HookInput, snapshot []metrics.RuleErrorCount) error {
	if s == nil || len(snapshot) == 0 {
		return nil
	}
	now := nowISO()
	for _, e := range snapshot {
		if _, err := s.db.Exec(
			`
			INSERT INTO rule_errors
				(session_id, cwd, tool_name, rule_name, error_count, error_sample, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			nilIfEmpty(input.SessionID), nilIfEmpty(input.CWD), nilIfEmpty(input.ToolName),
			e.Rule, e.Count, nilIfEmpty(e.Sample), now,
		); err != nil {
			return fmt.Errorf("insert rule_errors for %s: %w", e.Rule, err)
		}
	}
	return nil
}
