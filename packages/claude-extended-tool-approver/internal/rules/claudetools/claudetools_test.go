package claudetools

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestClaudeTools_ApprovedTools(t *testing.T) {
	approved := []string{
		"Agent", "AskQuestion", "AskUserQuestion", "CronCreate", "CronDelete", "CronList",
		"ReadLints", "SemanticSearch", "Skill", "SwitchMode", "Task",
		"TaskCreate", "TaskOutput", "TaskUpdate", "TodoWrite", "ToolSearch", "WebSearch",
	}
	r := New()
	for _, tool := range approved {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("tool %q: got %s, want approve", tool, got.Decision)
		}
	}
}

func TestClaudeTools_FileTools_Abstain(t *testing.T) {
	fileTools := []string{"Read", "Write", "Edit", "MultiEdit", "Delete"}
	r := New()
	for _, tool := range fileTools {
		input := &hookio.HookInput{
			ToolName:  tool,
			ToolInput: mustJSON(map[string]string{"file_path": "/project/foo.go"}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("tool %q: got %s, want abstain (handled by path-safety)", tool, got.Decision)
		}
	}
}

func TestClaudeTools_Bash_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "echo hello"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Bash: got %s, want abstain (handled by command-specific modules)", got.Decision)
	}
}

func TestClaudeTools_AbstainTools(t *testing.T) {
	abstainTools := []string{"ExitPlanMode"}
	r := New()
	for _, tool := range abstainTools {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("tool %q: got %s, want abstain (user-interaction tool)", tool, got.Decision)
		}
		if got.Reason == "" {
			t.Errorf("tool %q: expected reason to be set for explicit abstain", tool)
		}
	}
}

func TestClaudeTools_Unknown_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{ToolName: "UnknownTool", ToolInput: mustJSON(map[string]string{})}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("UnknownTool: got %s, want abstain", got.Decision)
	}
}

func TestClaudeTools_WebFetch_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "WebFetch",
		ToolInput: mustJSON(map[string]string{"url": "https://example.com", "prompt": ""}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("WebFetch: got %s, want abstain (handled by webfetch rule)", got.Decision)
	}
}

func TestClaudeTools_SearchTools_Abstain(t *testing.T) {
	searchTools := []string{"Glob", "Grep"}
	r := New()
	for _, tool := range searchTools {
		input := &hookio.HookInput{
			ToolName:  tool,
			ToolInput: mustJSON(map[string]string{"pattern": "**/*.go"}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("tool %q: got %s, want abstain (handled by path-safety)", tool, got.Decision)
		}
	}
}

func TestClaudeTools_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "claude-tools" {
		t.Errorf("Name() = %q, want claude-tools", got)
	}
}
