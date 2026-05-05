package mcp

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestMCP_AllowedTools_Approve(t *testing.T) {
	allowed := []string{
		"mcp__Atlassian-MCP-Server__atlassianUserInfo",
		"mcp__Atlassian-MCP-Server__getAccessibleAtlassianResources",
		"mcp__Atlassian-MCP-Server__getJiraIssue",
		"mcp__Atlassian-MCP-Server__search",
		"mcp__Notion__notion-fetch",
	}
	r := New()
	for _, tool := range allowed {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("tool %q: got %s, want approve", tool, got.Decision)
		}
	}
}

func TestMCP_UnknownMCPTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "mcp__Unknown-Server__unknownAction",
		ToolInput: mustJSON(map[string]string{}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("unknown MCP tool: got %s, want abstain", got.Decision)
	}
}

func TestMCP_NonMCPTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "echo hello"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Bash: got %s, want abstain", got.Decision)
	}
}

func TestMCP_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "mcp" {
		t.Errorf("Name() = %q, want mcp", got)
	}
}
