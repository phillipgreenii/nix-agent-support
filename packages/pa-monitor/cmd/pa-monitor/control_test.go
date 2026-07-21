package main

import (
	"testing"
)

// TestNudgeSuppressionNote pins pg2-65dyf: the CLI nudge must warn (parity with
// the TUI 'N' path) that a freshly-queued nudge will be suppressed until idle
// for any queued session that is Working (session_active) or blocked awaiting a
// human (waiting_for_human). Non-human blocks (usage_limit/error) and idle are
// NOT suppressed by the dispatcher, so they produce no warning.
func TestNudgeSuppressionNote(t *testing.T) {
	status := map[string][2]string{
		"w1":   {"working", ""},
		"w2":   {"working", ""},
		"hi":   {"blocked", "human_input"},
		"ha":   {"blocked", "human_authn"},
		"ul":   {"blocked", "usage_limit"},
		"idle": {"idle", ""},
	}
	statusOf := func(sid string) (string, string) { return status[sid][0], status[sid][1] }

	for _, tc := range []struct {
		name   string
		queued []string
		want   string
	}{
		{"empty", nil, ""},
		{"idle not suppressed", []string{"idle"}, ""},
		{"usage-limit block is not human-suppression", []string{"ul"}, ""},
		{"one working", []string{"w1"}, "note: 1 working — suppressed until idle"},
		{"two working", []string{"w1", "w2"}, "note: 2 working — suppressed until idle"},
		{"human_input waiting", []string{"hi"}, "note: 1 waiting for human — suppressed until idle"},
		{"human_authn waiting", []string{"ha"}, "note: 1 waiting for human — suppressed until idle"},
		{"working + waiting combined", []string{"w1", "hi", "ha", "idle", "ul"}, "note: 1 working, 2 waiting for human — suppressed until idle"},
	} {
		if got := nudgeSuppressionNote(tc.queued, statusOf); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

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
