package corpus

import (
	"testing"
	"time"
)

// TestTranscriptTail_SameMtimeAppend_Refolds is the C3 regression: a transcript
// appended-to WITHOUT its mtime advancing (coarse-grained filesystem mtime, or a
// rapid same-second append) MUST still be re-tailed. The pre-Phase-3 cache-hit
// gate keyed on (path, mtime) alone would wrongly serve the stale snapshot.
func TestTranscriptTail_SameMtimeAppend_Refolds(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Unix(1_776_000_000, 0)
	path := writeTranscript(t, dir, "s.jsonl", fixed,
		userPromptLine("hello"), assistantLine("m", 100, 50))

	tt := newTranscriptTail()
	_, rec1, err := tt.fold(path, fixed, nil)
	if err != nil {
		t.Fatalf("first fold: %v", err)
	}

	// Append another billable assistant line but keep the SAME mtime.
	appendLines(t, path, fixed, assistantLine("m", 200, 70))

	_, rec2, err := tt.fold(path, fixed, nil)
	if err != nil {
		t.Fatalf("second fold: %v", err)
	}
	if len(rec2) <= len(rec1) {
		t.Fatalf("same-mtime append not re-tailed: pricing records %d -> %d (want growth)", len(rec1), len(rec2))
	}
}
