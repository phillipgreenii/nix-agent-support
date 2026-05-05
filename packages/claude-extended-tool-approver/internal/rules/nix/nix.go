package nix

import (
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

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
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
		if basename == "nix-instantiate" || basename == "nix-hash" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: " + basename + " is read-only",
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateNix(args []string, input *hookio.HookInput) hookio.RuleResult {
	subcmd := firstNonFlag(args)
	if subcmd == "" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if subcmd == "develop" && r.exprEval != nil {
		innerCmd := extractAfterFlag(args, "--command")
		if innerCmd != "" {
			outerExpr := normalizeExpr("nix " + strings.Join(args, " "))
			stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "nix develop", Expression: outerExpr}}
			return r.exprEval.EvaluateExpression(innerCmd, stack, input)
		}
		// No --command flag: approve develop as usual
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: nix develop is approved",
			Module:   r.Name(),
		}
	}
	if subcmd == "shell" && r.exprEval != nil {
		innerCmd := extractAfterFlag(args, "-c")
		if innerCmd == "" {
			innerCmd = extractAfterFlag(args, "--command")
		}
		if innerCmd != "" {
			outerExpr := normalizeExpr("nix " + strings.Join(args, " "))
			stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "nix shell", Expression: outerExpr}}
			return r.exprEval.EvaluateExpression(innerCmd, stack, input)
		}
		// No -c flag: just entering a shell with packages available — approve
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: nix shell (no command) is approved",
			Module:   r.Name(),
		}
	}
	if subcmd == "flake" {
		flakeSub := firstNonFlagAfter(args, "flake")
		if nixFlakeApproved[flakeSub] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: nix flake " + flakeSub + " is approved",
				Module:   r.Name(),
			}
		}
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if nixApproved[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: nix " + subcmd + " is approved",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

var rebuildApproved = map[string]bool{
	"build": true, "check": true, "dry-activate": true, "dry-build": true,
}

func (r *Rule) evaluateRebuild(basename string, args []string) hookio.RuleResult {
	subcmd := firstNonFlag(args)
	if rebuildReject[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "nix: " + basename + " " + subcmd + " requires human",
			Module:   r.Name(),
		}
	}
	if rebuildApproved[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "nix: " + basename + " " + subcmd + " is safe (no activation)",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateNixEnv(args []string) hookio.RuleResult {
	for _, a := range args {
		if nixEnvRejectFlags[a] {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "nix: nix-env " + a + " modifies global profile",
				Module:   r.Name(),
			}
		}
		if a == "--query" || a == "-q" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: nix-env query is read-only",
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

var nixStoreReadOnly = map[string]bool{
	"--query": true, "-q": true,
	"--print-env": true,
	"--verify": true, "--verify-path": true,
	"--dump": true, "--export": true,
	"--read-log": true, "-l": true,
	"--dump-db": true,
}

func (r *Rule) evaluateNixStore(args []string) hookio.RuleResult {
	for _, a := range args {
		if nixStoreReadOnly[a] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "nix: nix-store " + a + " is read-only",
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) evaluateNixShell(args []string, input *hookio.HookInput) hookio.RuleResult {
	innerCmd := extractAfterFlag(args, "--run")
	if innerCmd == "" {
		innerCmd = extractAfterFlag(args, "--command")
	}
	if innerCmd != "" {
		outerExpr := normalizeExpr("nix-shell " + strings.Join(args, " "))
		stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "nix-shell", Expression: outerExpr}}
		return r.exprEval.EvaluateExpression(innerCmd, stack, input)
	}
	// nix-shell without --run: just entering a shell — approve
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "nix: nix-shell (no command) is approved",
		Module:   r.Name(),
	}
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

func extractAfterFlag(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return strings.Join(args[i+1:], " ")
		}
	}
	return ""
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
