package hookio

import (
	"encoding/json"
	"errors"
	"regexp"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

type Decision int

// The iota order IS the restrictiveness order the engine's EvaluateExpression
// fold depends on: a compound/expression takes the MOST restrictive
// (numerically largest) decision among its leaves.
//
//	Approve < NoOpinion < Ask < Reject
//
// Approve is LEAST restrictive — a green light that suppresses Claude Code's own
// permission prompt. NoOpinion means ceta HANDLED the input and has no opinion, so
// it defers to that prompt; it MUST outrank Approve so a compound containing ANY
// non-approving leaf is never green-lit as a whole (pg2-t4uyx). Reject is most
// restrictive.
//
// NoOpinion was called Abstain until ADR 0043. The rename is an IDENTIFIER rename
// only: the SERIALIZED value stays "abstain" (see String below and the three other
// emitters named in that ADR's Decision). The old name carried three unrelated
// meanings at once — "not my business", "handled, no opinion", and "I could not
// determine" — and ADR 0043 moved the first and third out of band onto the
// (RuleResult, error) pair RuleModule.Evaluate now returns, leaving NoOpinion with
// the second meaning ONLY. It is terminal: the engine stops the chain on it.
//
// Consequence: Approve is now the zero value. Every RuleResult MUST set Decision
// explicitly (audited: all do). Do not reorder without re-auditing the fold in
// internal/engine/engine.go and every `Decision`-ordering comparison.
const (
	Approve Decision = iota
	NoOpinion
	Ask
	Reject
)

// String is a SERIALIZATION boundary, not a debug helper: its output is persisted
// (asklog's hook_decision / decision_trace_entries.decision, `evaluate`'s
// replay_result) and tens of thousands of logged rows key on it. NoOpinion MUST
// keep emitting "abstain" — ADR 0043's Decision requires it so historical joins and
// the replay differential survive the rename.
func (d Decision) String() string {
	switch d {
	case NoOpinion:
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

// ErrNotApplicable reports that this rule does not govern this input. It is a
// CONTROL SIGNAL, not a failure (cf. fs.SkipDir): the engine's first-match chain
// treats it as "continue to the next rule" and ignores the returned RuleResult
// entirely.
//
// It MUST be returned BARE. Wrapping it with fmt.Errorf("%w") is FORBIDDEN (ADR
// 0043's Decision, point 5): a wrap that buries it — or one that accidentally makes
// a genuine failure match it — is not a compile error and no test catches it, and
// the failure mode is a SILENT AUTO-APPROVAL. Callers therefore compare with
// errors.Is, and the guard against wrapping is the review rule plus
// TestErrNotApplicableIsNeverWrapped in this package.
var ErrNotApplicable = errors.New("rule does not apply")

// NotApplicable is the canonical not-my-business return for a rule. Use it instead
// of spelling the pair out, so the "bare error, zero-value result" contract is
// stated in exactly one place.
func NotApplicable() (RuleResult, error) { return RuleResult{}, ErrNotApplicable }

// FromRecursion translates the verdict of a recursively-evaluated INNER expression
// (Evaluator.EvaluateExpression, which returns a bare RuleResult) into the
// (RuleResult, error) pair a RuleModule must return when it forwards that verdict
// as its own.
//
// The translation the ADR 0043 Consequences demand be stated explicitly: an inner
// NoOpinion is the inner chain's LOOP-EXHAUSTION verdict — no inner rule owned the
// expression — so a rule that merely forwards it has formed no opinion of its own
// and MUST let the outer chain continue. Returning it as a verdict instead would
// make the outer chain STOP, which is a decision change (before ADR 0043 the same
// forwarded Abstain meant "continue").
//
// Any other inner decision (Approve/Ask/Reject) IS an opinion and is forwarded
// verbatim as a terminal verdict, exactly as before.
//
// This is for a rule that adopts the inner verdict WHOLESALE. A rule that FOLDS the
// inner verdict with its own (envvars) must fold the RuleResult first — inside a
// MostRestrictive fold NoOpinion is the floor and an error has no representation —
// and apply this translation only to the folded result.
func FromRecursion(inner RuleResult) (RuleResult, error) {
	if inner.Decision == NoOpinion {
		return NotApplicable()
	}
	return inner, nil
}

type RuleResult struct {
	Decision Decision
	Reason   string
	Module   string
	Trace    []TraceEntry // nil when tracing is disabled
}

// MostRestrictive returns whichever of current/candidate is more restrictive
// under the Decision ordering (Approve < NoOpinion < Ask < Reject); ties keep
// current. This is the shared most-risky-wins aggregation primitive for
// substitution-body recursion (pg2-1q5i3); sibling env-value recursion
// (pg2-gkd5e) reuses it so both fold identically. An expression is Approve iff
// EVERY level affirmatively Approves; any NoOpinion/Ask/Reject at any level wins.
//
// The fold is a DIFFERENT MACHINE from the first-match chain, and ADR 0043 turns on
// the difference: the fold is seeded at Approve, so "contribute nothing" here is
// Approve, NOT NoOpinion. A failure therefore MUST contribute the NoOpinion FLOOR
// inside a fold and MUST NOT be routed to the chain's "continue" — routing it there
// would contribute the Approve identity and manufacture an approval, reinstating
// pg2-wguam and pg2-2u5jf. Errors consequently have no representation in this
// function's inputs at all: they are consumed at the engine's chain chokepoint.
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

	// RootExpression carries the FULL text of the expression a synthetic
	// per-leaf input was split out of. engine.EvaluateExpression sets it on every
	// leaf it evaluates (including the command-less assignment-only leaves), so a
	// rule that needs EXPRESSION scope rather than leaf scope can reach the
	// sibling leaves — the only place some facts exist at all.
	//
	// The motivating fact (gitdir/pg2-3hk7t): a leaf that merely BINDS a path,
	// `f="$r/.git/info/exclude"`, is byte-for-byte identical whether the sibling
	// that consumes `"$f"` is a read (`cat`) or a write (`sed -i`). Leaf scope
	// cannot tell those apart, so a leaf-local rule must fail safe and hard-deny
	// both. With the expression in hand the direction is decidable and only the
	// real write is rejected.
	//
	// Empty when a rule is invoked outside EvaluateExpression (non-Bash tools,
	// direct unit-test calls); a consumer MUST then fall back to leaf scope.
	// `json:"-"` is load-bearing: this is engine-derived provenance, never
	// something a hook payload may assert.
	RootExpression string `json:"-"`

	// InCommandVars holds the shell variables that EARLIER LEAVES of this same
	// expression established, mapped to their LITERAL values —
	// `WT=/abs/worktree && git -C "$WT" commit` binds `WT`. It is
	// cmdparse.InCommandVars, computed once per leaf by
	// engine.EvaluateExpression, so a rule that must judge a PATH can resolve a
	// variable the command itself writes down instead of treating every `$WT` as
	// unknowable (pg2-wq3ki).
	//
	// IT IS NOT THE ENVIRONMENT. CETA receives no environment, so an inherited
	// export, a `$(…)` value and anything set by an earlier Bash call are all
	// ABSENT here — not empty-valued, absent — and a consumer MUST keep its
	// existing fail-safe path for a name it does not find. nil is the ordinary
	// case (no assignment in the command) and nil for a rule invoked outside
	// EvaluateExpression, which is why every consumer's no-binding branch must be
	// the behaviour it had before this field existed.
	//
	// `json:"-"` for the same reason as RootExpression, and it matters more here:
	// a hook payload that could assert a variable's value would be asserting the
	// directory a commit lands in.
	InCommandVars map[string]string `json:"-"`
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

// RuleModule is one rule in the engine's first-match-wins chain.
//
// Evaluate returns THREE distinguishable outcomes (ADR 0043), which the engine
// discriminates at one chokepoint:
//
//	(res, nil)                       HANDLED — res.Decision is the verdict and the
//	                                 chain STOPS here. NoOpinion is a legitimate
//	                                 verdict: "I handled this and my answer is no
//	                                 gate", which emits {} and defers to Claude Code.
//	(RuleResult{}, ErrNotApplicable) NOT MY BUSINESS — the chain CONTINUES and the
//	                                 RuleResult is ignored. This is what the old
//	                                 Abstain-as-loop-sentinel meant.
//	(RuleResult{}, otherErr)         COULD NOT DETERMINE — evidence gathering failed.
//	                                 The engine records it per rule and continues.
//
// Choosing between the first two is the whole of the conversion risk, and the test
// is DIRECTIONAL: does a LATER rule need to act on this input? If yes it MUST be
// ErrNotApplicable (a terminal NoOpinion would shadow that rule — the shape
// claudetools and killshell have, guarded by
// TestIntegration_KillShellThroughChain's "does not shadow the later path-safety
// rule"). If the chain must STOP here it MUST be NoOpinion (ErrNotApplicable would
// let a later rule approve — the shape pathsafety's agent-config write branch has,
// required by ADR 0041's Decision).
//
// A rule that CANNOT complete an identity or ownership check MAY instead return its
// own fail-closed verdict with a nil error; killshell does exactly that and its Ask
// is named in ADR 0043's error policy as the one carve-out.
type RuleModule interface {
	Name() string
	Evaluate(input *HookInput) (RuleResult, error)
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
	RedirectStdout                        // >, >>, >|, 1>, 1>>, 1>|
	RedirectStderr                        // 2>, 2>>, 2>|
	RedirectAll                           // &>, &>>, >& FILE
	// RedirectOtherFD is a write to a PATH on a descriptor that is neither stdout
	// nor stderr: `9> f`, `3>> f`, `{fd}> f`. It is a file write like any other —
	// every write-direction consumer must treat it as one — but it captures no
	// stdout, so cmdparse.CapturesStdout deliberately does NOT count it.
	RedirectOtherFD
	// RedirectReadWrite is bash's `<>` open: the target is opened for reading AND
	// writing, and may be created. It is classified as a WRITE (it is checked for
	// writability, not readability) because creating/modifying the target is the
	// direction that matters to a permission gate.
	RedirectReadWrite
)

// IsWrite reports whether the redirection can CREATE OR MODIFY its target.
// Everything that is not a pure read (`<`) is a write, so a kind added later
// fails closed rather than silently becoming read-only.
func (k RedirectionKind) IsWrite() bool { return k != RedirectStdin }

// Redirection represents a parsed I/O redirection.
type Redirection struct {
	// Operator is the operator text AS WRITTEN, including any file-descriptor
	// prefix: "<", ">", ">>", ">|", ">&", "<>", "1>", "2>>", "9>", "{fd}>", "&>",
	// "&>>". A consumer MUST classify by Kind, never by matching this string.
	Operator string
	Path     string          // target file path
	Kind     RedirectionKind // classification
}

// devFdPattern matches /dev/fd/<n> for any file-descriptor number.
var devFdPattern = regexp.MustCompile(`^/dev/fd/[0-9]+$`)

// IsSafeRedirectTarget reports whether path is one of the standard special device
// files that are always safe as an I/O redirection target — for reading (stdin)
// and writing (stdout/stderr) alike: /dev/null, /dev/stdout, /dev/stderr,
// /dev/tty, and /dev/fd/<n>.
//
// TWO callers, for two different reasons, which is why it lives beside the
// Redirection type rather than inside either of them (the same relocation
// cmdparse.SkipGrepPattern got when a rule needed to share it):
//
//   - the engine's redirection evaluation, where the PathEvaluator does not model
//     these pseudo-files (it classifies them PathUnknown) and without the
//     short-circuit a redirect to one would demote an otherwise-approved command
//     to NoOpinion (pg2-9ctmb);
//   - the gitdir rule's copy-out detection, where an output redirection is what
//     turns a read of git metadata into a capture of it — but writing to a
//     terminal or discarding to /dev/null captures nothing, so `ls .git/hooks
//     2>/dev/null` must stay a plain read (tc-403c).
//
// Being a redirect-TARGET predicate is the whole of its meaning: it does NOT make
// these paths writable to the rest of the ruleset (e.g. `rm /dev/null` is
// unaffected).
func IsSafeRedirectTarget(path string) bool {
	switch path {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return devFdPattern.MatchString(path)
}

// Evaluator allows rules to recursively evaluate inner expressions
// through the full rule chain.
//
// It deliberately returns a BARE RuleResult, not the (RuleResult, error) pair
// RuleModule returns: the inner chain has already run to completion, so
// "not applicable" and "could not determine" were both consumed inside it and the
// only thing left is a verdict. An exhausted inner chain surfaces as the terminal
// NoOpinion. A rule forwarding that verdict as its OWN must translate it — see
// FromRecursion, which is where the ADR 0043 recursion-boundary rule lives.
type Evaluator interface {
	EvaluateExpression(expr string, stack []StackFrame, origin *HookInput) RuleResult
}
