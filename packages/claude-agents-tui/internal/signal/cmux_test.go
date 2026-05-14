package signal_test

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

// stubEnv returns a LookupEnv whose values come from m.
func stubEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestCmuxDetectReturnsTrueWhenWorkspaceEnvSet(t *testing.T) {
	sig := &signal.CmuxSignaler{LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "ws-123"})}
	if !sig.Detect(1234) {
		t.Error("Detect = false, want true when CMUX_WORKSPACE_ID is set")
	}
}

func TestCmuxDetectReturnsFalseWhenWorkspaceEnvUnset(t *testing.T) {
	sig := &signal.CmuxSignaler{LookupEnv: stubEnv(map[string]string{})}
	if sig.Detect(1234) {
		t.Error("Detect = true, want false when CMUX_WORKSPACE_ID is unset")
	}
}
