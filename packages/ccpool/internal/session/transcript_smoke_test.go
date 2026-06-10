package session

import (
	"testing"

	ct "github.com/phillipgreenii/claude-transcript"
)

func TestClaudeTranscriptImportResolves(t *testing.T) {
	// Compile-time proof the shared module is wired; behavior is tested in its own pkg.
	_, err := ct.LastAssistantText("/nonexistent-on-purpose")
	if err == nil {
		t.Error("expected an error opening a nonexistent transcript")
	}
}
