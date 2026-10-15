package keeper

import (
	"os"
	"testing"
)

// TestExternalProbeRealPmset runs the real /usr/bin/pmset (read-only) and
// checks the probe parses what this macOS prints. Every entry must be a valid
// holder; the list may legitimately be empty on a quiet machine.
func TestExternalProbeRealPmset(t *testing.T) {
	if _, err := os.Stat(PmsetPath); err != nil {
		t.Skipf("%s not available: %v", PmsetPath, err)
	}
	got := ExternalProbe{}.Probe(0)
	if got == nil {
		t.Fatal("Probe returned nil, want a non-nil list")
	}
	for _, h := range got {
		if h.PID <= 0 || h.Process == "" || h.Type == "" || h.AgeS < 0 {
			t.Errorf("malformed holder %+v", h)
		}
	}
	t.Logf("real pmset reported %d holder(s)", len(got))
}
