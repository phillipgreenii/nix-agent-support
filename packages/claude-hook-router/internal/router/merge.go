package router

import (
	"encoding/json"
	"strings"
)

// mergeState accumulates the ADR 0071 §2.5 merge policy across one event's
// dispatch chain, in dispatch order. It is deliberately additive-only: a
// mid-chain failure never rolls back a field a prior delegate already
// contributed (the "partial-chain failure" semantics — ADR 0071 §4 Phase B,
// B1 "Partial-chain failure semantics").
type mergeState struct {
	havePermission     bool
	permissionDecision string
	permissionRank     int

	haveUpdatedInput bool
	updatedInput     json.RawMessage

	haveUpdatedToolOutput bool
	updatedToolOutput     json.RawMessage

	haveDecisionInput bool
	decisionInput     json.RawMessage

	additionalContext []string

	haveRetry bool
	retry     bool
}

// apply folds one applied delegate call's HookOutput into the running merge
// state. contract is accepted for symmetry with the dispatch loop and
// future contract-scoped validation, though today every field here is
// already contract-appropriate by construction (packet A1's generator
// rejects a delegate whose declared contract doesn't match what its event
// supports — ADR 0071 §2.4).
func (m *mergeState) apply(contract string, out HookOutput) {
	_ = contract

	if rank, ok := permissionRank[out.PermissionDecision]; ok {
		if !m.havePermission || rank > m.permissionRank {
			m.havePermission = true
			m.permissionRank = rank
			m.permissionDecision = out.PermissionDecision
		}
	}

	if len(out.UpdatedInput) > 0 {
		m.haveUpdatedInput = true
		m.updatedInput = out.UpdatedInput
	}

	if len(out.UpdatedToolOutput) > 0 {
		m.haveUpdatedToolOutput = true
		m.updatedToolOutput = out.UpdatedToolOutput
	}

	if out.Decision != nil && len(out.Decision.UpdatedInput) > 0 {
		m.haveDecisionInput = true
		m.decisionInput = out.Decision.UpdatedInput
	}

	if out.AdditionalContext != "" {
		m.additionalContext = append(m.additionalContext, out.AdditionalContext)
	}

	if out.Retry != nil && *out.Retry {
		m.haveRetry = true
		m.retry = true
	}
}

// result renders the accumulated merge state as the router's own final
// hookSpecificOutput. A mergeState with nothing accumulated renders as the
// zero HookOutput ({}), the router's own fail-safe/no-opinion abstain.
func (m *mergeState) result() HookOutput {
	var o HookOutput
	if m.havePermission {
		o.PermissionDecision = m.permissionDecision
	}
	if m.haveUpdatedInput {
		o.UpdatedInput = m.updatedInput
	}
	if m.haveUpdatedToolOutput {
		o.UpdatedToolOutput = m.updatedToolOutput
	}
	if m.haveDecisionInput {
		o.Decision = &DecisionField{UpdatedInput: m.decisionInput}
	}
	if len(m.additionalContext) > 0 {
		o.AdditionalContext = strings.Join(m.additionalContext, "\n")
	}
	if m.haveRetry {
		v := m.retry
		o.Retry = &v
	}
	return o
}
