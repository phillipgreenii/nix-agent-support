package hookio

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatOutput_Approve(t *testing.T) {
	result := RuleResult{Decision: Approve, Reason: "ok", Module: "test"}
	got := FormatOutput(result, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("FormatOutput Approve: invalid JSON: %v", err)
	}
	hook := m["hookSpecificOutput"].(map[string]interface{})
	if perm := hook["permissionDecision"].(string); perm != "allow" {
		t.Errorf("permissionDecision = %q, want allow", perm)
	}
	if reason := hook["permissionDecisionReason"].(string); reason != "ok" {
		t.Errorf("permissionDecisionReason = %q, want ok", reason)
	}
	if event := hook["hookEventName"].(string); event != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", event)
	}
	if _, ok := hook["updatedInput"]; ok {
		t.Error("updatedInput should not be present when nil")
	}
}

func TestFormatOutput_Reject(t *testing.T) {
	result := RuleResult{Decision: Reject, Reason: "denied", Module: "test"}
	got := FormatOutput(result, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("FormatOutput Reject: invalid JSON: %v", err)
	}
	hook := m["hookSpecificOutput"].(map[string]interface{})
	if perm := hook["permissionDecision"].(string); perm != "deny" {
		t.Errorf("permissionDecision = %q, want deny", perm)
	}
}

func TestFormatOutput_Ask(t *testing.T) {
	result := RuleResult{Decision: Ask, Reason: "confirm", Module: "test"}
	got := FormatOutput(result, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("FormatOutput Ask: invalid JSON: %v", err)
	}
	hook := m["hookSpecificOutput"].(map[string]interface{})
	if perm := hook["permissionDecision"].(string); perm != "ask" {
		t.Errorf("permissionDecision = %q, want ask", perm)
	}
}

func TestFormatOutput_Abstain(t *testing.T) {
	result := RuleResult{Decision: Abstain}
	got := FormatOutput(result, nil)
	s := strings.TrimSpace(string(got))
	if s != "{}" {
		t.Errorf("FormatOutput Abstain = %q, want {}", s)
	}
}

func TestFormatOutput_WithUpdatedInput(t *testing.T) {
	result := RuleResult{Decision: Approve, Reason: "ok", Module: "test"}
	ui := map[string]interface{}{"command": "rtk git status"}
	got := FormatOutput(result, ui)
	var m map[string]interface{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("FormatOutput with updatedInput: invalid JSON: %v", err)
	}
	hook := m["hookSpecificOutput"].(map[string]interface{})
	updatedInput, ok := hook["updatedInput"].(map[string]interface{})
	if !ok {
		t.Fatal("updatedInput missing from output")
	}
	if cmd := updatedInput["command"].(string); cmd != "rtk git status" {
		t.Errorf("updatedInput.command = %q, want %q", cmd, "rtk git status")
	}
}

func TestFormatOutput_Abstain_IgnoresUpdatedInput(t *testing.T) {
	result := RuleResult{Decision: Abstain}
	ui := map[string]interface{}{"command": "rtk git status"}
	got := FormatOutput(result, ui)
	s := strings.TrimSpace(string(got))
	if s != "{}" {
		t.Errorf("FormatOutput Abstain with updatedInput = %q, want {}", s)
	}
}
