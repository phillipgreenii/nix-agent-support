package nix

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var nixApproved = map[string]bool{
	"log": true, "show-derivation": true, "path-info": true,
	"eval": true, "print-dev-env": true, "build": true,
	"develop": true, "fmt": true, "search": true,
	"doctor": true, "derivation": true, "hash": true,
	"why-depends": true, "store": true,
}

// NOTE: "nix run" is intentionally NOT in the approved list.
// Unlike "nix build" (produces a store derivation) or "nix develop --command"
// (inner command can be recursively evaluated), "nix run" executes an arbitrary
// flake package with no safe inner command to evaluate. Returning Abstain
// defers to Claude Code's built-in permission prompt.
//
// "nix shell" is also NOT in the approved list. "nix shell ... -c <cmd>" is
// handled specially by extracting and recursively evaluating the inner command,
// similar to "nix develop --command".

var nixFlakeApproved = map[string]bool{
	"show": true, "metadata": true, "check": true,
	"lock": true, "prefetch": true, "update": true,
	"info": true,
}

var rebuildReject = map[string]bool{
	"switch": true, "activate": true, "boot": true, "test": true,
}

var rebuildExecutables = map[string]bool{
	"darwin-rebuild": true, "nixos-rebuild": true, "home-manager": true,
}

var nixEnvRejectFlags = map[string]bool{
	"--install": true, "-i": true,
	"--upgrade": true, "-u": true,
	"--uninstall": true, "-e": true,
	"--set": true,
}

type Rule struct {
	exprEval hookio.Evaluator
}

func New() *Rule {
	return &Rule{}
}

func NewWithEvaluator(eval hookio.Evaluator) *Rule {
	return &Rule{exprEval: eval}
}

func (r *Rule) Name() string {
	return "nix"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("nix: read bash command: %w", err)
	}
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)

		if basename == "nix" {
			return r.evaluateNix(pc.Args, input)
		}
		if rebuildExecutables[basename] {
			return r.evaluateRebuild(basename, pc.Args)
		}
		if basename == "nix-env" {
			return r.evaluateNixEnv(pc.Args)
		}
		if basename == "nix-store" {
			return r.evaluateNixStore(pc.Args)
		}
		if basename == "nix-shell" && r.exprEval != nil {
			return r.evaluateNixShell(pc.Args, input)
		}
		if basename == "nix-instantiate" || basename == "nix-hash" ||
			basename == "nix-prefetch-url" || basename == "nix-prefetch-git" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: " + basename + " is read-only",
				Module:   r.Name(),
			}, nil
		}
		if basename == "statix" {
			return r.evaluateStatix(pc.Args)
		}
	}
	return hookio.NotApplicable()
}

// statixReadOnly are the statix subcommands that only lint/report; "fix" mutates
// files, so it is deliberately excluded (defers to the prompt).
var statixReadOnly = map[string]bool{
	"check": true, "explain": true,
}

func (r *Rule) evaluateStatix(args []string) (hookio.RuleResult, error) {
	sub := firstNonFlag(args)
	if statixReadOnly[sub] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: statix " + sub + " is read-only",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

// pg2-m132k OUTER-EXPR DECISION (I12/I13). evaluateNix's develop/shell
// branches and evaluateNixShell each push a StackFrame whose Expression is
// `outerExpr`, still computed as `normalizeExpr("nix[-shell] " +
// strings.Join(args, " "))` — a rule-constructed string, UNCHANGED by this
// bead. This is a DELIBERATE decision, not an oversight, recorded here per
// this bead's acceptance criteria:
//
//   - outerExpr is pushed onto `stack` for a DESCENDANT recursion to compare
//     itself against (hookio.Evaluator's cycle-detection key, engine.go's
//     detectCycle) — it is never itself compared against anything AT this
//     call site, so its own fidelity only matters for catching a
//     self-referential cycle, not for the leaf-dispatch correctness
//     innerCommandStructure's fix is about.
//   - That cycle check can only ever fire WITHIN a single
//     EvaluateStructure -> evaluateParsed -> foldSubstitutionScan recursion
//     chain (the `stack` parameter's own chaining mechanism). A nix-in-nix
//     delegation — the inner leaf calling back into THIS rule via
//     evaluateParsed's per-leaf `e.Evaluate(syntheticInput)` — does NOT
//     receive the ancestor `stack` at all: Engine.Evaluate takes no stack
//     parameter, so each nested nix.go invocation starts a brand-new,
//     single-frame stack. Recursion depth there is bounded by the finite
//     length of the input command (each "nix develop -c" nesting consumes
//     real characters), not by cycle detection — exactly as it was before
//     this bead, so leaving outerExpr untouched changes nothing about that
//     bound.
//   - Fixing outerExpr to a genuine source slice would need the same kind of
//     mechanism innerCommandStructure now has for the INNER command, but for
//     the OUTER one (which this rule never delegates into a second time at
//     this call site) — a real improvement, but an independent one from the
//     defect this bead's acceptance criteria names (the inner-command text
//     handed to the engine), and not required to fix it. It is left as a
//     pre-existing, tracked gap for a future, dedicated pass rather than
//     folded into this change.
func (r *Rule) evaluateNix(args []string, input *hookio.HookInput) (hookio.RuleResult, error) {
	subcmd := firstNonFlag(args)
	if subcmd == "" {
		return hookio.NotApplicable()
	}
	if subcmd == "develop" && r.exprEval != nil {
		// `nix develop -c <cmd>` and `nix develop --command <cmd>` both run an
		// inner command; recurse so it is evaluated (mirrors the `nix shell`
		// branch). Reading only --command let `nix develop -c rm -rf /etc` slip
		// through as a plain "approve develop".
		rest, ok := argsAfterFlag(args, "-c")
		if !ok {
			rest, ok = argsAfterFlag(args, "--command")
		}
		if ok {
			// pg2-m132k: leaves/innerSource are derived STRUCTURALLY (I13) — see
			// innerCommandStructure's doc for why a plain strings.Join of the
			// post-unquote args (the pre-fix behaviour) destroys quoting, and
			// EvaluateStructure (I13's structural delegate entry point) is used
			// in place of EvaluateExpression so no rule-built text is handed to
			// the engine's text entry point.
			leaves, innerSource := innerCommandStructure(rest)
			// pg2-m132k OUTER-EXPR DECISION (I12/I13): outerExpr below is left as
			// a rule-constructed string, UNCHANGED from before this bead — see
			// the package-level "OUTER-EXPR DECISION" comment for why this is
			// deliberate rather than an oversight.
			outerExpr := normalizeExpr("nix " + strings.Join(args, " "))
			stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "nix develop", Expression: outerExpr}}
			// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
			// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
			// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
			// hookio.FromRecursion states the translation in one place.
			return hookio.FromRecursion(r.exprEval.EvaluateStructure(innerSource, leaves, stack, input))
		}
		// No inner command: approve develop as usual
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: nix develop is approved",
			Module:   r.Name(),
		}, nil
	}
	if subcmd == "shell" && r.exprEval != nil {
		rest, ok := argsAfterFlag(args, "-c")
		if !ok {
			rest, ok = argsAfterFlag(args, "--command")
		}
		if ok {
			// pg2-m132k: see innerCommandStructure's doc and the "OUTER-EXPR
			// DECISION" comment below (nix develop's branch above cites both).
			leaves, innerSource := innerCommandStructure(rest)
			outerExpr := normalizeExpr("nix " + strings.Join(args, " "))
			stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "nix shell", Expression: outerExpr}}
			// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
			// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
			// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
			// hookio.FromRecursion states the translation in one place.
			return hookio.FromRecursion(r.exprEval.EvaluateStructure(innerSource, leaves, stack, input))
		}
		// No -c flag: just entering a shell with packages available — approve
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: nix shell (no command) is approved",
			Module:   r.Name(),
		}, nil
	}
	if subcmd == "flake" {
		flakeSub := firstNonFlagAfter(args, "flake")
		if nixFlakeApproved[flakeSub] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: nix flake " + flakeSub + " is approved",
				Module:   r.Name(),
			}, nil
		}
		return hookio.NotApplicable()
	}
	if nixApproved[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: nix " + subcmd + " is approved",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

var rebuildApproved = map[string]bool{
	"build": true, "check": true, "dry-activate": true, "dry-build": true,
}

func (r *Rule) evaluateRebuild(basename string, args []string) (hookio.RuleResult, error) {
	subcmd := firstNonFlag(args)
	if rebuildReject[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "nix: " + basename + " " + subcmd + " requires human",
			Module:   r.Name(),
		}, nil
	}
	if rebuildApproved[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: " + basename + " " + subcmd + " is safe (no activation)",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

func (r *Rule) evaluateNixEnv(args []string) (hookio.RuleResult, error) {
	for _, a := range args {
		if nixEnvRejectFlags[a] {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "nix: nix-env " + a + " modifies global profile",
				Module:   r.Name(),
			}, nil
		}
		if a == "--query" || a == "-q" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: nix-env query is read-only",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}

var nixStoreReadOnly = map[string]bool{
	"--query": true, "-q": true,
	"--print-env": true,
	"--verify":    true, "--verify-path": true,
	"--dump": true, "--export": true,
	"--read-log": true, "-l": true,
	"--dump-db": true,
}

func (r *Rule) evaluateNixStore(args []string) (hookio.RuleResult, error) {
	for _, a := range args {
		if nixStoreReadOnly[a] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: nix-store " + a + " is read-only",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}

func (r *Rule) evaluateNixShell(args []string, input *hookio.HookInput) (hookio.RuleResult, error) {
	// Unlike `nix develop`/`nix shell`'s -c/--command (which hand execve the
	// REST of argv directly, so a genuinely multi-token tail is real ARGV, not
	// shell text — see innerCommandStructure's doc), nix-shell's --run/--command
	// each take EXACTLY ONE string value that nix-shell itself hands to
	// `$SHELL -c` (confirmed against `nix-shell --help`: "--command cmd ...
	// executed in an interactive shell"; "--run cmd ... Like --command, but ...
	// non-interactive"). singleArgAfterFlag takes only that one value —
	// narrower than the pre-fix extractAfterFlag, which joined every remaining
	// arg (including any UNRELATED trailing nix-shell flag) into the command;
	// no existing test exercises that trailing-args case, and this is the
	// behaviour nix-shell itself implements.
	runStr, ok := singleArgAfterFlag(args, "--run")
	if !ok {
		runStr, ok = singleArgAfterFlag(args, "--command")
	}
	if ok {
		// runStr is the caller's OWN string, unmutated and unjoined (I13); it is
		// both the structural entry point's `source` (I12: the exact text
		// `leaves` was lowered from — see cmdparse.Parse's own doc for why this
		// is the sanctioned "text that exists nowhere in the [outer] source, so
		// the slice comes from the file produced by parsing that text" case) and
		// what gets parsed to produce `leaves`.
		leaves := cmdparse.Parse(runStr)
		outerExpr := normalizeExpr("nix-shell " + strings.Join(args, " "))
		stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "nix-shell", Expression: outerExpr}}
		// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
		// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
		// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
		// hookio.FromRecursion states the translation in one place.
		return hookio.FromRecursion(r.exprEval.EvaluateStructure(runStr, leaves, stack, input))
	}
	// nix-shell without --run: just entering a shell — approve
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "nix: nix-shell (no command) is approved",
		Module:   r.Name(),
	}, nil
}

func normalizeExpr(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func firstNonFlag(args []string) string {
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}

// argsAfterFlag returns the args following the first occurrence of flag,
// and whether flag was found with at least one following argument. It
// returns the SLICE, never a joined string: joining post-unquote args with
// bare spaces is exactly the pg2-m132k defect (see innerCommandStructure) —
// callers needing text must go through a function that quotes correctly.
func argsAfterFlag(args []string, flag string) ([]string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1:], true
		}
	}
	return nil, false
}

// singleArgAfterFlag returns ONLY the single arg immediately following the
// first occurrence of flag (never the rest of args) — the shape
// nix-shell's --run/--command need; see evaluateNixShell's doc.
func singleArgAfterFlag(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// innerCommandStructure derives nix develop/shell's -c/--command inner
// command STRUCTURALLY (I13) from rest — the caller's ALREADY-PARSED,
// post-unquote args following the flag — instead of the pre-fix
// `strings.Join(rest, " ")` text-join that was handed to EvaluateExpression.
//
// THE DEFECT (pg2-m132k). `strings.Join` rejoins each element of rest with a
// bare space and hands the result to a FRESH shell parse. When rest has more
// than one element, that reparse re-tokenizes on whitespace and operators —
// but nix's own `-c`/`--command` hands the REST OF ARGV to execve directly
// (confirmed against `nix develop --help`: "-c command args ... start the
// specified command and arguments"; never through a shell), so rest's
// element BOUNDARIES are already the correct argv boundaries from THIS
// command's own outer parse, not shell words to re-split. An operator
// embedded in one already-tokenized element — e.g. the `;` in
// `nix develop -c bash -c "echo hi; echo bye"`, where rest is
// ["bash", "-c", "echo hi; echo bye"] — resurfaces as a LIVE shell operator
// once the naively-joined text is reparsed, splitting one leaf into two and
// losing the semicolon's original protection.
//
// THE FIX. len(rest) == 1 is handled by parsing rest[0] directly: a single
// remaining token may itself be a whole command line the caller quoted as
// one shell word (`-c "git clean -fd"`), and reparsing it lets it split back
// into its real words — this is UNCHANGED from the pre-fix behaviour for the
// common one-argument case (nothing was ever joined) and is why the existing
// pinning tests (TestNixRule_ShellCommand, TestNixRule_DevelopCommand,
// TestIJ9SR_InnerRefusalIsForwardedByRule's nix rows) still pass unchanged.
//
// len(rest) > 1 is where the fix actually changes behaviour: rest's elements
// are safely re-quoted (quoteJoin — one shell word per element, embedded
// single quotes escaped) before being parsed, so the reparse reproduces
// exactly rest's own element boundaries — `cmdparse.Parse("'bash' '-c'
// 'echo hi; echo bye'")` yields ONE leaf (Executable "bash", Args ["-c",
// "echo hi; echo bye"]), never two. leaves is genuinely `cmdparse.Parse(source)`
// (I12: source is the exact text leaves was lowered from — never a smuggled
// mismatch), so it satisfies EvaluateStructure's contract precisely, even
// though — as I7 anticipates for the permanent text entry point — that text
// exists nowhere in the ORIGINAL raw command (it is a safely-requoted
// reconstruction of already-decoded values, not a substring of it).
//
// # pg2-ipn7w — NESTED bash -c / sh -c IS UNWRAPPED TOO
//
// The single leaf the steps above produce can ITSELF be `bash -c <script>` /
// `sh -c <script>` — nix's `-c`/`--command` hands the rest of argv to execve
// directly (this file's own package doc), so `nix develop -c bash -c
// "HOME=... cmd"` really does start a SECOND, nested shell on <script>, not
// an opaque leaf this rule chain should stop at. Before this bead, that
// second layer was never unwrapped here: the resulting leaf (Executable
// "bash", Args ["-c", "HOME=... cmd"]) was handed to EvaluateStructure
// as-is, so an env-var rule that inspects a leaf's OWN leading assignment
// (HOME, PATH, ...) never got a chance to see the assignment buried inside
// the script argument at all — it abstained, even though the equivalent
// `nix develop --command bash -c "HOME=... cmd"` was (coincidentally: see
// argsAfterFlag's own doc on why `-c` is tried before `--command`) already
// caught.
//
// The loop below unwraps repeatedly via cmdparse.UnwrapShellDashC (docker.go's
// `scriptArg` check, generalized — see that function's own doc), so a CHAIN
// of nested `bash -c 'bash -c "..."'` wrappers is fully resolved too. It
// keeps I12 intact at every step: `source` and `leaves` are reassigned
// together, so `leaves` is always genuinely `cmdparse.Parse(source)` for
// whichever `source` the loop last settled on. It stops the first time the
// leaf set is no longer EXACTLY one bash/sh -c-shaped leaf — either the
// wrapping ends (an ordinary command) or the script itself splits into more
// than one leaf (`bash -c "echo hi; echo bye"` becomes the two leaves `echo
// hi` and `echo bye`, each now independently visible to the rest of the
// rule chain instead of hiding inside one opaque argument).
func innerCommandStructure(rest []string) ([]cmdparse.ParsedCommand, string) {
	source := rest[0]
	if len(rest) > 1 {
		source = quoteJoin(rest)
	}
	leaves := cmdparse.Parse(source)
	for len(leaves) == 1 {
		script, ok := cmdparse.UnwrapShellDashC(leaves[0])
		if !ok {
			break
		}
		source = script
		leaves = cmdparse.Parse(source)
	}
	return leaves, source
}

// quoteJoin renders args as a shell command line that reparses back to
// EXACTLY the same argv: each element is single-quoted as one opaque word,
// with any embedded single quote escaped per shellQuoteArg's doc, rather
// than left bare — so no argument's own content — a semicolon, a pipe, a
// space — can be reinterpreted as a NEW word boundary or operator on
// reparse. See innerCommandStructure's doc for why this matters only when
// there is more than one element to join.
func quoteJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuoteArg(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuoteArg wraps s in single quotes. An embedded single quote is
// escaped by closing the quoted string, emitting a backslash-escaped
// literal quote, then reopening the quoted string — the standard technique
// for round-tripping an arbitrary string through a POSIX-shell reparse
// unchanged, for ANY byte sequence s may contain.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstNonFlagAfter(args []string, after string) string {
	found := false
	for _, a := range args {
		if !found {
			if a == after {
				found = true
			}
			continue
		}
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}
