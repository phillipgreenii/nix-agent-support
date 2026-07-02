package poller

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMaxActivity_IgnoresStatusSibling is the ADR 0021 §2 guard: maxActivity scans
// only "<transcriptDir>/<sid>/subagents/agent-*.jsonl", so a status-line
// rate_limits sibling — whether sitting beside the transcript or (defensively)
// inside the subagents dir under a non-agent name — must NOT be picked up as
// subagent activity and inflate the freshness/age computation.
func TestMaxActivity_IgnoresStatusSibling(t *testing.T) {
	dir := t.TempDir()
	sid := "sess-1"
	mainPath := filepath.Join(dir, sid+".jsonl")
	mainMTime := time.Now().Add(-1 * time.Hour)
	if err := os.WriteFile(mainPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A status sibling next to the transcript, freshly written (newest by mtime).
	statusPath := filepath.Join(dir, sid+".status.jsonl")
	if err := os.WriteFile(statusPath, []byte(`{"ts":1700000000}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(statusPath, now, now); err != nil {
		t.Fatal(err)
	}

	// A status sibling inside the subagents dir under a NON-agent name (defensive:
	// maxActivity requires an agent- prefix, so this must be ignored too).
	subDir := filepath.Join(dir, sid, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subStatus := filepath.Join(subDir, sid+".status.jsonl")
	if err := os.WriteFile(subStatus, []byte(`{"ts":1700000001}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(subStatus, now, now); err != nil {
		t.Fatal(err)
	}

	got := maxActivity(mainMTime, mainPath)
	if !got.Equal(mainMTime) {
		t.Errorf("maxActivity = %v, want the main mtime %v (status siblings must be ignored)", got, mainMTime)
	}
}
