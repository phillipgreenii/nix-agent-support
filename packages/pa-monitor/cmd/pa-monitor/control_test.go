package main

import (
	"testing"
)

// TestCLINudgeUsesNudgeQueue verifies that parseNudgeFlags returns the
// expected selector (and no cancel flag) when called with a plain selector
// argument — matching the NudgeQueue code path.
func TestCLINudgeUsesNudgeQueue(t *testing.T) {
	f, err := parseNudgeFlags([]string{"session:sid-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.selector != "session:sid-1" {
		t.Errorf("selector: got %q, want %q", f.selector, "session:sid-1")
	}
	if f.cancel {
		t.Error("cancel should be false when --cancel flag is absent")
	}
	if f.text != "" {
		t.Errorf("text: got %q, want empty", f.text)
	}
}

// TestCLINudgeCancelFlag verifies that --cancel sets the cancel flag,
// routing runNudge to the NudgeCancel RPC path.
func TestCLINudgeCancelFlag(t *testing.T) {
	f, err := parseNudgeFlags([]string{"session:sid-1", "--cancel"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.selector != "session:sid-1" {
		t.Errorf("selector: got %q, want %q", f.selector, "session:sid-1")
	}
	if !f.cancel {
		t.Error("cancel should be true when --cancel flag is present")
	}
}

func TestParseNudgeFlagsTextFlag(t *testing.T) {
	f, err := parseNudgeFlags([]string{"path:/some/dir", "--text=hello world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.selector != "path:/some/dir" {
		t.Errorf("selector: got %q, want %q", f.selector, "path:/some/dir")
	}
	if f.text != "hello world" {
		t.Errorf("text: got %q, want %q", f.text, "hello world")
	}
	if f.cancel {
		t.Error("cancel should be false")
	}
}

func TestParseNudgeFlagsMissingSelector(t *testing.T) {
	_, err := parseNudgeFlags([]string{})
	if err == nil {
		t.Fatal("expected error for missing selector, got nil")
	}
}

func TestParseNudgeFlagsAllFlags(t *testing.T) {
	f, err := parseNudgeFlags([]string{"cmux:ws-abc", "--text=ping", "--cancel"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.selector != "cmux:ws-abc" {
		t.Errorf("selector: got %q, want %q", f.selector, "cmux:ws-abc")
	}
	if f.text != "ping" {
		t.Errorf("text: got %q, want %q", f.text, "ping")
	}
	if !f.cancel {
		t.Error("cancel should be true")
	}
}
