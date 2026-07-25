package hookio

import (
	"strings"
	"testing"
)

func TestParseInput_BashToolInput(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/tmp"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", got.ToolName)
	}
	if got.CWD != "/tmp" {
		t.Errorf("CWD = %q, want /tmp", got.CWD)
	}
	cmd, err := got.BashCommand()
	if err != nil {
		t.Fatalf("BashCommand: %v", err)
	}
	if cmd != "git status" {
		t.Errorf("BashCommand = %q, want git status", cmd)
	}
}

func TestParseInput_WriteToolInput(t *testing.T) {
	input := `{"tool_name":"Write","tool_input":{"file_path":"/project/foo.txt","content":"hello"},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.ToolName != "Write" {
		t.Errorf("ToolName = %q, want Write", got.ToolName)
	}
	fp, err := got.FilePath()
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	if fp != "/project/foo.txt" {
		t.Errorf("FilePath = %q, want /project/foo.txt", fp)
	}
}

func TestParseInput_InvalidJSON(t *testing.T) {
	input := `{invalid json}`
	_, err := ParseInput(strings.NewReader(input))
	if err == nil {
		t.Error("ParseInput: expected error for invalid JSON")
	}
}

func TestParseInput_EmptyInput(t *testing.T) {
	input := ``
	_, err := ParseInput(strings.NewReader(input))
	if err == nil {
		t.Error("ParseInput: expected error for empty input")
	}
}

func TestBashCommand_NonBashTool(t *testing.T) {
	input := `{"tool_name":"Write","tool_input":{"file_path":"x"},"cwd":"/tmp"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	_, err = got.BashCommand()
	if err == nil {
		t.Error("BashCommand: expected error for non-Bash tool")
	}
}

func TestFilePath_NonFileTool(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	_, err = got.FilePath()
	if err == nil {
		t.Error("FilePath: expected error for non-file tool")
	}
}

func TestParseInput_MultiEditToolInput(t *testing.T) {
	input := `{"tool_name":"MultiEdit","tool_input":{"file_path":"/project/bar.go","edits":[{"old_string":"foo","new_string":"bar"}]},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.ToolName != "MultiEdit" {
		t.Errorf("ToolName = %q, want MultiEdit", got.ToolName)
	}
	fp, err := got.FilePath()
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	if fp != "/project/bar.go" {
		t.Errorf("FilePath = %q, want /project/bar.go", fp)
	}
}

func TestParseInput_DeleteToolInput(t *testing.T) {
	input := `{"tool_name":"Delete","tool_input":{"file_path":"/project/obsolete.txt"},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.ToolName != "Delete" {
		t.Errorf("ToolName = %q, want Delete", got.ToolName)
	}
	fp, err := got.FilePath()
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	if fp != "/project/obsolete.txt" {
		t.Errorf("FilePath = %q, want /project/obsolete.txt", fp)
	}
}

func TestParseInput_AgentFields(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp","session_id":"abc123","agent_id":"agent-xyz","agent_type":"Explore"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.AgentID != "agent-xyz" {
		t.Errorf("AgentID = %q, want agent-xyz", got.AgentID)
	}
	if got.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", got.AgentType)
	}
}

func TestParseInput_NoAgentFields(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp","session_id":"abc123"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.AgentID != "" {
		t.Errorf("AgentID = %q, want empty", got.AgentID)
	}
	if got.AgentType != "" {
		t.Errorf("AgentType = %q, want empty", got.AgentType)
	}
}

func TestParseInput_HookContextFields(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp","session_id":"abc123",` +
		`"permission_mode":"acceptEdits","prompt_id":"prompt-42","transcript_path":"/x/t.jsonl",` +
		`"tool_response":{"stdout":"ok","is_error":false}}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.PermissionMode != "acceptEdits" {
		t.Errorf("PermissionMode = %q, want acceptEdits", got.PermissionMode)
	}
	if got.PromptID != "prompt-42" {
		t.Errorf("PromptID = %q, want prompt-42", got.PromptID)
	}
	if got.TranscriptPath != "/x/t.jsonl" {
		t.Errorf("TranscriptPath = %q, want /x/t.jsonl", got.TranscriptPath)
	}
	if string(got.ToolResponse) != `{"stdout":"ok","is_error":false}` {
		t.Errorf("ToolResponse = %q, want the raw tool_response object", string(got.ToolResponse))
	}
}

func TestParseInput_NoHookContextFields(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp","session_id":"abc123"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	if got.PromptID != "" {
		t.Errorf("PromptID = %q, want empty", got.PromptID)
	}
	if got.TranscriptPath != "" {
		t.Errorf("TranscriptPath = %q, want empty", got.TranscriptPath)
	}
	if got.ToolResponse != nil {
		t.Errorf("ToolResponse = %q, want nil", string(got.ToolResponse))
	}
}

func TestSearchPath_GlobWithPath(t *testing.T) {
	input := `{"tool_name":"Glob","tool_input":{"pattern":"**/*.go","path":"/project/src"},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	path, err := got.SearchPath()
	if err != nil {
		t.Fatalf("SearchPath: %v", err)
	}
	if path != "/project/src" {
		t.Errorf("SearchPath = %q, want /project/src", path)
	}
}

func TestSearchPath_GlobWithoutPath(t *testing.T) {
	input := `{"tool_name":"Glob","tool_input":{"pattern":"**/*.go"},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	path, err := got.SearchPath()
	if err != nil {
		t.Fatalf("SearchPath: %v", err)
	}
	if path != "" {
		t.Errorf("SearchPath = %q, want empty (no explicit path)", path)
	}
}

func TestSearchPath_GrepWithPath(t *testing.T) {
	input := `{"tool_name":"Grep","tool_input":{"pattern":"TODO","path":"/project/src"},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	path, err := got.SearchPath()
	if err != nil {
		t.Fatalf("SearchPath: %v", err)
	}
	if path != "/project/src" {
		t.Errorf("SearchPath = %q, want /project/src", path)
	}
}

func TestSearchPath_GrepWithoutPath(t *testing.T) {
	input := `{"tool_name":"Grep","tool_input":{"pattern":"TODO"},"cwd":"/project"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	path, err := got.SearchPath()
	if err != nil {
		t.Fatalf("SearchPath: %v", err)
	}
	if path != "" {
		t.Errorf("SearchPath = %q, want empty (no explicit path)", path)
	}
}

func TestSearchPath_NonSearchTool(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp"}`
	got, err := ParseInput(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInput: %v", err)
	}
	_, err = got.SearchPath()
	if err == nil {
		t.Error("SearchPath: expected error for non-search tool")
	}
}
