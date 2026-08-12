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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("tool %q: got %s, want approve", tool, got.Decision)
		}
	}
}

func TestMCP_ReadOnlyVerbs_Approve(t *testing.T) {
	readOnly := []string{
		// Jira (Atlassian) — camelCase, verb leads
		"mcp__Atlassian-MCP-Server__searchJiraIssuesUsingJql",
		"mcp__Atlassian-MCP-Server__getTransitionsForJiraIssue", // "transitions" != mutating "transition"
		"mcp__Atlassian-MCP-Server__getVisibleJiraProjects",
		"mcp__Atlassian-MCP-Server__getIssueLinkTypes",
		// Notion — BOTH server ids, kebab, verb after server prefix
		"mcp__Notion__notion-search",
		"mcp__claude_ai_Notion__notion-search",
		"mcp__claude_ai_Notion__notion-get-users",
		"mcp__claude_ai_Notion__notion-get-comments", // "comments" != mutating "comment"
		// Slack — snake, verb after server prefix
		"mcp__Slack__slack_search_public",
		"mcp__Slack__slack_read_thread",
		// ArgoCD
		"mcp__ArgoCD__get_application",
		"mcp__ArgoCD__list_applications",
		"mcp__ArgoCD__check_connectivity",
		"mcp__ArgoCD__get_application_resource_tree",
		// Backstage
		"mcp__Backstage__get_component_ownership",
		"mcp__Backstage__get_team_info_and_members",
	}
	r := New()
	for _, tool := range readOnly {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.Approve {
			t.Errorf("read-only MCP tool %q: got %s, want approve", tool, got.Decision)
		}
	}
}

func TestMCP_MutatingVerbs_Abstain(t *testing.T) {
	mutating := []string{
		"mcp__Atlassian-MCP-Server__createJiraIssue",
		"mcp__Atlassian-MCP-Server__transitionJiraIssue",
		"mcp__Atlassian-MCP-Server__addCommentToJiraIssue",
		"mcp__Atlassian-MCP-Server__createIssueLink",
		"mcp__Atlassian-MCP-Server__editJiraIssue",
		"mcp__claude_ai_Notion__notion-update-page",
		"mcp__claude_ai_Notion__notion-create-pages",
		// compound read+mutate must NOT approve (mutating verb wins)
		"mcp__Some-Server__getAndDeleteWidget",
		"mcp__Some-Server__listThenRemoveStale",
	}
	r := New()
	for _, tool := range mutating {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("mutating MCP tool %q: got %s, want abstain", tool, got.Decision)
		}
	}
}

func TestMCP_UnknownMCPTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "mcp__Unknown-Server__unknownAction",
		ToolInput: mustJSON(map[string]string{}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("unknown MCP tool: got %s, want abstain", got.Decision)
	}
}

func TestMCP_NonMCPTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "echo hello"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Bash: got %s, want abstain", got.Decision)
	}
}

func TestMCP_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "mcp" {
		t.Errorf("Name() = %q, want mcp", got)
	}
}
