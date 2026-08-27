package assume

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type Rule struct {
	// exprEval is this rule's I13 structural delegate (pg2-bm0ai) for an
	// assume `--exec "<inner-command-string>"` payload, mirroring
	// kubectl/nix/docker/safecmds' own production wiring (all take the engine
	// as their Evaluator). nil for New() — the bare-assume Reject branch below
	// is unaffected; an --exec payload found with no evaluator wired fails
	// closed to NotApplicable (see evaluateExec's doc), never approve.
	exprEval hookio.Evaluator
}

func New() *Rule { return &Rule{} }

// NewWithEvaluator is New plus the structural delegate used to evaluate an
// `assume ... --exec "<inner-command-string>"` payload through the full rule
// chain (ADR 0039 I13's EvaluateStructure entry point) — the SAME pattern
// kubectl.New / nix.NewWithEvaluator / docker.New / safecmds.NewWithEvaluator
// already use for an inner command found inside a flag value.
func NewWithEvaluator(eval hookio.Evaluator) *Rule { return &Rule{exprEval: eval} }

func (r *Rule) Name() string { return "assume" }

// refuse is this rule's ADR 0044 refusal-and-continue return — same shape as
// kubectl's/safecmds' own refuse: assume KNOWS this leaf is an --exec payload
// it examined and cannot clear, so reporting a plain not-applicable would make
// it indistinguishable from a leaf no rule ever looked at (an EXHAUSTION,
// which a consumer may treat as clearable) — but it must not stop rules
// ordered after it in the chain (secrets, envvars, safe-commands, …) from
// still forming their own opinion. It can only make the leaf MORE
// restrictive: the engine folds it as a floor and keeps going.
func (r *Rule) refuse(reason string) (hookio.RuleResult, error) {
	return hookio.Refused(r.Name(), reason)
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("assume: read bash command: %w", err)
	}
	for _, pc := range parsed {
		args, ok := assumeArgs(pc)
		if !ok {
			continue
		}
		if execStr, found := execFlagValue(args); found {
			return r.evaluateExec(execStr, input)
		}
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "assume: AWS assume-role commands must be run outside of Claude sessions. Exit the session, run assume externally, then resume.",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

// assumeArgs reports whether pc is an `assume` invocation and, if so, returns
// the arguments assume itself sees. Two shapes, per pg2-bm0ai:
//
//   - direct: Executable's basename is "assume".
//   - via the `source`/`.` shell builtins, which run the named script
//     ("assume") with the remaining args as positionals. cmdparse has no
//     special model for shell builtins — Executable is literally "source" or
//     "."  and Args[0] is the script name — which is WHY the direct check
//     above never matches this form on its own (see the package doc / the
//     bead this implements for the measured leaf shape).
//
// A `bash -c '...'`-wrapped invocation is deliberately OUT of scope here: it
// produces a leaf whose Executable is "bash", not "source"/"assume", and
// unwrapping arbitrary shell scripts to search for an assume call one level
// deeper is a different, broader mechanism this bead does not add (measured:
// `cmdparse.Parse("bash -c 'source assume ...'")` yields exactly one leaf,
// Executable "bash").
func assumeArgs(pc cmdparse.ParsedCommand) (args []string, ok bool) {
	if filepath.Base(pc.Executable) == "assume" {
		return pc.Args, true
	}
	if (pc.Executable == "source" || pc.Executable == ".") &&
		len(pc.Args) > 0 && filepath.Base(pc.Args[0]) == "assume" {
		return pc.Args[1:], true
	}
	return nil, false
}

// execFlagValue returns assume's `--exec <value>` / `--exec=<value>` payload
// — the ONE string assume itself hands to a shell to run under the assumed
// role — and whether the flag was present at all. Only the flag's own value
// is returned, mirroring nix-shell's singleArgAfterFlag (nix.go's
// evaluateNixShell): assume's --exec, like nix-shell's --run/--command, takes
// exactly one string value, never "the rest of argv".
func execFlagValue(args []string) (value string, ok bool) {
	for i, a := range args {
		if a == "--exec" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if v, cut := strings.CutPrefix(a, "--exec="); cut {
			return v, true
		}
	}
	return "", false
}

// evaluateExec structurally delegates assume's --exec payload through the
// engine's rule chain (ADR 0039 I13's EvaluateStructure entry point), the
// SAME pattern kubectl (kc exec), nix (develop/shell -c), docker (run/exec)
// and safecmds (xargs sh -c) already use for an inner command found inside a
// flag value. This is the CAUTION the bead is explicit about: assume itself,
// and any --exec payload that is not independently approvable through the
// normal rule chain, must continue to Reject/Abstain exactly as before —
// only the unwrapped inner command is judged on its own merits.
//
// execStr is parsed AS-IS: it is already-unquoted text this rule's own outer
// parse of the assume/source invocation produced (I12), exactly the shape
// nix-shell's --run/--command handle the same way — see evaluateNixShell's
// doc for why a lone already-unquoted flag value is parsed directly rather
// than rejoined with anything else.
func (r *Rule) evaluateExec(execStr string, input *hookio.HookInput) (hookio.RuleResult, error) {
	if r.exprEval == nil {
		// DELIBERATELY NOT a refusal (ADR 0044) — the same construction-state
		// posture kubectl.evaluateExec / nix / safecmds use for their own nil
		// exprEval guard: this Rule was built via New, not NewWithEvaluator, so
		// it never looked at the --exec payload at all. Falling through to
		// NotApplicable (never Approve, never the bare hard Reject either) keeps
		// this indistinguishable from "no rule examined it" rather than
		// asserting a judgement this construction cannot make.
		return hookio.NotApplicable()
	}
	leaves, ok := structuralExecCommand(execStr)
	if !ok {
		return r.refuse("assume: --exec value could not be parsed as a command (deferred to claude-code)")
	}
	stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "assume --exec", Expression: normalizeExpr(execStr)}}
	// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
	// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
	// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
	// hookio.FromRecursion states the translation in one place.
	return hookio.FromRecursion(r.exprEval.EvaluateStructure(execStr, leaves, stack, input))
}

// structuralExecCommand derives assume's --exec payload as PARSED STRUCTURE
// (I13) — never rule-constructed text handed back to the engine for
// re-evaluation. execStr is already-unquoted text produced by this rule's own
// outer parse, so it is parsed as-is with one real cmdparse.ParseShell call,
// mirroring kubectl.structuralInnerCommand's "no shell in between" bash/sh -c
// branch. ok is false when execStr itself fails to parse as shell syntax (a
// malformed, mismatched-quote, or otherwise nested-and-broken --exec value)
// or parses to no command leaves at all (blank/comment-only) — the caller
// MUST fail closed rather than call EvaluateStructure with an empty leaf set.
func structuralExecCommand(execStr string) (leaves []cmdparse.ParsedCommand, ok bool) {
	sp := cmdparse.ParseShell(execStr)
	if sp.Unparseable || len(sp.Leaves) == 0 {
		return nil, false
	}
	return sp.Leaves, true
}

func normalizeExpr(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}
