package hookio

import (
	"encoding/json"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

type Decision int

const (
	Abstain Decision = iota
	Approve
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
