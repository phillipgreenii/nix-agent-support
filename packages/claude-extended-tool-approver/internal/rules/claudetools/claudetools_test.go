package claudetools

import (
	"encoding/json"
	"errors"
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
		// First-party agent-control / read-only tools (pg2-9cist)
		"Monitor", "StructuredOutput", "ScheduleWakeup", "TaskStop", "SendMessage",
		"EnterWorktree", "TaskList", "Workflow", "TaskGet", "ReportFindings",
		// BashOutput is read-only (retrieves background-shell output) — approved.
		"BashOutput",
	}
	r := New()
	for _, tool := range approved {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("tool %q: got %s, want approve", tool, got.Decision)
		}
	}
}

func TestClaudeTools_PlanModeToolsAbstain(t *testing.T) {
	// Plan-mode transitions must NOT be auto-approved — approving their
	// PreToolUse would short-circuit the native plan-review gate (pg2-9cist).
	r := New()
	for _, tool := range []string{"ExitPlanMode", "EnterPlanMode"} {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
			t.Errorf("tool %q: got %s, want abstain", tool, got.Decision)
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Bash: got %s, want abstain (handled by command-specific modules)", got.Decision)
	}
}

func TestClaudeTools_AbstainTools(t *testing.T) {
	deferredTools := []string{"ExitPlanMode"}
	r := New()
	for _, tool := range deferredTools {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		got, err := r.Evaluate(input)
		if hookio.Verdict(got, err).Decision != hookio.NoOpinion {
			t.Errorf("tool %q: got %s, want abstain (user-interaction tool)", tool, got.Decision)
		}
		// This used to assert a non-empty Reason, as the marker of an EXPLICIT
		// deferral rather than an incidental fall-through. ADR 0043's decision 2
		// empties the RuleResult on a not-applicable return, so the explicitness now
		// lives in the channel itself plus the membership below — which is the fact
		// the test is really about: a plan-mode gate MUST NOT be auto-approved.
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("tool %q: err = %v, want ErrNotApplicable (the chain must continue)", tool, err)
		}
		if approvedTools[tool] {
			t.Errorf("tool %q is in approvedTools — auto-approving its PreToolUse short-circuits the "+
				"native plan-review gate (pg2-9cist)", tool)
		}
		if !abstainTools[tool] {
			t.Errorf("tool %q is no longer in abstainTools, so nothing keeps it out of a future "+
				"approve list", tool)
		}
	}
}

func TestClaudeTools_Unknown_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{ToolName: "UnknownTool", ToolInput: mustJSON(map[string]string{})}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("UnknownTool: got %s, want abstain", got.Decision)
	}
}

func TestClaudeTools_WebFetch_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "WebFetch",
		ToolInput: mustJSON(map[string]string{"url": "https://example.com", "prompt": ""}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
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
