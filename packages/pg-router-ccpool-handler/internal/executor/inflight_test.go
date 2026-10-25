package executor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// InFlightToolCall (pg2-r4ig2): the unit-level contract behind waitDone's end
// test. mtime is the last write; now is expressed as an age past it.
func TestInFlightToolCall(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	cases := []struct {
		name  string
		lines func(t *testing.T) []string
		age   time.Duration
		want  bool
	}{
		{"unanswered, inside declared timeout", func(t *testing.T) []string { return []string{bashUse(t, "a", 400000)} }, 399 * time.Second, true},
		{"unanswered, inside timeout plus grace", func(t *testing.T) []string { return []string{bashUse(t, "a", 400000)} }, 400*time.Second + inflightGrace - time.Second, true},
		{"unanswered, timeout plus grace elapsed", func(t *testing.T) []string { return []string{bashUse(t, "a", 400000)} }, 400*time.Second + inflightGrace, false},
		{"answered", func(t *testing.T) []string { return []string{bashUse(t, "a", 400000), resultFor(t, "a")} }, time.Second, false},
		{"default timeout is 120s", func(t *testing.T) []string { return []string{bashUse(t, "a", 0)} }, 119 * time.Second, true},
		{"default timeout elapsed", func(t *testing.T) []string { return []string{bashUse(t, "a", 0)} }, 120*time.Second + inflightGrace, false},
		{"timeout is capped at 600s", func(t *testing.T) []string { return []string{bashUse(t, "a", 9_000_000)} }, 600*time.Second + inflightGrace, false},
		{"one of two parallel calls unanswered", func(t *testing.T) []string {
			return []string{bashUse(t, "a", 300000), bashUse(t, "b", 300000), resultFor(t, "a")}
		}, 10 * time.Second, true},
		{"a result for an earlier call does not answer a later one", func(t *testing.T) []string {
			return []string{bashUse(t, "a", 300000), resultFor(t, "a"), bashUse(t, "b", 300000)}
		}, 10 * time.Second, true},
		{"non-Bash tool_use is not counted", func(t *testing.T) []string {
			return []string{transcriptLine(t, "assistant", map[string]any{"type": "tool_use", "id": "a", "name": "Read", "input": map[string]any{}})}
		}, 10 * time.Second, false},
		{"garbage lines are skipped", func(t *testing.T) []string { return []string{"not json", bashUse(t, "a", 300000), "{"} }, 10 * time.Second, true},
		{"a non-numeric timeout reads as the default", func(t *testing.T) []string {
			return []string{transcriptLine(t, "assistant", map[string]any{"type": "tool_use", "id": "a", "name": "Bash", "input": map[string]any{"timeout": "400000"}})}
		}, 119 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := sessionTree(t, mtime, tc.lines(t)...)
			if got := InFlightToolCall(tr, mtime.Add(tc.age)); got != tc.want {
				t.Errorf("InFlightToolCall at +%v = %v, want %v", tc.age, got, tc.want)
			}
		})
	}
}

// The session's own transcript counts too, not only subagents'.
func TestInFlightToolCall_sessionTranscript(t *testing.T) {
	mtime := time.Unix(1_000_000, 0)
	tr := sessionTree(t, mtime, resultFor(t, "none"))
	if err := os.WriteFile(tr, []byte(bashUse(t, "a", 300000)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tr, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if !InFlightToolCall(tr, mtime.Add(time.Minute)) {
		t.Error("an unanswered call in the session's own transcript is in flight")
	}
}

func TestInFlightToolCall_missingTranscript(t *testing.T) {
	if InFlightToolCall(filepath.Join(t.TempDir(), "nope.jsonl"), time.Now()) {
		t.Error("a missing transcript has no call in flight")
	}
}
