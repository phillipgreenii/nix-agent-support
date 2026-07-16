package claudetools

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var approvedTools = map[string]bool{
	"Agent":           true,
	"AskQuestion":     true,
	"AskUserQuestion": true,
	"CronCreate":      true,
	"CronDelete":      true,
	"CronList":        true,
	"ReadLints":       true,
	"SemanticSearch":  true,
	"Skill":           true,
	"SwitchMode":      true,
	"Task":            true,
	"TaskCreate":      true,
	"TaskOutput":      true,
	"TaskUpdate":      true,
	"TodoWrite":       true,
	"ToolSearch":      true,
	"WebSearch":       true,
	// First-party agent-control / read-only tools — inherently safe, no
	// filesystem or external side effects of their own (pg2-9cist).
	"Monitor":          true,
	"StructuredOutput": true,
	"ScheduleWakeup":   true,
	"TaskStop":         true,
	"TaskList":         true,
	"TaskGet":          true,
	"SendMessage":      true,
	"EnterWorktree":    true,
	"Workflow":         true,
	"ReportFindings":   true,
}

var searchTools = map[string]bool{
	"Glob": true, "Grep": true,
}

// abstainTools are tools that the hook should never interfere with.
// These gate plan-mode transitions; auto-approving their PreToolUse would
// short-circuit the native plan-review gate, so they are explicitly excluded
// from the safe-tool allowlist and left to manual/native handling (pg2-9cist).
var abstainTools = map[string]bool{
	"ExitPlanMode":  true,
	"EnterPlanMode": true,
}

var fileTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "MultiEdit": true, "Delete": true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "claude-tools"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	// MCP tools (mcp__*) are handled by the MCP rule module
	if strings.HasPrefix(input.ToolName, "mcp__") {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if abstainTools[input.ToolName] {
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "claude-tools: " + input.ToolName + " is a user-interaction tool (always abstain)",
			Module:   r.Name(),
		}
	}
	if fileTools[input.ToolName] {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if searchTools[input.ToolName] {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if input.ToolName == "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if approvedTools[input.ToolName] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "approved Claude tool",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
