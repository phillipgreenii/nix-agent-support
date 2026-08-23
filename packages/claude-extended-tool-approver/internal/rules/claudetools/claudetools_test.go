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
		"EnterWorktree", "TaskList", "Workflow", "TaskGet", "ReportFindings", "ListAgents",
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

// TestADR0044_ClaudeTools_PlanModeToolsRefuse is the per-rule half of pg2-qxe85's census for
// claude-tools: its single site now REFUSES.
//
// abstainTools is a DELIBERATE EXCLUSION from the allowlist directly above it (pg2-9cist):
// these tools gate the native plan-review transition, so approving their PreToolUse would
// short-circuit that gate. That is a judgement about the tool, and reporting it as a
// not-applicable made ExitPlanMode indistinguishable from a tool name ceta has never heard
// of — erasing the very record of why the tool is excluded.
//
// A REFUSAL, not a terminal NoOpinion: the chain must still CONTINUE, which is the property
// TestIntegration_KillShellThroughChain's "does not shadow the later path-safety rule" pins
// for this rule's other branches. The floor only demotes a later Approve.
func TestADR0044_ClaudeTools_PlanModeToolsRefuse(t *testing.T) {
	r := New()
	for _, tool := range []string{"ExitPlanMode", "EnterPlanMode"} {
		t.Run(tool, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
			res, err := r.Evaluate(input)
			if !errors.Is(err, hookio.ErrRefused) {
				t.Fatalf("%s: err=%v res=%+v, want ErrRefused", tool, err, res)
			}
			if res.Decision < hookio.NoOpinion {
				t.Errorf("%s: floor is %s, weaker than NoOpinion", tool, res.Decision)
			}
			if res.Reason == "" || res.Module != r.Name() {
				t.Errorf("%s: floor = %+v, want a reasoned refusal attributed to %q", tool, res, r.Name())
			}
			// The chain MUST continue: a nil error here would make this rule shadow
			// path-safety and every Bash rule after it.
			if !errors.Is(err, hookio.ErrNotApplicable) {
				t.Errorf("%s: refusal does not match ErrNotApplicable; the chain would stop or the engine would file a FAILURE", tool)
			}
		})
	}

	// The three deferral branches (file tools, search tools, Bash) are NOT refusals: this
	// rule has no model for those inputs at all, and their owning rules run later. Claiming
	// a refusal would floor every file/search/Bash leaf in the tree.
	for _, tool := range []string{"Read", "Write", "Glob", "Grep", "Bash", "mcp__x__y", "SomeUnknownTool"} {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		if _, err := r.Evaluate(input); errors.Is(err, hookio.ErrRefused) {
			t.Errorf("%s reported a REFUSAL; this rule never examined it and the floor would reach every such leaf", tool)
		}
	}
}
