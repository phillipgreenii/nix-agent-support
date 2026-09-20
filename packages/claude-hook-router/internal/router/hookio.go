package router

import "encoding/json"

// HookOutput is the subset of Claude Code's hookSpecificOutput fields this
// router reads from a delegate's stdout and, once merged across the whole
// chain, writes to its own stdout — one JSON object per invocation
// (ADR 0071 §2.7). A zero HookOutput serializes as "{}", the router's own
// abstain/no-opinion shape.
type HookOutput struct {
	// PermissionDecision is PreToolUse/PermissionRequest's decide-contract
	// field (deny/defer/ask/allow).
	PermissionDecision string `json:"permissionDecision,omitempty"`
	// UpdatedInput is PreToolUse's rewrite-contract field.
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	// UpdatedToolOutput is PostToolUse's rewrite-contract field.
	UpdatedToolOutput json.RawMessage `json:"updatedToolOutput,omitempty"`
	// Decision carries PermissionRequest's nested decision.updatedInput
	// rewrite-contract field.
	Decision *DecisionField `json:"decision,omitempty"`
	// AdditionalContext is the annotate-contract field, concatenated across
	// every delegate that set it.
	AdditionalContext string `json:"additionalContext,omitempty"`
	// Retry is PermissionDenied's decide-contract field (its only field).
	Retry *bool `json:"retry,omitempty"`
}

// DecisionField is PermissionRequest's nested decision object. Today this
// router only reads/writes decision.updatedInput — the one rewrite-shaped
// subfield the Contract requires covering; other decision subfields
// (e.g. updatedPermissions) are out of scope for this packet.
type DecisionField struct {
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

// IsZero reports whether o carries no field at all — a delegate's (or the
// router's own) full abstain, {}.
func (o HookOutput) IsZero() bool {
	return o.PermissionDecision == "" &&
		len(o.UpdatedInput) == 0 &&
		len(o.UpdatedToolOutput) == 0 &&
		(o.Decision == nil || len(o.Decision.UpdatedInput) == 0) &&
		o.AdditionalContext == "" &&
		o.Retry == nil
}

// delegateWireOutput is the shape a delegate's stdout is decoded into. A
// delegate registered for this router ALONE (packet B3's own fixtures, e.g.
// tests/fixtures/delegates/decide-ask.sh) emits HookOutput's fields
// unnested/flat at the top level — the router's own established, tested
// convention. A delegate that is ALSO independently registered as a real
// Claude Code hook (e.g. claude-extended-tool-approver) instead follows the
// real Claude Code hook contract unconditionally: its decision nests under a
// top-level "hookSpecificOutput" key (see
// packages/claude-extended-tool-approver/internal/hookio/output.go). Both
// shapes decode in a single json.Unmarshal: the flat fields populate the
// embedded HookOutput directly, while a present "hookSpecificOutput" object
// populates HookSpecificOutput — ADR 0071 §2.4 frames a delegate's
// stdout as expected to be "the expected hookSpecificOutput shape", so both
// are honored (bug tc-6sfia: previously only the flat shape was, and a
// nested delegate's real decision was silently discarded as abstain).
type delegateWireOutput struct {
	HookOutput
	// HookSpecificOutput, when present (even as {}), takes precedence over
	// any flat fields decoded into the embedded HookOutput above — a
	// delegate following the real nested contract does not also duplicate
	// its fields at the top level, but if it somehow did, the nested,
	// contract-accurate value must win.
	HookSpecificOutput *HookOutput `json:"hookSpecificOutput,omitempty"`
}

// decodeHookOutput parses one delegate's raw stdout into the HookOutput the
// dispatch/merge loop operates on, unwrapping a nested "hookSpecificOutput"
// envelope when present (see delegateWireOutput).
func decodeHookOutput(stdout []byte) (HookOutput, error) {
	var wire delegateWireOutput
	// json.Unmarshal already ignores unknown fields by default, so extra
	// fields in either shape (e.g. the nested envelope's own
	// "hookEventName") are handled gracefully here without special-casing
	// (ADR 0071 Binding decisions, "forward-compat").
	if err := json.Unmarshal(stdout, &wire); err != nil {
		return HookOutput{}, err
	}
	if wire.HookSpecificOutput != nil {
		return *wire.HookSpecificOutput, nil
	}
	return wire.HookOutput, nil
}

// permissionRank orders permissionDecision values from least to most
// restrictive (ADR 0071 §2.5: "strict order deny > defer > ask > allow").
// A value absent from this map is not a recognized permissionDecision and
// is ignored by the merge rather than ranked.
var permissionRank = map[string]int{
	"allow": 1,
	"ask":   2,
	"defer": 3,
	"deny":  4,
}
