package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func valid() *Envelope {
	return &Envelope{
		Schema: Schema, EventID: "e1", TS: 1, Source: "tracker", Kind: "tracker.call_start",
		SessionID: "sess-1", ToolUseID: "tu1", Data: map[string]any{},
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Envelope){
		"schema":       func(e *Envelope) { e.Schema = "x" },
		"event_id":     func(e *Envelope) { e.EventID = "" },
		"ts":           func(e *Envelope) { e.TS = 0 },
		"source dot":   func(e *Envelope) { e.Source = "a.b" },
		"source upper": func(e *Envelope) { e.Source = "Tracker" },
		"kind prefix":  func(e *Envelope) { e.Kind = "ceta.verdict" },
		"kind empty":   func(e *Envelope) { e.Kind = "tracker." },
		"session path": func(e *Envelope) { e.SessionID = "../x" },
		"session dot":  func(e *Envelope) { e.SessionID = "a.b" },
		"tool_use_id":  func(e *Envelope) { e.ToolUseID = "" },
		"data":         func(e *Envelope) { e.Data = nil },
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	for name, mut := range cases {
		e := valid()
		mut(e)
		if e.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
	e := valid()
	e.Kind, e.ToolUseID = "tracker.session_end", ""
	if err := e.Validate(); err != nil {
		t.Errorf("session_end without tool_use_id: %v", err)
	}
}

func TestParseFileName(t *testing.T) {
	s, src, ok := ParseFileName(FileName("abc-1", "ceta"))
	if !ok || s != "abc-1" || src != "ceta" {
		t.Fatalf("got %q %q %v", s, src, ok)
	}
	for _, bad := range []string{"tool-events.a.b.c.jsonl", "tool-events.a.ceta.jsonl.bak", "x.jsonl", "tool-events..ceta.jsonl", "tool-events.a.CETA.jsonl"} {
		if _, _, ok := ParseFileName(bad); ok {
			t.Errorf("%q should not match", bad)
		}
	}
}

func TestEventIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	now := time.Now()
	for range 1000 {
		id := NewEventID(now)
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}

func TestAppendPermsUnderPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(state, "fresh"))
	path, err := Append(valid())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		st, _ := os.Stat(d)
		if st.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", d, st.Mode().Perm())
		}
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", st.Mode().Perm())
	}
}

func TestAppendWritesOneLinePerCall(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for range 3 {
		if _, err := Append(valid()); err != nil {
			t.Fatal(err)
		}
	}
	dir, _ := Dir()
	b, _ := os.ReadFile(filepath.Join(dir, FileName("sess-1", "tracker")))
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	var e Envelope
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || e.Schema != Schema {
		t.Fatalf("bad line: %v %s", err, lines[0])
	}
}

func TestOversizedEventShortenedToOneLine(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e := valid()
	e.Data = map[string]any{
		"tool":  "Write",
		"input": map[string]any{"file_path": "/x", "content": strings.Repeat("é", 200000)},
		"list":  []any{strings.Repeat("a", 90000), "keep"},
	}
	path, err := Append(e)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if len(b) > MaxLineBytes {
		t.Fatalf("line is %d bytes, max %d", len(b), MaxLineBytes)
	}
	if strings.Count(string(b), "\n") != 1 || b[len(b)-1] != '\n' {
		t.Fatalf("want exactly one newline-terminated line")
	}
	var got Envelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("not valid JSON (rune cut?): %v", err)
	}
	if got.Data["_truncated"] == nil || got.Data["tool"] != "Write" {
		t.Errorf("marker/short fields wrong: %v", got.Data["_truncated"])
	}
	if !strings.Contains(string(b), "[truncated ") {
		t.Error("no truncation marker in text")
	}
	if e.Data["tool"] != "Write" || len(e.Data["input"].(map[string]any)["content"].(string)) != 400000 {
		t.Error("caller's data was mutated")
	}
}

func TestShortenDropsUnshortenableData(t *testing.T) {
	data := map[string]any{}
	for i := range 3000 {
		data[strings.Repeat("k", 10)+string(rune('a'+i%26))+strings.Repeat("z", i%50)+string(rune(0x4e00+i))] = "short"
	}
	got := Shorten(data, 1000, 123456)
	if got["_dropped"] != true || got["_truncated"] != 123456 {
		t.Fatalf("got %v", got)
	}
}
