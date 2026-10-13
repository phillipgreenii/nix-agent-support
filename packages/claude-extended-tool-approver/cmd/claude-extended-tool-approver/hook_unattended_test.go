package main

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// unattendedFromEnv is the ONE place the process environment decides that a session
// is an autonomous worker. It must honor exactly the value ccpool injects ("1"), so a
// stray CCPOOL_AUTONOMOUS=0 or a truthy-looking spelling never silently converts an
// attended session's Asks into denials.
func TestUnattendedFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"unset", "", false, false},
		{"empty", "", true, false},
		{"injected by ccpool new --autonomous", "1", true, true},
		{"explicit zero", "0", true, false},
		{"truthy word is not the contract", "true", true, false},
		{"whitespace is not the contract", " 1", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Always set (even to "") so the ambient value of the machine that runs
			// the test can never leak in; TestMain scrubs the family as well.
			t.Setenv(hookio.AutonomousEnvVar, "")
			if tt.set {
				t.Setenv(hookio.AutonomousEnvVar, tt.value)
			}
			if got := unattendedFromEnv(); got != tt.want {
				t.Errorf("unattendedFromEnv() with %s=%q = %v, want %v", hookio.AutonomousEnvVar, tt.value, got, tt.want)
			}
		})
	}
}
