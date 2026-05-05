package asklog

import (
	"encoding/json"
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
	input := json.RawMessage(`{"command":"line1\nline2\nline3"}`)
	got := ToolSummary("Bash", input)
	if got != "line1" {
		t.Errorf("got %q, want first line only", got)
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
