package asklog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolSummary_Bash(t *testing.T) {
	input := json.RawMessage(`{"command":"git push --force origin main"}`)
	got := ToolSummary("Bash", input)
	if got != "git push --force origin main" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_BashLongCommand(t *testing.T) {
	long := "echo " + string(make([]byte, 200))
	input, _ := json.Marshal(map[string]string{"command": long})
	got := ToolSummary("Bash", json.RawMessage(input))
	if len(got) > 123 {
		t.Errorf("summary too long: %d chars", len(got))
	}
	if got[len(got)-3:] != "..." {
		t.Errorf("long summary should end with ..., got %q", got[len(got)-5:])
	}
}

func TestToolSummary_BashMultiline(t *testing.T) {
	// Display (ToolSummary) now COLLAPSES newlines to spaces instead of cutting
	// at the first line, so a multi-line command no longer masquerades as its
	// (often "cd …") first line. Grouping uses the full normalized command via
	// CommandClass; this collapse is display-only. See bead pg2-okd13.3.
	input := json.RawMessage(`{"command":"line1\nline2\nline3"}`)
	got := ToolSummary("Bash", input)
	if got != "line1 line2 line3" {
		t.Errorf("got %q, want newlines collapsed to a single line", got)
	}
}

// --- CommandClass (grouping-key producer, bead pg2-okd13.3) ---

func mustBashInput(command string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": command})
	return json.RawMessage(b)
}

func TestCommandClass_BashMultilineGroupsByTail(t *testing.T) {
	// A synthetic multi-line compound row must bucket by its real TAIL command,
	// not its first line: "cd foo\nwork" and "cd foo && work" share one key.
	multiline := CommandClass("Bash", mustBashInput("cd foo\nwork"), "")
	compound := CommandClass("Bash", mustBashInput("cd foo && work"), "")
	if multiline != compound {
		t.Fatalf("multiline key %q != compound key %q", multiline, compound)
	}
	// Must not collapse to the (buggy) first-line summary "cd foo".
	firstLineSummary := ToolSummary("Bash", mustBashInput("cd foo"))
	if multiline == firstLineSummary {
		t.Errorf("command class %q must not equal the first-line summary %q", multiline, firstLineSummary)
	}
	// Must reflect the real tail command "work".
	if !strings.Contains(multiline, "work") {
		t.Errorf("command class %q should reflect the real tail command 'work'", multiline)
	}
}

func TestCommandClass_LongDistinctNotCollapsed(t *testing.T) {
	// Two distinct commands sharing a >120-char prefix must not collapse.
	prefix := "echo " + strings.Repeat("a", 150)
	a := CommandClass("Bash", mustBashInput(prefix+" AAA"), "")
	b := CommandClass("Bash", mustBashInput(prefix+" BBB"), "")
	if a == b {
		t.Fatalf("distinct long commands collapsed to the same command class")
	}
}

func TestCommandClass_NonBashUsesSummary(t *testing.T) {
	// Non-Bash tools reuse the (untruncated-for-them) ToolSummary form.
	got := CommandClass("Write", json.RawMessage(`{"file_path":"/src/main.go"}`), "")
	if got != "Write: /src/main.go" {
		t.Errorf("non-Bash command class = %q, want the ToolSummary form", got)
	}
}

func TestToolSummary_Write(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/src/main.go","content":"package main"}`)
	got := ToolSummary("Write", input)
	if got != "Write: /src/main.go" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_Edit(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/src/main.go","old_string":"a","new_string":"b"}`)
	got := ToolSummary("Edit", input)
	if got != "Edit: /src/main.go" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_Read(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/src/main.go"}`)
	got := ToolSummary("Read", input)
	if got != "Read: /src/main.go" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_Delete(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/old/file.txt"}`)
	got := ToolSummary("Delete", input)
	if got != "Delete: /old/file.txt" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_WebFetch(t *testing.T) {
	input := json.RawMessage(`{"url":"https://api.github.com/repos/foo/bar"}`)
	got := ToolSummary("WebFetch", input)
	if got != "WebFetch: https://api.github.com/repos/foo/bar" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_MCP(t *testing.T) {
	input := json.RawMessage(`{"some":"args"}`)
	got := ToolSummary("mcp__github__search_repositories", input)
	if got != "mcp: github__search_repositories" {
		t.Errorf("got %q", got)
	}
}

func TestToolSummary_Unknown(t *testing.T) {
	input := json.RawMessage(`{"key":"val"}`)
	got := ToolSummary("Agent", input)
	if got != "Agent" {
		t.Errorf("got %q", got)
	}
}
