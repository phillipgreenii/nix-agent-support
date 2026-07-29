package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// devFdPattern matches /dev/fd/<n> for any file-descriptor number.
var devFdPattern = regexp.MustCompile(`^/dev/fd/[0-9]+$`)

// isSafeRedirectTarget reports whether path is one of the standard special
// device files that are always safe as an I/O redirection target — for reading
// (stdin) and writing (stdout/stderr) alike: /dev/null, /dev/stdout,
// /dev/stderr, /dev/tty, and /dev/fd/<n>. The PathEvaluator does not model these
// pseudo-files (it classifies them PathUnknown), so without this short-circuit a
// redirect to one would demote an otherwise-approved command to Abstain
// (pg2-9ctmb). Kept redirect-scoped on purpose: it does NOT make these paths
// writable to the rest of the ruleset (e.g. `rm /dev/null` is unaffected).
func isSafeRedirectTarget(path string) bool {
	switch path {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return devFdPattern.MatchString(path)
}

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

type Engine struct {
	rules    []hookio.RuleModule
	pathEval *patheval.PathEvaluator
	trace    bool
}

func New(rules ...hookio.RuleModule) *Engine {
	return &Engine{rules: rules}
}

// RegisterRules sets the rule list (for create-then-register pattern).
func (e *Engine) RegisterRules(rules ...hookio.RuleModule) {
	e.rules = rules
}

// SetPathEvaluator sets the path evaluator for I/O redirection evaluation.
func (e *Engine) SetPathEvaluator(pe *patheval.PathEvaluator) {
	e.pathEval = pe
}

// SetTrace enables or disables trace collection and stderr logging.
func (e *Engine) SetTrace(enabled bool) {
	e.trace = enabled
}

func (e *Engine) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	var trace []hookio.TraceEntry

	for _, rule := range e.rules {
		result := rule.Evaluate(input)

		if e.trace {
			entry := hookio.TraceEntry{
				RuleName: rule.Name(),
				Decision: result.Decision,
				Reason:   result.Reason,
			}
			trace = append(trace, entry)
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: TRACE %s -> %s: %s\n",
				rule.Name(), result.Decision, result.Reason)
		}

		if result.Decision != hookio.Abstain {
			if input.ToolName == "Bash" {
				if cmd, err := input.BashCommand(); err == nil {
					if comment := cmdparse.ExtractComment(cmd); comment != "" {
						result.Reason = result.Reason + " (note: " + comment + ")"
					}
				}
			}
			fmt.Fprintf(os.Stderr, "claude-extended-tool-approver: %s -> %s: %s\n",
				rule.Name(), result.Decision, result.Reason)
			result.Trace = trace
			return result
		}
	}

	result := hookio.RuleResult{Decision: hookio.Abstain}
	if e.trace {
		result.Trace = trace
	}
	return result
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

func (e *Engine) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	normalized := normalizeExpression(expr)
	// Check for cycle: has this exact expression been evaluated before?
	for _, frame := range stack {
		if frame.Expression == normalized {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "recursive evaluation: cycle detected (command repeated in stack)",
				Module:   "engine",
			}
		}
	}

	// Strip comments line by line
	lines := strings.Split(expr, "\n")
	for i, line := range lines {
		lines[i] = cmdparse.StripComment(line)
	}
	cleaned := strings.Join(lines, "\n")

	// Parse into sub-commands
	parsed := cmdparse.Parse(cleaned)
	if len(parsed) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: "engine"}
	}

	// Evaluate each sub-command, track most restrictive.
	// Seed with Approve — the least-restrictive identity for the fold: an
	// expression is Approve iff EVERY leaf independently approves.
	mostRestrictive := hookio.RuleResult{Decision: hookio.Approve, Reason: "all sub-commands approved", Module: "engine"}

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

	for _, pc := range parsed {
		if pc.Executable == "" {
			// Command-less leaf: no executable, but it may carry env assignments
			// (`LD_PRELOAD=/evil.so && cmd`, pg2-mtnmb) or redirections/a heredoc (the
			// trailing "> /etc/passwd" of a subshell) that MUST still be evaluated —
			// otherwise the injection, or the write to a protected path, is silently
			// approved.
			if pc.HasHeredoc {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "recursive evaluation: heredoc detected", Module: "engine"}
			}
			leafResult := e.evaluateRedirections(pc.Redirections, currentPathEval)
			if len(pc.Redirections) > 0 {
				judgedLeaf = true
			}
			if assignResult, judged := e.evaluateAssignmentOnlyLeaf(pc, currentCWD, origin); judged {
				leafResult = hookio.MostRestrictive(leafResult, assignResult)
				judgedLeaf = true
			}
			if leafResult.Decision > mostRestrictive.Decision {
				mostRestrictive = leafResult
			}
			continue
		}
		judgedLeaf = true

		// Heredoc detected — Abstain
		if pc.HasHeredoc {
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "recursive evaluation: heredoc detected", Module: "engine"}
		}

		// Build synthetic HookInput (using the running cwd/path-evaluator so a
		// leaf after a `cd` resolves relative paths against the cd target).
		syntheticInput := &hookio.HookInput{
			SessionID:      origin.SessionID,
			CWD:            currentCWD,
			ToolName:       "Bash",
			ToolInput:      mustBashJSON(pc.Raw),
			PermissionMode: origin.PermissionMode,
			HookEventName:  origin.HookEventName,
			PathEval:       currentPathEval,
		}

		// Evaluate through rule chain
		cmdResult := e.Evaluate(syntheticInput)

		// Evaluate I/O redirections. With the restrictiveness ordering
		// (Approve < Abstain < Ask < Reject) a plain most-restrictive-wins
		// comparison correctly lets an unknown redirection path (Abstain) demote
		// an otherwise-approved command — no special case needed.
		redirResult := e.evaluateRedirections(pc.Redirections, currentPathEval)
		cmdResult = hookio.MostRestrictive(cmdResult, redirResult)

		// Substitution-body recursion (pg2-1q5i3). Every top-level $(...) / `...` /
		// <(...) / >(...) body in the COMMAND (env-assignment values excluded — those
		// are the static classifyExpansion path / pg2-gkd5e) is re-evaluated through
		// ALL rules with a pushed StackFrame (so the cycle check above fires) and
		// folded most-risky-wins. The raw command text is scanned (not post-unquote
		// args) so a single-quoted literal `'$(rm -rf ~)'` is correctly skipped and a
		// double-quoted `"$(cmd)"` is still recursed. This replaces the former
		// static command-substitution guard AND the process-substitution loop with
		// one shared enumerator.
		for _, sub := range cmdparse.EnumerateSubstitutions(cmdparse.StripLeadingEnvAssignments(pc.Raw)) {
			subStack := append(stack, hookio.StackFrame{RuleName: "engine", Command: "substitution", Expression: normalized})
			subResult := e.EvaluateExpression(sub.Body, subStack, origin)

			// Static allowlist FLOOR for command substitutions ($()/backtick): a body
			// the static allowlist rejects (e.g. `git show HEAD` — textconv/external-diff
			// RCE) can be no LESS restrictive than Abstain even if full-engine recursion
			// would approve the inner command. Recursion only ADDS demotions. Process
			// substitutions have no static allowlist and are governed by recursion alone.
			if sub.IsCommandSubstitution() && !cmdparse.IsSafeSubstitutionBody(sub.Body) &&
				subResult.Decision < hookio.Abstain {
				subResult = hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "command substitution not on static safe allowlist: " + sub.Body,
					Module:   "engine",
				}
			}

			cmdResult = hookio.MostRestrictive(cmdResult, subResult)
		}

		// Track most restrictive
		mostRestrictive = hookio.MostRestrictive(mostRestrictive, cmdResult)

		// After processing the leaf, advance the running cwd if it is a simple
		// `cd <dir>` with exactly one non-flag argument, so subsequent leaves
		// resolve relative paths against the cd target (pg2-opclh). Conservative:
		// `cd` with zero/multiple args, `cd -`, or `cd ~...` leave the running
		// cwd unchanged (worst case a relative path stays classified as today).
		if basePE != nil && pc.Executable == "cd" && len(pc.Args) == 1 &&
			!strings.HasPrefix(pc.Args[0], "-") && !strings.HasPrefix(pc.Args[0], "~") {
			dir := pc.Args[0]
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
	// Abstain, exactly as it did when Parse dropped these segments and the expression
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
			Decision: hookio.Abstain,
			Reason:   "env assignments only, no rule has an opinion (nothing is executed)",
			Module:   "engine",
		}
	}

	return mostRestrictive
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
// leaf carries no assignments at all and when the chain Abstained on them — see the
// NEUTRAL discussion below and the caller's judgedLeaf floor.
func (e *Engine) evaluateAssignmentOnlyLeaf(pc cmdparse.ParsedCommand, cwd string, origin *hookio.HookInput) (result hookio.RuleResult, judged bool) {
	if len(pc.EnvVars) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "no env assignments to evaluate", Module: "engine"}, false
	}
	syntheticInput := &hookio.HookInput{
		SessionID:      origin.SessionID,
		CWD:            cwd,
		ToolName:       "Bash",
		ToolInput:      mustBashJSON(pc.Raw),
		PermissionMode: origin.PermissionMode,
		HookEventName:  origin.HookEventName,
		PathEval:       origin.PathEval,
	}
	if chainResult := e.Evaluate(syntheticInput); chainResult.Decision != hookio.Abstain {
		return chainResult, true
	}
	// NEUTRAL when no rule has a decisive opinion. An assignment-only leaf EXECUTES
	// NOTHING — it binds shell variables — so with nothing to object to it must
	// contribute nothing to the fold, exactly as evaluateRedirections returns Approve
	// for a leaf with no redirections. Folding the chain's Abstain instead would
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
	// KNOWN GAP, form-independent and pre-existing: cmdparse.classifyExpansion keys on
	// `$`/backtick, so a PROCESS substitution in a value (`A=<(evil)`) classifies as
	// ExpansionNone and no recursion happens. That value already auto-approves in the
	// leading, `export` and `env` forms today, so this does not widen it — but it is a
	// real hole in classifyExpansion and wants its own fix.
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "env assignments only, no rule objects (nothing is executed)",
		Module:   "engine",
	}, false
}

func normalizeExpression(expr string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(expr)), " ")
}

func mustBashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

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
		return hookio.RuleResult{Decision: hookio.Abstain, Module: "engine"}
	}
	// dynamic holds the first unresolvable target's Abstain. It is NOT returned
	// early: the remaining redirections must still be evaluated so a STATIC
	// read-only target later in the same command (`> "$T" > /etc/hosts`) still
	// produces its Reject instead of being masked by this Abstain.
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
					Decision: hookio.Abstain,
					Reason:   "redirection: dynamically-expanded target " + r.Path + " (deferred to claude-code)",
					Module:   "engine",
				}
			}
			continue
		}
		access := pe.Evaluate(r.Path)
		switch r.Kind {
		case hookio.RedirectStdin:
			if !access.CanRead() {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "redirection: stdin from non-readable path " + r.Path, Module: "engine"}
			}
		default:
			if access == patheval.PathReadOnly {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "redirection: write to read-only path " + r.Path, Module: "engine"}
			}
			if !access.CanWrite() {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "redirection: write to non-writable path " + r.Path, Module: "engine"}
			}
		}
	}
	if dynamic != nil {
		return *dynamic
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "redirections: all paths safe", Module: "engine"}
}
