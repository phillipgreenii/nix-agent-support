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
