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
	// First-party agent-control / read-only tools. Most have no filesystem or
	// external side effects; EnterWorktree does create a git worktree on disk,
	// but it is agent-initiated, low-risk, and reversible, so it is auto-approved
	// alongside the rest (pg2-9cist; comment corrected in pg2-zu6xj).
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
	// BashOutput retrieves output from a background Bash shell — read-only, no
	// filesystem or external side effects — so it is auto-approved (hook-support
	// parity; BashOutputEvaluator). KillShell is deliberately NOT here: it is
	// gated by the dedicated `killshell` rule (ownership check).
	"BashOutput": true,
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

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	// MCP tools (mcp__*) are handled by the MCP rule module
	if strings.HasPrefix(input.ToolName, "mcp__") {
		return hookio.NotApplicable()
	}
	if abstainTools[input.ToolName] {
		// Not applicable (ADR 0043): the chain must continue. Former Reason,
		// kept because it is the only record of WHY: "claude-tools: " + input.ToolName + " is a user-interaction tool (always abstain)"
		return hookio.NotApplicable()
	}
	if fileTools[input.ToolName] {
		return hookio.NotApplicable()
	}
	if searchTools[input.ToolName] {
		return hookio.NotApplicable()
	}
	if input.ToolName == "Bash" {
		return hookio.NotApplicable()
	}
	if approvedTools[input.ToolName] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "approved Claude tool",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}
