package router

import "testing"

// TestDecodeHookOutputFlatShape covers the router's own established,
// already-tested convention (packet B3's fixtures, e.g.
// tests/fixtures/delegates/decide-ask.sh): a delegate registered ONLY with
// this router emits HookOutput's fields unnested at the top level.
func TestDecodeHookOutputFlatShape(t *testing.T) {
	out, err := decodeHookOutput([]byte(`{"permissionDecision":"ask"}`))
	if err != nil {
		t.Fatalf("decodeHookOutput: %v", err)
	}
	if out.PermissionDecision != "ask" {
		t.Errorf("PermissionDecision = %q, want %q", out.PermissionDecision, "ask")
	}
}

// TestDecodeHookOutputEmptyFlatShapeIsZero covers the flat full-abstain
// shape, "{}".
func TestDecodeHookOutputEmptyFlatShapeIsZero(t *testing.T) {
	out, err := decodeHookOutput([]byte(`{}`))
	if err != nil {
		t.Fatalf("decodeHookOutput: %v", err)
	}
	if !out.IsZero() {
		t.Errorf("out = %#v, want zero value", out)
	}
}

// TestDecodeHookOutputNestedHookSpecificOutputShape covers tc-6sfia: a
// delegate that is ALSO an independently-registered real Claude Code hook
// (e.g. claude-extended-tool-approver, see
// packages/claude-extended-tool-approver/internal/hookio/output.go)
// unconditionally nests its response under "hookSpecificOutput" per the
// real Claude Code hook contract. Before the fix, json.Unmarshal into the
// flat HookOutput silently dropped this unknown key, leaving a zero-value
// HookOutput (treated as Abstain) even though the delegate made a real
// decision.
func TestDecodeHookOutputNestedHookSpecificOutputShape(t *testing.T) {
	out, err := decodeHookOutput([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"ceta stub"}}`))
	if err != nil {
		t.Fatalf("decodeHookOutput: %v", err)
	}
	if out.PermissionDecision != "allow" {
		t.Errorf("PermissionDecision = %q, want %q (nested envelope must be unwrapped and honored)", out.PermissionDecision, "allow")
	}
	if out.IsZero() {
		t.Errorf("out = %#v, want non-zero (must not be treated as abstain)", out)
	}
}

// TestDecodeHookOutputNestedEmptyObjectIsExplicitAbstain covers a nested
// delegate that explicitly abstains, {"hookSpecificOutput":{}}: it must
// still decode to the zero HookOutput (Abstain), not error.
func TestDecodeHookOutputNestedEmptyObjectIsExplicitAbstain(t *testing.T) {
	out, err := decodeHookOutput([]byte(`{"hookSpecificOutput":{}}`))
	if err != nil {
		t.Fatalf("decodeHookOutput: %v", err)
	}
	if !out.IsZero() {
		t.Errorf("out = %#v, want zero value (explicit nested abstain)", out)
	}
}

// TestDecodeHookOutputNestedWinsOverFlatFields covers the (unlikely, but
// possible) case of a delegate emitting both a nested envelope and stray
// flat fields alongside it: the nested, contract-accurate value must win,
// since a delegate following the real nested contract does not intend the
// flat fields to be read at all.
func TestDecodeHookOutputNestedWinsOverFlatFields(t *testing.T) {
	out, err := decodeHookOutput([]byte(`{"permissionDecision":"deny","hookSpecificOutput":{"permissionDecision":"allow"}}`))
	if err != nil {
		t.Fatalf("decodeHookOutput: %v", err)
	}
	if out.PermissionDecision != "allow" {
		t.Errorf("PermissionDecision = %q, want %q (nested envelope must take precedence)", out.PermissionDecision, "allow")
	}
}

// TestDecodeHookOutputMalformedJSONErrors covers the pre-existing
// malformed-JSON error path is unaffected by the new wrapper decode.
func TestDecodeHookOutputMalformedJSONErrors(t *testing.T) {
	if _, err := decodeHookOutput([]byte(`not json {{{`)); err == nil {
		t.Error("decodeHookOutput: want error for malformed JSON, got nil")
	}
}
