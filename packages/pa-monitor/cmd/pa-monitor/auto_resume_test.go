package main

import "testing"

// TestCLIAutoResumeOn verifies that parseAutoResumeArgs returns "on" for the
// "on" argument, confirming the SetAutoResume(true) code path.
func TestCLIAutoResumeOn(t *testing.T) {
	action, err := parseAutoResumeArgs([]string{"on"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "on" {
		t.Errorf("action: got %q, want %q", action, "on")
	}
}

// TestCLIAutoResumeOff verifies that parseAutoResumeArgs returns "off".
func TestCLIAutoResumeOff(t *testing.T) {
	action, err := parseAutoResumeArgs([]string{"off"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "off" {
		t.Errorf("action: got %q, want %q", action, "off")
	}
}

// TestCLIAutoResumeToggle verifies that parseAutoResumeArgs returns "toggle",
// confirming the code path that reads GetState first then inverts the flag.
func TestCLIAutoResumeToggle(t *testing.T) {
	action, err := parseAutoResumeArgs([]string{"toggle"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "toggle" {
		t.Errorf("action: got %q, want %q", action, "toggle")
	}
}

// TestCLIAutoResumeDefaultToggle verifies that no argument defaults to "toggle".
func TestCLIAutoResumeDefaultToggle(t *testing.T) {
	action, err := parseAutoResumeArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "toggle" {
		t.Errorf("action: got %q, want %q", action, "toggle")
	}
}

// TestCLIAutoResumeInvalidAction verifies that an unknown action returns an error.
func TestCLIAutoResumeInvalidAction(t *testing.T) {
	_, err := parseAutoResumeArgs([]string{"maybe"})
	if err == nil {
		t.Fatal("expected error for invalid action, got nil")
	}
}
