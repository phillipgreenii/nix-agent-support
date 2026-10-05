package main

import "testing"

// TestNewRootCmd_SubcommandsRegisteredOnce guards against registering the
// same top-level subcommand twice in newRootCmd (bead pg2-w2g9l: newAlertCmd
// was added twice, which made "alert" appear twice in `pg-connector --help`).
func TestNewRootCmd_SubcommandsRegisteredOnce(t *testing.T) {
	seen := map[string]int{}
	for _, c := range newRootCmd().Commands() {
		seen[c.Name()]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("top-level subcommand %q registered %d times, want exactly 1", name, n)
		}
	}
	if seen["alert"] != 1 {
		t.Errorf("alert subcommand registered %d times, want 1", seen["alert"])
	}
}
