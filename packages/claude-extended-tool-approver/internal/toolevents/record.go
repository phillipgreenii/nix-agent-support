package toolevents

import (
	"encoding/json"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/metrics"
)

// RecordPreToolUse builds and appends the ceta.* events for one decided
// PreToolUse call: ceta.verdict always, ceta.trace when the engine computed a
// trace, ceta.rule_errors when the snapshot is non-empty, and ceta.command_class
// for Bash. verdict is the engine's verdict, delivered the one actually sent
// (they differ when an unattended session turns an ask into a reject).
//
// It returns the write error for the caller to report; it never panics on a
// bad session id or an unwritable directory, and the caller MUST NOT let the
// error change the verdict.
func RecordPreToolUse(input *hookio.HookInput, verdict, delivered hookio.RuleResult, sandboxEnabled bool, ruleErrors []metrics.RuleErrorCount) error {
	return Append(PreToolUseEvents(input, verdict, delivered, sandboxEnabled, ruleErrors)...)
}

// PreToolUseEvents is RecordPreToolUse without the write.
func PreToolUseEvents(input *hookio.HookInput, verdict, delivered hookio.RuleResult, sandboxEnabled bool, ruleErrors []metrics.RuleErrorCount) []Event {
	mk := func(kind string, data map[string]any) Event {
		return Event{Kind: kind, SessionID: input.SessionID, ToolUseID: input.ToolUseID, AgentID: input.AgentID, Data: data}
	}
	vd := map[string]any{
		"verdict":         verdict.Decision.String(),
		"delivered":       delivered.Decision.String(),
		"sandbox_enabled": sandboxEnabled,
	}
	if delivered.Reason != "" {
		vd["reason"] = delivered.Reason
	}
	if verdict.Module != "" {
		vd["rule"] = verdict.Module
	}
	evs := []Event{mk(KindVerdict, vd)}

	if len(verdict.Trace) > 0 {
		entries := make([]any, 0, len(verdict.Trace))
		for _, t := range verdict.Trace {
			entries = append(entries, map[string]any{
				"rule": t.RuleName, "decision": t.Decision.String(), "reason": t.Reason,
			})
		}
		evs = append(evs, mk(KindTrace, map[string]any{"trace": map[string]any{"entries": entries}}))
	}
	if len(ruleErrors) > 0 {
		errs := make(map[string]any, len(ruleErrors))
		for _, e := range ruleErrors {
			entry := map[string]any{"count": e.Count}
			if e.Sample != "" {
				entry["sample"] = e.Sample
			}
			errs[e.Rule] = entry
		}
		evs = append(evs, mk(KindRuleErrors, map[string]any{"errors": errs}))
	}
	if input.ToolName == "Bash" {
		evs = append(evs, mk(KindCommandClass, map[string]any{
			"class": asklog.CommandClass(input.ToolName, json.RawMessage(input.ToolInput), input.CWD),
		}))
	}
	return evs
}
