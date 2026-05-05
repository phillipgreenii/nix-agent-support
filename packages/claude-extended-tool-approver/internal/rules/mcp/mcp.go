package mcp

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var allowedMCPTools = map[string]bool{
	"mcp__Atlassian-MCP-Server__atlassianUserInfo":              true,
	"mcp__Atlassian-MCP-Server__getAccessibleAtlassianResources": true,
	"mcp__Atlassian-MCP-Server__getJiraIssue":                   true,
	"mcp__Atlassian-MCP-Server__search":                         true,
	"mcp__Notion__notion-fetch":                                 true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "mcp"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if !strings.HasPrefix(input.ToolName, "mcp__") {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if allowedMCPTools[input.ToolName] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "allowed MCP tool",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
