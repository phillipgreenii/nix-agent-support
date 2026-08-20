package envvars

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarycommit"
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
//
// # OPERATOR RULING 2026-07-30 (pg2-553z3): KEEP STRICT
//
// Widening preservesCallerValue's component predicate to accept arbitrary
// `$VAR`-derived components (so `$JAVA_HOME/bin:$PATH`, `$bindir:$PATH`, etc.
// would also Approve) was CONSIDERED AND REJECTED. Do not re-litigate the
// blanket widen from prompt volume alone — the objection below holds at any
// volume.
//
// COHERENCE REASON (the load-bearing one): isStaticAbsolutePath deliberately
// REJECTS an empty `:`-separated component, because an empty PATH entry means
// "the current directory" to the shell — so `PATH=":$PATH"` must keep asking.
// Accepting any `$VAR/...` component on the strength of "it looks like a
// directory" would auto-approve `$PWD/bin:$PATH`, which is that identical
// CWD-on-PATH hazard wearing a variable. Blessing one spelling of the hazard
// while the other keeps asking is incoherent, independent of how many prompts
// widening would clear.
//
// MEASURED BASIS (pg2-3arc2, 2026-07-30): post-apply asklog rows whose command
// contains `PATH=` and were decided by this predicate: ZERO. The 41 pre-apply
// PATH asks that originally motivated the question were already resolved by
// commit 202c2f80 ("value-aware split for the envvars PATH/HOME name-only
// Ask", pg2-0q99a) — cited here as 202c2f80, and deliberately NOT as
// `c280e018`, the pre-rebase sha this bead's own earlier history cites: that
// sha is not an ancestor of main (the local ff-merge rewrote it), while
// 202c2f80 is, with an identical patch-id. The single most common ask shape
// (73 of 99 rows in that window) was a fully STATIC absolute prepend with no
// `$VAR` component at all, so widening would not have relieved the traffic
// that raised the question in the first place.
//
// The knowingly-accepted trade documented in preservesCallerValue below — a
// hostile static prepend (`export PATH="/tmp/evil/bin:$PATH"`) is Approved
// today because isStaticAbsolutePath asks only for a leading `/` — is a
// PRE-EXISTING documented trade, untouched by this ruling; it is not something
// this ruling newly discovers or changes.
//
// NOT KILLED by this ruling — two narrower questions in the same predicate
// area were tracked separately, deliberately NOT folded into this decision:
//
//   - pg2-qhhil: the NARROW middle option — accept a `$VAR` component only
//     when that variable was assigned earlier IN THE SAME COMMAND to a static
//     absolute path (e.g. `bindir=/tmp/x/bin; PATH="$bindir:$PATH"`). Such a
//     component is exactly as inspectable as a literal path, so it is not
//     subject to the coherence objection above. OPERATOR RULING 2026-08-17
//     (via `/unblock-human-beads`, gated on measured volume — pg2-3arc2: 23 of
//     74 post-apply PATH/HOME asks, 0 denials, all this shape): BUILD. Now
//     IMPLEMENTED — see preservesCallerValue's in-command-assigned branch,
//     which wires the pg2-wq3ki InCommandVars/ExpandInCommand seam in. The
//     ambient-variable shapes above ($PWD, $JAVA_HOME, $TMP, ...) are
//     unaffected: they are never assigned by the command's own text, so the
//     seam never resolves them and they keep asking.
//   - pg2-kzqw2: a `$(...)`-derived component — the middle option this
//     ruling's trade analysis fanned out to once the blanket widen was
//     rejected. OPERATOR RULING 2026-08-17 (via `/unblock-human-beads`,
//     decided together with pg2-qhhil and pg2-d71my, same sitting; measured
//     pg2-3arc2: 24 of 74 post-apply PATH/HOME asks, 0 denials): BUILD —
//     "Option 1", admit the component by SUBSTITUTION SAFETY rather than by
//     evaluating its resolved value: a component whose command substitution
//     body is already certified safe under the static allowlist
//     (cmdparse.IsSafeSubstitutionBody, e.g. `$(dirname …)`, `$(readlink -f
//     …)`) is acceptable WITHOUT knowing what it resolves to — the identical
//     trade this ruling's own COHERENCE REASON already accepts for a literal
//     static prepend. Now IMPLEMENTED — see preservesCallerValue's
//     splitPathValueComponents/componentSafeSubstitution branch. This does
//     NOT reopen the coherence objection above: unlike `$PWD` or `$JAVA_HOME`,
//     a command substitution is not an AMBIENT lookup this predicate can never
//     ground — its body is inspected, statically, by the same allowlist
//     ExpansionSafeCmd already trusts. What it DOES require, because a
//     command substitution (unlike a syntactic hazard) can resolve to the
//     EMPTY STRING on any given invocation: componentSafeSubstitution demands
//     the component's LITERAL skeleton alone — the substitution zeroed out —
//     still be a non-empty static absolute path, so a bare `$(safe-cmd)`
//     component with nothing else keeps asking exactly like `PATH="$PATH:"`
//     does, while `$(safe-cmd)/bin` does not (see that function's own doc for
//     the full reasoning). A body carrying a NESTED substitution
//     (`$(dirname "$(readlink -f x)")`) is never on the static allowlist in
//     the first place (IsSafeSubstitutionBody refuses nesting), so it is
//     unaffected by this widening and keeps asking on the SAME pre-existing
//     ground as before.
//   - pg2-d71my: REPLACEMENT-form values (`env -i`, hermetic-HOME test
//     idioms) — a DIFFERENT question from this ruling's EXTEND-shape
//     component predicate (see the Rule doc comment's Approve CONTRACT).
//     OPERATOR RULING 2026-08-17 (via `/unblock-human-beads`, decided
//     together with pg2-qhhil, same sitting): BUILD both the `env -i`
//     hermetic-replacement relief and the HOME=temp-dir relief. Now
//     IMPLEMENTED — see isHermeticEnvReplacement and
//     isHermeticHomeReplacement, and evaluateAssignment's askVars case,
//     which tries preservesCallerValue first and these two second.
var askVars = map[string]bool{
	"PATH": true,
	"HOME": true,
}

// Rule is the unified, DECISIVE environment-assignment guard. It aggregates a
// per-(var,value) sub-verdict most-restrictive-wins.
//
// Approve CONTRACT (pg2-0q99a — this replaced a former "NEVER returns Approve"
// invariant; pg2-d71my widened condition 2's alternatives, not its shape). The
// rule returns Approve for EXACTLY ONE shape, and all three conditions must
// hold:
//
//  1. the NAME is an askVar (PATH/HOME) — never an injector, never an
//     injectorAskVar, never a benign name (a benign assignment stays Abstain: the
//     rule has no opinion to offer);
//  2. the VALUE satisfies ONE of three mutually-independent predicates:
//     preservesCallerValue (it demonstrably preserves the caller's own value and
//     adds only static absolute path components — pg2-0q99a/pg2-qhhil), or
//     isHermeticEnvReplacement (a static, reasonable REPLACEMENT under `env -i` —
//     pg2-d71my), or, for HOME only, isHermeticHomeReplacement (a REPLACEMENT
//     grounded in a `mktemp -d` fresh temp dir — pg2-d71my); and
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
// one whole `:`-separated component, and every other component is EITHER a static
// absolute path, OR a reference to a variable THIS SAME COMMAND assigned, earlier,
// to one (vars — see below), OR a command substitution already certified safe by
// the static allowlist (pg2-kzqw2 — see componentSafeSubstitution). The first shape
// is 954 of the 1,118 logged PATH/HOME assignments, with zero adversarial values
// among them (pg2-qfuto).
//
// The predicate is deliberately STRICT — a component must be literal-and-absolute,
// resolve to exactly that through vars, or be a certified-safe substitution — so
// nothing behind an AMBIENT expansion can smuggle a lookup directory in. It
// therefore still asks on `$PWD/bin:$PATH`, `$JAVA_HOME/bin:$PATH` and
// `$(nix build …)/bin:$PATH` (`nix` is not on the static safe-cmd allowlist).
// Widening it to accept ARBITRARY $VAR-derived components was considered and RULED
// AGAINST (2026-07-30) — see the "OPERATOR RULING" note on the `askVars` doc
// comment above for the coherence reason and the measured basis; do not
// re-litigate the blanket widen here. The in-command-assigned case and the
// certified-safe-substitution case below are the two surviving middle options that
// ruling explicitly did NOT kill (pg2-qhhil and pg2-kzqw2 respectively, both BUILD).
//
// vars is the in-command variable environment for the leaf this assignment
// belongs to (primarycommit.LeafVars over the caller's own parse, wrapping
// cmdparse.InCommandVars) — nil is the ordinary case (no qualifying in-command
// assignment exists) and reproduces the pre-pg2-qhhil predicate exactly.
//
// What it can NOT distinguish, knowingly: a hostile static prepend
// (`PATH="/tmp/evil/bin:$PATH"`) from a legitimate one (`/nix/store/…/bin`). That
// is inherent to any value-aware split — the caller's PATH is still intact and the
// directory is one the user already controls — and it is the same guarantee a
// settings.json `Bash(export PATH:*)` entry already grants. An in-command-assigned
// component (`bindir=/tmp/x/bin; PATH="$bindir:$PATH"`) and a certified-safe
// substitution component (`$(dirname /usr/local/bin/go)/bin`) carry the identical
// trade: each is exactly as inspectable as writing the path literally, no more and
// no less — except for the substitution's own EMPTY-RESULT hazard, which
// componentSafeSubstitution handles explicitly (see its doc).
func preservesCallerValue(ev cmdparse.EnvAssignment, vars map[string]string) bool {
	// The bash append form NAME+=VALUE (normalized to NAME by cmdparse) IS
	// semantically a preserve, but it deliberately does NOT approve: no logged row
	// uses it, and excluding it keeps the Approve as narrow as possible.
	if strings.HasPrefix(ev.Raw, ev.Name+"+=") {
		return false
	}
	// A plain $VAR/${VAR}-referencing value with NO substitution at all is the
	// ordinary extend shape (ExpansionVarRef). A value that ALSO embeds a command
	// substitution alongside the self-reference — `"$(cmd)/bin:$PATH"` — censuses
	// as ExpansionUnknown instead (classifyExpansion's kind() awards VarRef only
	// when NO command substitution is present at all), so ExpansionUnknown must
	// also reach the per-component analysis below for pg2-kzqw2's relief to apply.
	// This does NOT reopen the door to every other ExpansionUnknown shape: every
	// component below still needs its OWN independent, structural reason to
	// clear, and the value with no self-reference at all still fails on
	// `preserved` staying false (a value like `PATH=$(curl evil)` continues to be
	// treated purely as a REPLACEMENT and never reaches Approve here).
	if ev.Expansion != cmdparse.ExpansionVarRef && ev.Expansion != cmdparse.ExpansionUnknown {
		return false
	}
	// The value's quote/expansion structure is derived from the seam's own parse
	// (cmdparse.LiteralAssignmentValueText, pg2-30wro), not a hand-rolled scan: it
	// accepts an unquoted value or one wrapped in a SINGLE double-quoted span
	// covering the whole value, and refuses everything else — a single-quoted
	// value (`PATH='$PATH:/x'` REPLACES, since `$PATH` never expands inside single
	// quotes, so it must not read as an extend) and mixed quoting
	// (`PATH="$PATH":/x`, whose component boundaries are not derivable from a
	// literal split) both refuse.
	value, ok := cmdparse.LiteralAssignmentValueText(ev.Value)
	if !ok {
		return false
	}
	components, ok := splitPathValueComponents(value)
	if !ok {
		return false
	}
	selfRef, braceRef := "$"+ev.Name, "${"+ev.Name+"}"
	preserved := false
	for _, atoms := range components {
		if text, literalOnly := componentLiteral(atoms); literalOnly {
			if text == selfRef || text == braceRef {
				preserved = true
				continue
			}
			if isStaticAbsolutePath(text) {
				continue
			}
			// NARROW MIDDLE OPTION (pg2-qhhil): the component is not itself a literal
			// static absolute path, but it may be a variable THIS SAME COMMAND
			// assigned, earlier, to one — e.g. `bindir=/tmp/x/bin;
			// PATH="$bindir:$PATH"`. cmdparse.ExpandInCommand resolves it against
			// vars ALL-OR-NOTHING: an AMBIENT variable (never assigned in this
			// command's own text — $PWD, $JAVA_HOME, $TMP, …) is simply absent from
			// vars, so ok is false and this component falls through to the decisive
			// Ask below, unchanged from before this bead.
			if expanded, ok := cmdparse.ExpandInCommand(text, vars); ok && isStaticAbsolutePath(expanded) {
				continue
			}
			return false
		}
		// The component embeds at least one top-level command/process substitution
		// (componentLiteral only reports literalOnly=false for that case) —
		// pg2-kzqw2's relief. See componentSafeSubstitution for the shape it
		// requires and the empty-substitution hazard it guards against.
		if componentSafeSubstitution(atoms) {
			continue
		}
		return false
	}
	return preserved
}

// pathValueAtom is one piece of a PATH/HOME value's text, inside a single
// ':'-delimited component: either literal source text, or one top-level
// command/process substitution the component embeds verbatim. Splitting the
// whole value into these BEFORE looking for ':' component boundaries is what
// keeps a literal ':' inside a substitution's OWN body (`$(date +%H:%M)`) from
// ever being mistaken for a component boundary — a colon is only ever
// significant inside a literal atom; a substitution atom is opaque to it
// (pg2-kzqw2).
type pathValueAtom struct {
	literal string
	sub     *cmdparse.Substitution // nil for a literal atom
}

// splitPathValueComponents splits value — text literalValue already produced,
// so it carries no surviving quote or backslash — into ':'-delimited PATH/HOME
// components, each expressed as a sequence of pathValueAtoms rather than a raw
// substring.
//
// cmdparse.EnumerateSubstitutions finds every TOP-LEVEL command/process
// substitution in value, in source order and never nested (ADR 0039 /
// pg2-1019a). Each is relocated by an exact, left-to-right, cursor-advancing
// text search that never restarts from the front — safe specifically BECAUSE
// substitutions are returned in that order and never overlap, so an earlier
// one's occurrence is always consumed before a later one's search begins, and
// a later substitution's wrapped text can never be mistaken for an EARLIER,
// not-yet-consumed occurrence of the same text.
//
// ok is false only when a substitution's reconstructed source text
// (`$(Body)` / “ `Body` “ / `<(Body)` / `>(Body)`) cannot be relocated at or
// after the cursor — meaning EnumerateSubstitutions and this reconstruction
// have desynced — and the caller MUST fail closed (treat the value as
// unclassifiable) rather than guess a split.
func splitPathValueComponents(value string) ([][]pathValueAtom, bool) {
	subs := cmdparse.EnumerateSubstitutions(value)
	var atoms []pathValueAtom
	cursor := 0
	for i := range subs {
		wrapped, ok := wrappedSubstitutionText(subs[i])
		if !ok {
			return nil, false
		}
		idx := strings.Index(value[cursor:], wrapped)
		if idx < 0 {
			return nil, false
		}
		pos := cursor + idx
		if pos > cursor {
			atoms = append(atoms, pathValueAtom{literal: value[cursor:pos]})
		}
		atoms = append(atoms, pathValueAtom{sub: &subs[i]})
		cursor = pos + len(wrapped)
	}
	if cursor < len(value) {
		atoms = append(atoms, pathValueAtom{literal: value[cursor:]})
	}

	// A substitution atom is opaque to ':'; only a literal atom's OWN text is
	// ever split on it, which is what keeps `$(date +%H:%M)` whole.
	var components [][]pathValueAtom
	var current []pathValueAtom
	for _, a := range atoms {
		if a.sub != nil {
			current = append(current, a)
			continue
		}
		pieces := strings.Split(a.literal, ":")
		for i, piece := range pieces {
			if i > 0 {
				components = append(components, current)
				current = nil
			}
			if piece != "" {
				current = append(current, pathValueAtom{literal: piece})
			}
		}
	}
	components = append(components, current)
	return components, true
}

// wrappedSubstitutionText reconstructs a Substitution's exact original source
// span — the text EnumerateSubstitutions extracted Body from — so
// splitPathValueComponents can relocate it inside the value it came from. ok
// is false only for a SubstitutionKind this package does not know about (a
// future addition to cmdparse's enum), which must fail the caller closed
// rather than guess a wrapping.
func wrappedSubstitutionText(sub cmdparse.Substitution) (string, bool) {
	switch sub.Kind {
	case cmdparse.SubstCommand:
		return "$(" + sub.Body + ")", true
	case cmdparse.SubstBacktick:
		return "`" + sub.Body + "`", true
	case cmdparse.SubstProcessIn:
		return "<(" + sub.Body + ")", true
	case cmdparse.SubstProcessOut:
		return ">(" + sub.Body + ")", true
	default:
		return "", false
	}
}

// componentLiteral reports the concatenated literal text of atoms when the
// component carries NO substitution at all, in which case ok is true. By
// splitPathValueComponents' construction, a no-substitution component holds at
// most one atom (a second literal atom can only ever appear around an
// intervening substitution atom), so this never needs to concatenate more than
// one string.
func componentLiteral(atoms []pathValueAtom) (text string, ok bool) {
	switch len(atoms) {
	case 0:
		return "", true
	case 1:
		if atoms[0].sub != nil {
			return "", false
		}
		return atoms[0].literal, true
	default:
		return "", false
	}
}

// componentSafeSubstitution reports whether atoms is the pg2-kzqw2 relief
// shape: an OPTIONAL literal prefix, EXACTLY ONE top-level COMMAND substitution
// (never a process substitution — IsCommandSubstitution excludes `<(...)` /
// `>(...)`, which no static allowlist governs) whose body is already certified
// safe under the static allowlist (cmdparse.IsSafeSubstitutionBody — which also
// refuses any NESTED substitution, so `$(dirname "$(readlink -f x)")` never
// reaches an Approve through this path), and an OPTIONAL literal suffix —
// treated as acceptable WITHOUT knowing the substitution's resolved value, per
// the operator's 2026-08-17 ruling (see the askVars doc comment's pg2-kzqw2
// bullet).
//
// THE EMPTY-SUBSTITUTION HAZARD is the crux of pg2-kzqw2, and why this is NOT
// simply "IsSafeSubstitutionBody(sub) implies Approve". Unlike a purely
// syntactic hazard ($PWD, $JAVA_HOME — never assigned by the command's own
// text, so they are unresolvable rather than merely unpredictable), a command
// substitution can resolve to the EMPTY STRING on any given invocation —
// nothing about being on the static safe-cmd allowlist rules that out.
// isStaticAbsolutePath already refuses an empty ':' component ON PURPOSE (an
// empty PATH entry means "current directory" to the shell — the identical CWD
// hazard `PATH="$PATH:"` triggers, pinned by TestEnvVars_AskVars_NotPreserveForm_Ask).
// So this predicate demands that the component's LITERAL SKELETON ALONE — with
// the substitution's own contribution zeroed out — is STILL a safe, non-empty
// absolute path:
//
//   - `$(dirname /usr/local/bin/go)/bin` empties to "/bin" — a real absolute
//     path whatever the substitution actually returns — so it Approves.
//   - a BARE `$(safe-cmd)` component with no literal prefix or suffix at all
//     empties to "" — exactly the CWD hazard — so it keeps asking, even though
//     the substitution itself is certified safe. `printf` invoked with an
//     empty-string argument is a concrete, real member of the static
//     allowlist that genuinely returns empty, which is exactly the case this
//     guards.
//
// A component with MORE than one substitution atom (`$(a)$(b)/bin`, no ':'
// between them) is unclassifiable by this predicate and returns false — a
// narrower scope than strictly necessary, matching pg2-qhhil's own narrow
// scoping of its middle option, and relaxable later if measurement calls for
// it.
func componentSafeSubstitution(atoms []pathValueAtom) bool {
	var sub *cmdparse.Substitution
	var literal strings.Builder
	for _, a := range atoms {
		if a.sub == nil {
			literal.WriteString(a.literal)
			continue
		}
		if sub != nil {
			return false // more than one substitution in this component: unclassifiable
		}
		sub = a.sub
	}
	if sub == nil || !sub.IsCommandSubstitution() {
		return false
	}
	if !cmdparse.IsSafeSubstitutionBody(sub.Body) {
		return false
	}
	return isStaticAbsolutePath(literal.String())
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

// isHermeticEnvReplacement reports whether an askVar's REPLACEMENT value is safe
// under `env -i`/`env --ignore-environment` (the leaf's EnvCleared, passed by the
// caller as envCleared and already checked true before this is called): the
// invocation discards the WHOLE caller environment before applying this leaf's
// own EnvVars, so there is no caller PATH/HOME left for preservesCallerValue's
// EXTEND shape to preserve — the assignment is instead constructing a
// known-minimal environment from scratch, which is the POINT of `env -i`, not a
// disguised hijack.
//
// # OPERATOR RULING 2026-08-17 (pg2-d71my, decided together with pg2-qhhil)
//
// Authorizes relief for exactly this shape, narrower than preservesCallerValue:
// gated on the value being STATIC and REASONABLE, tested with the identical
// isStaticAbsolutePath denylist preservesCallerValue's EXTEND shape already
// uses, applied to EVERY `:`-separated component (for a bare, non-PATH-shaped
// value like HOME this is just the whole value, since splitting a component-free
// string by ":" yields itself). It is INDEPENDENT of the in-command $VAR
// dataflow pg2-qhhil wired in: `env -i` is itself the hermetic marker the ruling
// relies on, so no earlier assignment need be inspected to grant this relief.
//
// What it does NOT do: `injectorVars`/`injectorAskVars` (LD_PRELOAD, BASH_ENV,
// …) are checked in evaluateAssignment's earlier switch cases, which this
// function is never reached for — env -i does not make an injector safe, and
// this predicate has no opinion on names outside askVars at all. And a value
// that still self-references the caller ($PATH-shaped) is not a REPLACEMENT in
// the first place and is preservesCallerValue's shape to grant, evaluated first
// by the caller's switch — this predicate is reached only for the shapes that
// fell through it.
func isHermeticEnvReplacement(ev cmdparse.EnvAssignment) bool {
	value, ok := cmdparse.LiteralAssignmentValueText(ev.Value)
	if !ok || value == "" {
		return false
	}
	for _, component := range strings.Split(value, ":") {
		if !isStaticAbsolutePath(component) {
			return false
		}
	}
	return true
}

// isHermeticHomeReplacement reports whether a HOME REPLACEMENT value is grounded
// in a `mktemp -d` fresh temporary directory this SAME command created — either
// DIRECTLY (`HOME=$(mktemp -d)`, cmdparse.IsFreshTempDirAssignment) or via a
// variable the command bound to one EARLIER (`T=$(mktemp -d); … HOME="$T/h"`),
// composed here with cmdparse.ExpandInCommand exactly the way
// preservesCallerValue composes it against the in-command-assigned $VAR middle
// option pg2-qhhil wired in — the identical seam, reused rather than
// re-derived, gated here on tempDirVars (cmdparse.InCommandTempDirVars via
// primarycommit.LeafTempDirVars) instead of on vars.
//
// # OPERATOR RULING 2026-08-17 (pg2-d71my, decided together with pg2-qhhil)
//
// Authorizes this relief: a `mktemp -d` directory is freshly created and
// session-unique, so nothing — attacker or otherwise — could have pre-staged
// content there in advance, which is precisely what makes a HOME replacement
// pointed at one NOT the PATH-hijack shape the decisive Ask otherwise exists to
// catch. It is scoped to HOME only (the caller checks ev.Name == "HOME" before
// calling this) — PATH's own replacement relief is isHermeticEnvReplacement's
// `env -i` shape, a deliberately different and narrower gate, not this one.
//
// tempDirVars nil is the ordinary case (no qualifying earlier mktemp -d
// assignment): the direct-value check still runs (it needs no vars at all), and
// the var-ref composition below correctly reports false via ExpandInCommand's
// own `len(vars) == 0` fail-safe.
func isHermeticHomeReplacement(ev cmdparse.EnvAssignment, tempDirVars map[string]string) bool {
	if cmdparse.IsFreshTempDirAssignment(ev) {
		return true
	}
	if ev.Expansion != cmdparse.ExpansionVarRef {
		return false
	}
	value, ok := cmdparse.LiteralAssignmentValueText(ev.Value)
	if !ok {
		return false
	}
	_, expanded := cmdparse.ExpandInCommand(value, tempDirVars)
	return expanded
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("env-vars: read bash command: %w", err)
	}

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
	refused := false
	for i, pc := range parsed {
		wholeLeaf := assignmentIsWholeLeaf(pc)
		// The variables THIS SAME command establishes for leaf i (pg2-qhhil, wiring
		// the pg2-wq3ki InCommandVars/ExpandInCommand seam) — a component of a
		// PATH/HOME value that merely references one of them is exactly as
		// inspectable as a literal path (see preservesCallerValue). Under the
		// engine `input.InCommandVars` already carries this, computed once per
		// synthetic leaf against the WHOLE expression; a direct/test caller handing
		// this rule a multi-leaf compound is covered by primarycommit.LeafVars
		// reading the leaves THIS reparse produced off `parsed` — the identical
		// overlay internal/rules/primarypush already shares with primarycommit for
		// the same base/local reason (pg2-eqacu).
		vars := primarycommit.LeafVars(input.InCommandVars, parsed, i)
		// The sibling scan for a DIFFERENT fact about the same earlier leaves: which
		// of their names are bound to a fresh `mktemp -d` directory rather than to a
		// literal value (pg2-d71my's HOME=temp-dir relief, gated on this identical
		// seam per the operator ruling). Same base/local fallback reasoning as vars
		// above.
		tempDirVars := primarycommit.LeafTempDirVars(input.InCommandTempDirVars, parsed, i)
		for _, ev := range pc.EnvVars {
			sub, subRefused := r.evaluateAssignment(ev, input, vars, tempDirVars, pc.EnvCleared)
			refused = refused || subRefused
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
	if result.Decision == hookio.NoOpinion && held != nil && !refused {
		// The held Approve is surfaced only when NOTHING on this leaf was refused.
		// Without the `!refused` guard a leaf carrying both a verified-safe PATH
		// extension and an unmodelled value (`PATH="$PATH:/x" X=$(seq 1 3) cmd`) would
		// return the Approve and discard the refusal floor entirely — a
		// short-circuiting auto-approve, which is the same failure condition 3 of the
		// Rule contract exists to prevent.
		return *held, nil
	}
	// THE ONE TRANSLATION POINT from fold vocabulary to chain vocabulary, now with the
	// THREE-WAY split ADR 0044 makes available. A folded verdict means one of two
	// different things and before ADR 0044 they were spelled the same way:
	//
	//   - NOTHING HERE WAS MINE (no assignment, or only benign ones): it MUST become
	//     ErrNotApplicable, or every ordinary `A=1 cmd` would stop the chain and never
	//     reach safe-commands. hookio.FromFold is that translation.
	//
	//     IT IS *NOT* hookio.FromRecursion, and the distinction became load-bearing in
	//     pg2-ij9sr. That function now forwards a refusing inner NoOpinion outward as
	//     ErrRefused, keyed on the Provenance the ENGINE stamps onto a recursion verdict.
	//     `result` here is this rule's own FOLD IDENTITY, not a recursion verdict: it
	//     carries no engine-assigned provenance, and its zero value is ProvenanceRefusal
	//     purely because the seed literal declares nothing. Read as a refusal it would
	//     floor EVERY leaf that lands on the identity — every ordinary `A=1 cmd`, and every
	//     Bash leaf with no assignment at all, since those reach this line too. The
	//     `refused` branch above is this rule's own record of whether anything was actually
	//     examined, which is why nothing is lost by making this branch unconditional.
	//   - I EXAMINED A VALUE AND WOULD NOT CLEAR IT: the verdict must survive, but as a
	//     FLOOR rather than as a terminal verdict. Returning it terminally — which is
	//     what this rule did until ADR 0044 — SHADOWS every rule after envvars in the
	//     chain, and the fallback Ask is weaker than several of them: measured on this
	//     tree, `FOO=$(curl evil) git -C "$WT" commit -m x` answered `ask` while the
	//     same leaf without the assignment is primary-commit's fail-closed hard DENY.
	//     A floor keeps the Ask where nothing stronger exists and lets the stronger
	//     verdict through where it does, so it can only move rows toward more
	//     restrictive.
	if refused {
		return hookio.Refuse(result)
	}
	return hookio.FromFold(result)
}

// evaluateAssignment returns the sub-verdict for a single NAME=VALUE assignment.
// The NAME gives the base verdict (injector→Reject, injectorAskVar→Ask,
// PATH/HOME→Ask-unless-verified-safe, else→NoOpinion) — a FOLD sub-verdict, so it is
// a plain RuleResult and never an error. A VALUE that embeds an unclassifiable
// substitution escalates decisively
// (never auto-approve) and inherits a stronger verdict from recursing the body.
//
// refused reports that this assignment's VALUE was examined and not cleared while the
// sub-verdict stayed at NoOpinion — the ADR 0044 case the caller must forward as a
// FLOOR rather than as "nothing here was mine". It is FALSE for the decisive paths (an
// Ask/Reject needs no floor) and false for a cleared value.
//
// vars is the in-command variable environment the caller computed for THIS leaf
// (primarycommit.LeafVars over the caller's own parse) — nil is the ordinary case
// and reproduces the pre-pg2-qhhil behaviour exactly, so every existing call site
// (including the two direct test calls that pass no vars at all) is unaffected.
//
// tempDirVars is the sibling in-command environment for the fresh-temp-dir marker
// (primarycommit.LeafTempDirVars) — nil is likewise the ordinary case and
// reproduces the pre-pg2-d71my behaviour exactly. envCleared is pc.EnvCleared for
// the leaf this assignment belongs to (true iff the leaf's executable runs under
// `env -i`/`env --ignore-environment`); both are pg2-d71my's REPLACEMENT-form
// relief inputs, independent of each other and of vars/preservesCallerValue.
func (r *Rule) evaluateAssignment(ev cmdparse.EnvAssignment, input *hookio.HookInput, vars, tempDirVars map[string]string, envCleared bool) (result hookio.RuleResult, refused bool) {
	name := r.Name()

	// Base verdict from the variable NAME.
	// NoOpinion, not an error: this is a fold sub-verdict (see Evaluate), and the
	// fold has no representation for the chain's "continue".
	result = hookio.RuleResult{Decision: hookio.NoOpinion, Module: name}
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
		// absolute components — or a component THIS SAME COMMAND assigned,
		// earlier, to one (pg2-qhhil's narrow middle option) — is affirmatively
		// safe. pg2-d71my adds TWO further, narrower REPLACEMENT-form reliefs
		// (isHermeticEnvReplacement, isHermeticHomeReplacement — see their own
		// docs) per the operator's 2026-08-17 ruling. Everything else — every
		// other REPLACEMENT, and every value with a component we cannot classify
		// — keeps the decisive Ask. This is a pure NAME/VALUE/in-command-text
		// decision and must stay independent of r.exprEval, so the verdict is
		// identical with New() and NewWithEvaluator(): vars/tempDirVars are
		// derived from the command's own parse
		// (cmdparse.InCommandVars|InCommandTempDirVars via
		// primarycommit.LeafVars|LeafTempDirVars), never from evaluator
		// recursion. Whether the Approve is actually surfaced is scoped by
		// the caller (see Evaluate / the Rule contract).
		switch {
		case preservesCallerValue(ev, vars):
			result = hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "sensitive env var preserves the caller's value and adds only static absolute paths: " + sanitizeReasonName(ev.Name),
				Module:   name,
			}
		case envCleared && isHermeticEnvReplacement(ev):
			result = hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "sensitive env var is a static replacement under a hermetic env -i invocation: " + sanitizeReasonName(ev.Name),
				Module:   name,
			}
		case ev.Name == "HOME" && isHermeticHomeReplacement(ev, tempDirVars):
			result = hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "HOME replacement is grounded in a fresh mktemp -d temporary directory: " + sanitizeReasonName(ev.Name),
				Module:   name,
			}
		default:
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
	//   - A NoOpinion body is merely UNCLASSIFIED, not safe, and NoOpinion is
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
	//
	// # THE ADR 0044 SPLIT OF THE UN-CLEARED HALF, AND WHY IT IS OBSERVABLE-ONLY
	//
	// Until ADR 0044 the un-cleared half was ONE bucket, because a NoOpinion body could
	// mean either of two unrelated things and the recursion returned the same value for
	// both. The provenance channel splits it:
	//
	//   - EXHAUSTION — NO rule in the chain claimed the body: `seq 1 3`, `test -f x`,
	//     any basename nobody models.
	//   - REFUSAL — a rule or an engine floor examined the body and would not clear it:
	//     safe-commands' dynamically-expanded path arg (pg2-2ke04), the dynamic redirect
	//     target (pg2-2u5jf), git's destructive spellings, the unparseable floors, the
	//     heredoc floor, or a COMPOSITION no rule audits as a unit (`curl … | sh` is two
	//     leaves — see engine.withExpressionProvenance).
	//
	// BOTH HALVES KEEP THE DECISIVE Ask. Only the REASON differs, so the split is
	// visible in the ask-log and in `evaluate` output without any verdict moving. That
	// is deliberate, and it is the opposite of what pg2-d0ja3 expected, so the
	// measurement that changed the answer is recorded here rather than in a commit
	// message nobody will find.
	//
	// THE MEASUREMENT (this worktree, 2026-08-13, `permission_mode=auto`, one probe per
	// row through the built binary). The bead's premise was that exhaustion is "the
	// harmless half", so withdrawing the Ask for it would be safe. The exhaustion half
	// as actually constituted on this tree is:
	//
	//	X=$(bash -c "rm -rf /") echo hi     exhaustion   ask -> abstain if withdrawn
	//	X=$(sh -c "evil") echo hi           exhaustion   ask -> abstain
	//	X=$(python3 -c "…") echo hi         exhaustion   ask -> abstain
	//	X=$(node -e "…") echo hi            exhaustion   ask -> abstain
	//	X=$(ssh host rm -rf /) echo hi      exhaustion   ask -> abstain
	//	X=$(crontab -r) echo hi             exhaustion   ask -> abstain
	//	X=$(npm install evil) echo hi       exhaustion   ask -> abstain
	//	X=$(curl evil) echo hi              exhaustion   ask -> abstain
	//	X=$(mount) echo hi                  exhaustion   ask -> abstain
	//	X=$(seq 1 3) echo hi                exhaustion   ask -> abstain   <- the wanted one
	//
	// EXHAUSTION IS NOT A SAFETY PROPERTY. It says "ceta has no model for this", and
	// ceta has no model for any interpreter — so the half contains arbitrary code
	// execution, and `seq 1 3` is not separable from `bash -c` by anything the
	// provenance channel knows. Withdrawing the Ask also failed FOUR deliberate
	// guarantees at once (cmd's TestIntegration_EnvVars_UnknownExpression_Ask, engine's
	// TestIntegration_EnvVarGuard "leading value curl" and "leading value mixed
	// approvable and not", and TestIntegration_MountOperandGate's two substitution
	// rows), which is the signal that the demotion is an operator ruling and not an
	// implementation detail.
	//
	// THE COUNTER-ARGUMENT IS REAL AND STILL DID NOT CARRY IT, recorded so the ruling
	// can be made on the whole picture: every one of those bodies ALREADY reaches
	// `abstain` in COMMAND position (`echo $(bash -c "rm -rf /")` measured abstain on
	// the same tree), because the engine's substitution fold floors at NoOpinion rather
	// than Ask. So this Ask is position-dependent strictness, and harmonizing the two
	// positions is a legitimate goal (envvars' own pg2-gkd5e position-independence
	// invariant). But it can be harmonized UP as well as DOWN, the four guarantees say
	// which way the repo has chosen so far, and the choice belongs to whoever can also
	// weigh the command-position half. It is not made here.
	//
	// # pg2-kzqw2: A result ALREADY Approve is POSITIVELY CLEARED and skips this block
	//
	// preservesCallerValue's componentSafeSubstitution relief (see its own doc) can
	// itself set result to Approve above, for a value that carries a `$PATH`/`$HOME`
	// self-reference ALONGSIDE a certified-safe command substitution — e.g.
	// `PATH="$(dirname /usr/local/bin/go)/bin:$PATH"`. That value's census has both a
	// param expansion and a command substitution, so expansionCensus.kind() classifies
	// it ExpansionUnknown (the "beside ANY other expansion" rule), exactly like an
	// unmodelled value. Before this guard, THIS block ran unconditionally on every
	// ExpansionUnknown value and clobbered that Approve back to Ask via
	// MostRestrictive — with no evaluator wired (New()) there is no recursion to
	// re-clear it, so the componentSafeSubstitution relief was unreachable in
	// practice for exactly the case the operator ruling authorized it for. The static
	// allowlist check componentSafeSubstitution already performed is narrower and more
	// precise than this block's generic "recurse and see" fallback, so a value it
	// already cleared must not be re-escalated by a check that has no way to know it
	// was cleared by a DIFFERENT static mechanism than its own recursion.
	if ev.Expansion == cmdparse.ExpansionUnknown && result.Decision != hookio.Approve {
		var subResults []hookio.RuleResult
		clearedByRecursion := false
		exhaustionOnly := false
		if r.exprEval != nil {
			subs := cmdparse.EnumerateSubstitutions(ev.Value)
			clearedByRecursion = len(subs) > 0
			exhaustionOnly = len(subs) > 0
			for _, sub := range subs {
				stack := []hookio.StackFrame{{RuleName: name, Command: "env-value", Expression: ev.Raw}}
				// TEXT RE-ENTRY DECISION (pg2-30wro's adjacent audit item, ADR 0039 I13).
				// sub.Body is a VERBATIM SOURCE SLICE straight out of
				// cmdparse.EnumerateSubstitutions(ev.Value) — nothing here builds,
				// rewrites, or joins text before handing it to the evaluator, so this is
				// NOT an instance of the "no rule constructs command text" violation I13
				// forbids. It is the permanent I7 text entry point's SANCTIONED use: a
				// rule that needs to delegate on an exact, already-existing slice of the
				// source it was given, not a synthesized one.
				//
				// DECISION: LEAVE AS-IS. `pg2-m1i6r` (closed 2026-08-20, ff-merged to main
				// at `9189ab72`, landing `hookio.Evaluator`'s new structural delegate entry
				// point `EvaluateStructure`) names this exact call site in its own Scope
				// section as one of `EvaluateExpression`'s two legitimate PERMANENT callers
				// post-migration — "the hook boundary and verbatim-source-slice re-entries
				// (e.g. envvars.go:779, which constructs nothing)" — so no migration onto
				// `EvaluateStructure` is owed here. `9189ab72` is not yet an ancestor of
				// this branch's base (`4fdca75c`), so `EvaluateStructure` does not exist on
				// this tree; migrating onto it now is not an option regardless of the
				// policy question, and is left as a future no-op once that lands.
				subResult := r.exprEval.EvaluateExpression(sub.Body, stack, input)
				if subResult.Decision != hookio.Approve {
					clearedByRecursion = false
				}
				if !bodyIsUnmodelled(subResult) {
					exhaustionOnly = false
				}
				subResults = append(subResults, subResult)
			}
		}
		switch {
		case clearedByRecursion:
			// Positively cleared: no escalation at all, exactly as before.
		case exhaustionOnly:
			// SAME Ask, DIFFERENT reason. The reason is the whole deliverable of this
			// branch: it partitions the live ask cohort into the half a future ruling
			// could safely relieve and the half it must not, so the ruling can be made
			// on counted rows instead of on a prediction.
			result = hookio.MostRestrictive(result, hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "env var value runs a command no rule models: " + sanitizeReasonName(ev.Name),
				Module:   name,
			})
			refused = true
		default:
			result = hookio.MostRestrictive(result, hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "env var value contains an unevaluated/unsafe expression: " + sanitizeReasonName(ev.Name),
				Module:   name,
			})
			refused = true
		}
		// Folded after the fallback so a body that is itself Ask keeps the
		// fallback's reason (MostRestrictive keeps `current` on a tie), preserving
		// the pre-pg2-5huwx reason precedence for every non-cleared value.
		for _, subResult := range subResults {
			result = hookio.MostRestrictive(result, subResult)
		}
	}

	return result, refused
}

// bodyIsUnmodelled reports whether a recursed substitution body's verdict is one the
// assignment may forward AS-IS instead of escalating above: either an affirmative
// Approve, or a NoOpinion that the engine attributes to chain EXHAUSTION.
//
// The Approve arm is here so a value mixing an approved body with an unmodelled one
// (`X=$(git rev-parse HEAD)$(seq 1 3)`) takes the floor rather than the Ask — the pair
// is still "nothing was refused". Every other shape — a NoOpinion whose provenance is
// a REFUSAL, and every Ask/Reject — returns false, which routes the value to the
// decisive fallback.
//
// The Provenance test is the FAIL-SAFE direction by construction: ProvenanceRefusal is
// the zero value, so a verdict from a site that declares nothing reads as a refusal and
// escalates. Only engine.Evaluate's loop exhaustion claims otherwise, and only under
// engine.withExpressionProvenance's shape conditions.
func bodyIsUnmodelled(subResult hookio.RuleResult) bool {
	if subResult.Decision == hookio.Approve {
		return true
	}
	return subResult.Decision == hookio.NoOpinion &&
		subResult.Provenance == hookio.ProvenanceExhaustion
}
