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

// Provenance is the SECOND, EXPLICIT channel ADR 0044 adds beside Decision: for a
// NoOpinion verdict it reports WHY there is no opinion.
//
// ADR 0043 narrowed NoOpinion to exactly one meaning — "handled, no gate" — and this
// type MUST NOT be read as re-widening it. Decision still answers only "how
// restrictive is this verdict?"; Provenance answers the ORTHOGONAL question "did any
// rule form that verdict, or did the chain simply run out of rules?". The
// restrictiveness order, MostRestrictive and the serialized "abstain" are untouched.
//
// WHY A CALLER NEEDS IT. A rule that delegates to Evaluator.EvaluateExpression gets a
// bare RuleResult back, and an exhausted inner chain surfaces as the same terminal
// NoOpinion a refusing rule produces (pg2-d0ja3). Without this channel the delegating
// rule cannot tell "no rule models `seq 1 3`" from "safe-commands looked at
// `rm -rf /etc` and would not clear it", so its only safe move is to escalate BOTH —
// which is why the envvars fallback had to be an unconditional Ask and why the static
// substitution allowlist had to be hand-extended per basename.
//
// FAIL-SAFE ZERO VALUE. ProvenanceRefusal is 0, so every RuleResult literal in the
// tree — 150-odd of them — reads as a refusal without being touched, and a site that
// forgets to declare its provenance can never be MISTAKEN FOR AN EXHAUSTION.
// Exhaustion is the half that lets a consumer clear a body, so it must be claimed
// explicitly and in exactly one place (engine.Evaluate's loop exhaustion).
type Provenance int

const (
	// ProvenanceRefusal means a rule, or an engine floor, formed this verdict. It is
	// the ZERO VALUE and therefore the default for every unannotated RuleResult.
	ProvenanceRefusal Provenance = iota
	// ProvenanceExhaustion means NO rule claimed the input: every rule in the chain
	// reported ErrNotApplicable, none failed, and none refused. It is claimed by
	// engine.Evaluate when the loop runs out, and it survives an expression-level
	// fold only under the conditions engine.EvaluateExpression documents.
	ProvenanceExhaustion
)

func (p Provenance) String() string {
	if p == ProvenanceExhaustion {
		return "exhaustion"
	}
	return "refusal"
}

// mergeProvenance folds two provenances CONSERVATIVELY: the result is an exhaustion
// only if BOTH inputs are. One rule refusing is enough to make the pair a refusal, so
// the merge is an AND over "exhaustion" and the fail-safe zero value wins by default.
//
// It exists so MostRestrictive stays ORDER-INDEPENDENT. Without it a tie between an
// exhaustion leaf and a refusal leaf would resolve to whichever the caller happened to
// fold first, and the verdict of `seq 1 3 && cat <<EOF` would depend on the order of
// the operands.
func mergeProvenance(a, b Provenance) Provenance {
	if a == ProvenanceExhaustion && b == ProvenanceExhaustion {
		return ProvenanceExhaustion
	}
	return ProvenanceRefusal
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

// refusalError is the concrete type behind ErrRefused. It DELIBERATELY matches
// ErrNotApplicable under errors.Is, and that is the whole reason it is a type rather
// than another package-level errors.New value.
//
// The match is a SUBTYPE claim, not a wrap. At the chain level a refusal says exactly
// what ErrNotApplicable says — "do not stop here, keep going" — and adds one fact on
// top: "and whatever you conclude, it cannot be less restrictive than the RuleResult I
// returned". So every existing consumer that only knows ErrNotApplicable keeps
// behaving as it does today, which is what makes the 46 site conversions in this
// change test-compatible and makes an un-upgraded consumer fail SAFE (it merely loses
// the floor, it does not mis-read a refusal as a verdict).
//
// It is NOT the wrap ADR 0043's Decision point 5 forbids, and the distinction is
// worth stating because the shapes look alike. That prohibition guards two failures:
// (i) BURYING ErrNotApplicable so errors.Is stops matching it, and (ii) making a
// GENUINE FAILURE match it by accident, silently converting an error into "absent".
// This type does neither. It never carries a cause, so nothing can be buried in it and
// no failure can arrive wearing it — errors.Is(someRuleError, ErrNotApplicable) is
// still false. It uses no fmt.Errorf("%w") and no errors.Join, so
// TestErrNotApplicableIsNeverWrappedInSource still passes on its own terms.
// TestRefusalIsANotApplicableSubtype pins the intended relationship in both
// directions.
type refusalError struct{}

func (refusalError) Error() string {
	return "rule refuses to clear this input (chain continues, floored)"
}

// Is makes a refusal match ErrNotApplicable — see refusalError's doc for why this is
// a subtype claim and not the forbidden wrap.
func (refusalError) Is(target error) bool { return target == ErrNotApplicable }

// ErrRefused reports that this rule EXAMINED the input, will not clear it, and yet
// must not stop the chain — the fourth chain outcome ADR 0044 adds.
//
// It is the outcome ADR 0043 had no room for. That ADR mapped every rule site onto
// three cases, and the sites whose Abstain meant "I looked and this is not clearable,
// but a LATER rule may still own it" had nowhere to go: a terminal NoOpinion would
// SHADOW that later rule (Shape A/B), so they became ErrNotApplicable and their
// REASONS were demoted to comments. 46 of those comments survive in the tree, each
// opening "Former Reason, kept because it is the only record of WHY" — a written
// record of information the vocabulary could not carry. This is where they go back.
//
// The engine folds the returned RuleResult into the leaf's verdict as a FLOOR through
// MostRestrictive and continues the chain. So it can only ever make a leaf MORE
// restrictive, it never shadows a later rule (the later rule still runs and its
// Ask/Reject still wins), and it never suppresses a Reject.
var ErrRefused error = refusalError{}

// Refuse pairs an arbitrary verdict FLOOR with the sentinel. The floor may be any
// Decision: NoOpinion for "I cannot clear this", Ask for "this needs a person" — the
// difference from returning the same verdict with a nil error is only that the chain
// KEEPS GOING, so a later rule's stronger verdict still wins and nothing is shadowed.
func Refuse(floor RuleResult) (RuleResult, error) { return floor, ErrRefused }

// Refused is the common case of Refuse: the NoOpinion floor, spelled as module+reason.
// Provenance is left at its zero value ProvenanceRefusal, which is the point.
func Refused(module, reason string) (RuleResult, error) {
	return Refuse(RuleResult{Decision: NoOpinion, Reason: reason, Module: module})
}

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
// AN INNER REFUSAL IS FORWARDED AS A REFUSAL (pg2-ij9sr, the residual ADR 0044 recorded
// and deferred). ADR 0044 gave the inner NoOpinion a PROVENANCE, which splits the case
// the paragraph above treats as one:
//
//   - EXHAUSTION — no inner rule owned the expression. The forwarding rule has formed no
//     opinion of its own, so the outer chain must continue with nothing carried over:
//     ErrNotApplicable, exactly as before.
//   - REFUSAL — an inner rule, or an engine floor, examined the expression and would not
//     clear it. That IS a fact about the outer leaf, and dropping it was the same collapse
//     ADR 0044 exists to fix, one level out: the outer chain would conclude its own
//     terminal NoOpinion with nothing recording that anything was refused, so the outer
//     leaf reported an EXHAUSTION — the half a consumer may act on to clear a body. So the
//     inner verdict is forwarded as ErrRefused, WHOLE: Module, Reason and Provenance ride
//     along, which is what makes the refusing rule's identity survive the hop instead of
//     being re-attributed to the delegating rule.
//
// The two MUST NOT collapse into one another in EITHER direction, and the test here is
// written so only an EXPLICIT exhaustion claim takes the not-applicable branch. That is the
// fail-safe orientation: ProvenanceRefusal is the zero value, and a genuine inner FAILURE
// also withdraws the exhaustion claim (engine.Evaluate's sawFailure), so an inner chain
// that broke is floored rather than reported as "nobody refused".
//
// A refusal can only make the outer leaf MORE restrictive: the outer engine folds the
// floor and keeps going, so a later rule's Ask/Reject still wins and only its Approve is
// demoted. Nothing is shadowed.
//
// THIS IS FOR A RECURSION BOUNDARY ONLY — the verdict of Evaluator.EvaluateExpression,
// which the engine always stamps with a Provenance. A rule translating its OWN FOLD result
// must use FromFold instead; see that function for why sharing one translation stopped
// being possible the moment a refusal became forwardable.
func FromRecursion(inner RuleResult) (RuleResult, error) {
	if inner.Decision != NoOpinion {
		return inner, nil
	}
	if inner.Provenance == ProvenanceExhaustion {
		return NotApplicable()
	}
	return Refuse(inner)
}

// FromFold translates a rule's OWN FOLD RESULT into the (RuleResult, error) pair a
// RuleModule must return. It is the translation FromRecursion performed before pg2-ij9sr,
// split out under its own name because the two inputs are DIFFERENT KINDS OF THING and
// only one of them can carry a refusal.
//
// A fold's NoOpinion is the fold's IDENTITY — "nothing in this leaf was mine" — and it
// carries no engine-assigned Provenance at all. Its zero value is ProvenanceRefusal only
// because the seed literal declares nothing, which is the correct fail-safe default for a
// VERDICT but is emphatically not a claim that anything was examined. So the identity MUST
// become ErrNotApplicable, and routing it through FromRecursion after pg2-ij9sr would read
// that zero value as a refusal and floor every leaf the rule folds over. For envvars —
// which reaches its identity for every ordinary `A=1 cmd` AND for every Bash leaf carrying
// no assignment at all — that is every Bash command in the corpus.
//
// A rule that folds its own verdicts already knows, separately, whether anything WAS
// examined and refused (envvars' `refused` flag) and returns hookio.Refuse itself in that
// case. That is the ADR 0044 division of labour, and it is why this function never has to
// guess: by the time it is called, the refusal case has already been taken.
//
// An Approve/Ask/Reject fold result IS an opinion and is forwarded verbatim as a terminal
// verdict, the same as in FromRecursion.
func FromFold(folded RuleResult) (RuleResult, error) {
	if folded.Decision == NoOpinion {
		return NotApplicable()
	}
	return folded, nil
}

type RuleResult struct {
	Decision Decision
	Reason   string
	Module   string
	Trace    []TraceEntry // nil when tracing is disabled

	// Provenance qualifies a NoOpinion Decision: did a rule form this verdict, or
	// did the chain run out of rules? It is meaningful only for NoOpinion and is
	// ignored for every other Decision (an Approve/Ask/Reject is affirmative by
	// construction). NOT SERIALIZED and deliberately so: ADR 0043 pinned the four
	// emitters of the "abstain" string and the live log holds tens of thousands of
	// rows keyed on it, so this channel adds no column and no new persisted value.
	//
	// Its zero value is ProvenanceRefusal, so every existing literal is a refusal
	// and only an explicit claim can be an exhaustion.
	Provenance Provenance
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
//
// PROVENANCE (ADR 0044) is merged only on a TIE, and the asymmetry is deliberate:
//
//   - candidate STRICTLY MORE restrictive: the candidate IS the verdict and its
//     provenance comes with it. The loser is discarded whole.
//   - candidate STRICTLY LESS restrictive: discarded whole, provenance included. This
//     is what keeps the neutral Approve seeds ("no redirections to evaluate", "no
//     substitutions to evaluate") — which carry the zero-value refusal provenance —
//     from tainting every fold they take part in.
//   - TIE: current's Reason/Module win as before, and the provenances merge
//     conservatively (exhaustion only if both are). Two equally-restrictive NoOpinions
//     are jointly the verdict, so if either was a refusal the pair is one, and the
//     result cannot depend on fold order.
func MostRestrictive(current, candidate RuleResult) RuleResult {
	if candidate.Decision > current.Decision {
		return candidate
	}
	if candidate.Decision == current.Decision {
		current.Provenance = mergeProvenance(current.Provenance, candidate.Provenance)
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

	// InCommandTempDirVars is InCommandVars' sibling for a different, narrower
	// fact: which of those same EARLIER-LEAF variables are bound to a freshly
	// created `mktemp -d` directory (cmdparse.InCommandTempDirVars), rather than
	// to a literal value. `T=$(mktemp -d)` is exactly the shape InCommandVars
	// itself refuses — a command substitution is never literal — so this field
	// exists precisely because InCommandVars has nothing to say about T at all.
	// Values are the empty-string SENTINEL cmdparse.ExpandInCommand needs to
	// treat the name as literal-and-known without asserting what path it
	// actually names (pg2-d71my; internal/rules/envvars is its only consumer,
	// for HOME="$T/h" after an earlier `T=$(mktemp -d)`).
	//
	// Same provenance and the same fail-safe default as InCommandVars: nil for
	// no qualifying assignment and nil for a rule invoked outside
	// EvaluateExpression, so a consumer's no-binding branch is unaffected by
	// this field's absence. `json:"-"` for the identical reason — a hook payload
	// must never assert which directory HOME will end up naming.
	InCommandTempDirVars map[string]string `json:"-"`
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
// Evaluate returns FOUR distinguishable outcomes — ADR 0043's three plus ADR 0044's
// refusal — which the engine discriminates at one chokepoint:
//
//	(res, nil)                       HANDLED — res.Decision is the verdict and the
//	                                 chain STOPS here. NoOpinion is a legitimate
//	                                 verdict: "I handled this and my answer is no
//	                                 gate", which emits {} and defers to Claude Code.
//	(RuleResult{}, ErrNotApplicable) NOT MY BUSINESS — the chain CONTINUES and the
//	                                 RuleResult is ignored. This is what the old
//	                                 Abstain-as-loop-sentinel meant.
//	(floor, ErrRefused)              REFUSED — I examined this and will not clear it,
//	                                 but a LATER rule may still own it. The chain
//	                                 CONTINUES and `floor` is folded into whatever it
//	                                 concludes (hookio.Refused; ADR 0044).
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
// ADR 0044 splits that choice into THREE, and the new middle option is what most of
// the converted sites actually meant: if a later rule must still act AND this rule has
// nothing to say, ErrNotApplicable; if a later rule must still act BUT this rule has
// looked and will not clear the input, ErrRefused; if the chain must stop here,
// NoOpinion. Choosing ErrNotApplicable where ErrRefused is meant is the
// APPROVAL-WIDENING mistake, because it lets the leaf be reported as an EXHAUSTION —
// see Provenance.
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
//
// The returned RuleResult's Provenance is the ADR 0044 channel that makes that
// NoOpinion actionable: ProvenanceExhaustion means no rule owned the inner expression
// (so ceta's own verdict for it, standing alone, is `{}`), while ProvenanceRefusal
// means a rule or an engine floor formed the verdict. A consumer MUST treat
// ProvenanceRefusal as the default and MUST NOT infer safety from an exhaustion — an
// exhaustion says only "ceta has no model for this", which is the SAME thing ceta
// would answer for the expression standing alone, never that the expression is safe.
// engine.EvaluateExpression documents the exact conditions under which an exhaustion
// survives its fold.
type Evaluator interface {
	EvaluateExpression(expr string, stack []StackFrame, origin *HookInput) RuleResult
}
