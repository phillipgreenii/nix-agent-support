package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	userLine      = `{"type":"user","message":{"role":"user","content":"do it"}}`
	toolUseLine   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`
	toolResult    = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	textLine      = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"May I read the file? mentions tool_use here"}],"stop_reason":"end_turn"}}`
	bookkeepingLn = `{"type":"last-prompt","lastPrompt":"x"}`
)

func TestFSTranscriptState(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"ended on a question", []string{userLine, textLine}, true},
		{"tool call pending", []string{userLine, textLine, toolUseLine}, false},
		{"tool result not yet answered", []string{userLine, toolUseLine, toolResult}, false},
		{"bookkeeping lines are skipped", []string{userLine, textLine, bookkeepingLn, "not json"}, true},
		{"only a user prompt", []string{userLine}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cwd := "/work/my.assistant"
			pd := filepath.Join(dir, "projects", "-work-my-assistant")
			if err := os.MkdirAll(pd, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(pd, "sid.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(c.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mt := time.Date(2026, 11, 20, 9, 41, 0, 0, time.UTC)
			if err := os.Chtimes(path, mt, mt); err != nil {
				t.Fatal(err)
			}
			got, err := FSTranscript{ConfigDir: dir}.State(cwd, "sid")
			if err != nil {
				t.Fatal(err)
			}
			if got.TurnEnded != c.want || !got.ModTime.Equal(mt) {
				t.Errorf("State = %+v, want TurnEnded %v at %v", got, c.want, mt)
			}
		})
	}
}

func TestFSTranscriptMissingAndCutTail(t *testing.T) {
	dir := t.TempDir()
	if _, err := (FSTranscript{ConfigDir: dir}).State("/w", "none"); err == nil {
		t.Error("State of a missing transcript = nil error, want one")
	}
	pd := filepath.Join(dir, "projects", "-w")
	if err := os.MkdirAll(pd, 0o700); err != nil {
		t.Fatal(err)
	}
	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", tailBytes) + `"}}`
	if err := os.WriteFile(filepath.Join(pd, "big.jsonl"), []byte(big+"\n"+textLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FSTranscript{ConfigDir: dir}.State("/w", "big")
	if err != nil || !got.TurnEnded {
		t.Errorf("State = %+v, %v; want the last line read after a cut first line", got, err)
	}
}
