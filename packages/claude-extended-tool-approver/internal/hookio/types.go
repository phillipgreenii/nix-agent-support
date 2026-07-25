package hookio

import (
	"encoding/json"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

type Decision int

// The iota order IS the restrictiveness order the engine's EvaluateExpression
// fold depends on: a compound/expression takes the MOST restrictive
// (numerically largest) decision among its leaves.
//
//	Approve < Abstain < Ask < Reject
//
// Approve is LEAST restrictive — a green light that suppresses Claude Code's own
// permission prompt. Abstain means ceta has no opinion and defers to that prompt;
// it MUST outrank Approve so a compound containing ANY non-approving leaf is never
// green-lit as a whole (pg2-t4uyx). Reject is most restrictive.
//
// Consequence: Approve is now the zero value. Every RuleResult MUST set Decision
// explicitly (audited: all do). Do not reorder without re-auditing the fold in
// internal/engine/engine.go and every `Decision`-ordering comparison.
const (
	Approve Decision = iota
	Abstain
	Ask
	Reject
)

func (d Decision) String() string {
	switch d {
	case Abstain:
		return "abstain"
	case Approve:
		return "approve"
	case Ask:
		return "ask"
	case Reject:
		return "reject"
	default:
		return "unknown"
	}
}

type RuleResult struct {
	Decision Decision
	Reason   string
	Module   string
	Trace    []TraceEntry // nil when tracing is disabled
}

// MostRestrictive returns whichever of current/candidate is more restrictive
// under the Decision ordering (Approve < Abstain < Ask < Reject); ties keep
// current. This is the shared most-risky-wins aggregation primitive for
// substitution-body recursion (pg2-1q5i3); sibling env-value recursion
// (pg2-gkd5e) reuses it so both fold identically. An expression is Approve iff
// EVERY level affirmatively Approves; any Abstain/Ask/Reject at any level wins.
func MostRestrictive(current, candidate RuleResult) RuleResult {
	if candidate.Decision > current.Decision {
		return candidate
	}
	return current
}

type TraceEntry struct {
	RuleName string
	Decision Decision
	Reason   string
}

type HookInput struct {
	SessionID             string          `json:"session_id"`
	CWD                   string          `json:"cwd"`
	ToolName              string          `json:"tool_name"`
	ToolInput             json.RawMessage `json:"tool_input"`
	PermissionMode        string          `json:"permission_mode"`
	HookEventName         string          `json:"hook_event_name"`
	ToolUseID             string          `json:"tool_use_id"`
	AgentID               string          `json:"agent_id,omitempty"`
	AgentType             string          `json:"agent_type,omitempty"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
	Reason                string          `json:"reason,omitempty"`

	// PromptID identifies the user prompt that triggered this tool call. Its
	// presence discriminates a user-approved (prompted) row from a no-prompt
	// settings/hook auto-approval when deriving approval_source. Historical
	// rows (logged before this field was persisted) have it empty/NULL.
	PromptID string `json:"prompt_id,omitempty"`
	// TranscriptPath points at the session transcript file. Persisted as a
	// pointer only (not parsed/inlined); useful for post-hoc investigation.
	TranscriptPath string `json:"transcript_path,omitempty"`
	// ToolResponse is the PostToolUse result payload (raw JSON). Persisted so
	// downstream analysis can tell whether a tool call errored — see the
	// tool_response shape in references/database-schema.md.
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`

	// PathEval, when non-nil, overrides the rule-injected path evaluator for
	// this input. Set by the docker rule when delegating inner expression
	// evaluation to provide mount-aware container semantics. Not serialized.
	PathEval *patheval.PathEvaluator `json:"-"`
}

type BashToolInput struct {
	Command string `json:"command"`
}

type FileToolInput struct {
	FilePath  string `json:"file_path"`
	Content   string `json:"content,omitempty"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}

type SearchToolInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

type WebFetchToolInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

type RuleModule interface {
	Name() string
	Evaluate(input *HookInput) RuleResult
}

// StackFrame represents a level in the recursive evaluation call stack.
type StackFrame struct {
	RuleName   string // e.g., "docker", "nix"
	Command    string // human-readable label: "docker run", "nix develop"
	Expression string // the normalized inner expression being evaluated
}

// RedirectionKind classifies the type of I/O redirection.
type RedirectionKind int

const (
	RedirectStdin  RedirectionKind = iota // <
	RedirectStdout                        // >, >>
	RedirectStderr                        // 2>, 2>>
	RedirectAll                           // &>
)

// Redirection represents a parsed I/O redirection.
type Redirection struct {
	Operator string          // "<", ">", ">>", "2>", "2>>", "&>"
	Path     string          // target file path
	Kind     RedirectionKind // classification
}

// Evaluator allows rules to recursively evaluate inner expressions
// through the full rule chain.
type Evaluator interface {
	EvaluateExpression(expr string, stack []StackFrame, origin *HookInput) RuleResult
}
