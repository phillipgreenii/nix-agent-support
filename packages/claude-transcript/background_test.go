package claudetranscript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeBG(t *testing.T, path, body string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestPendingBackgroundAgents(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	const recent = 15 * time.Minute
	turn := func(n string) string {
		return `{"type":"assistant"}` + "\n" + `{"type":"system","subtype":"turn_duration","durationMs":5` + n + `}` + "\n"
	}
	cases := []struct {
		name    string
		body    string
		mainAge time.Duration
		subAge  time.Duration // 0 = no subagent transcript
		want    bool
	}{
		{"pending and subagent recently written", turn(`,"pendingBackgroundAgentCount":1`), time.Hour, time.Minute, true},
		{"pending but subagent silent past the bound", turn(`,"pendingBackgroundAgentCount":1`), time.Hour, 30 * time.Minute, false},
		{"pending, no subagent file, main transcript recent", turn(`,"pendingBackgroundAgentCount":2`), time.Minute, 0, true},
		{"pending, no subagent file, main transcript old", turn(`,"pendingBackgroundAgentCount":1`), time.Hour, 0, false},
		{"no pending field", turn(``), time.Minute, time.Minute, false},
		{"explicit zero", turn(`,"pendingBackgroundAgentCount":0`), time.Minute, time.Minute, false},
		{"last turn_duration wins", turn(`,"pendingBackgroundAgentCount":1`) + turn(``), time.Minute, time.Minute, false},
		{"no turn_duration at all", `{"type":"assistant"}` + "\n", time.Minute, time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "sess.jsonl")
			writeBG(t, p, tc.body, now.Add(-tc.mainAge))
			if tc.subAge > 0 {
				writeBG(t, filepath.Join(filepath.Dir(p), "sess", "subagents", "agent-a.jsonl"), "{}\n", now.Add(-tc.subAge))
			}
			if got := PendingBackgroundAgents(p, now, recent); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	if PendingBackgroundAgents(filepath.Join(t.TempDir(), "missing.jsonl"), now, recent) {
		t.Error("missing transcript must report false")
	}
}
