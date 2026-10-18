// Package pgnixlogwrapped classifies phillipg-nix-repo-base's
// pg-nix-log-wrapped, the transparent telemetry wrapper that runs a nix
// command unchanged and turns its activity stream into OpenTelemetry spans
// (bead tc-ny0h1).
//
// # Why a dedicated module
//
// The wrapper is a pure pass-through: its verdict is exactly the verdict of
// the command it wraps. pn-workspace-rules/skills/pg-nix-log-wrapped (in
// phillipg-nix-repo-base) prescribes `pg-nix-log-wrapped -- nix flake check`,
// `... -- nix build .#pkg` and `... -- darwin-rebuild build --flake .`, and
// plugin-conformance-repo-base failed because no rule had an opinion on the
// wrapper's own executable. Approving the wrapper blindly would launder
// `pg-nix-log-wrapped -- darwin-rebuild switch`, so the rule DELEGATES the
// wrapped command to the full rule chain (the same structural-recursion
// pattern nix develop -c / assume --exec use) and forwards that verdict.
//
// # Grammar (README of the tool)
//
//	pg-nix-log-wrapped [--traceparent TP] [--otlp-endpoint URL] [--log-dir DIR] [--] CMD ARGS...
//	pg-nix-log-wrapped --check [--otlp-endpoint URL] [--log-dir DIR]
//
// Wrapper flags stop at the first `--` or the first non-flag argument.
// `--check` only reports the resolved endpoint/reachability/log-dir
// writability and runs no command: Approve. An unrecognized wrapper flag or
// a missing wrapped command is left unmatched (NotApplicable), so
// under-matching only costs a prompt, never a wrong Approve.
package pgnixlogwrapped

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// valueFlags are the wrapper flags that consume the following argument.
var valueFlags = map[string]bool{
	"--traceparent": true, "--otlp-endpoint": true, "--log-dir": true,
}

type Rule struct {
	exprEval hookio.Evaluator
}

// New constructs the rule without a recursive evaluator; it then can only
// approve `--check`.
func New() *Rule { return &Rule{} }

// NewWithEvaluator constructs the rule with the engine as its Evaluator so
// the wrapped command is classified by the full chain.
func NewWithEvaluator(eval hookio.Evaluator) *Rule { return &Rule{exprEval: eval} }

func (r *Rule) Name() string { return "pg-nix-log-wrapped" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-nix-log-wrapped: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pg-nix-log-wrapped" {
			continue
		}
		return r.evaluate(pc.Args, input)
	}
	return hookio.NotApplicable()
}

func (r *Rule) evaluate(args []string, input *hookio.HookInput) (hookio.RuleResult, error) {
	check := false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if a == "--check" {
			check = true
			continue
		}
		if valueFlags[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "--traceparent=") || strings.HasPrefix(a, "--otlp-endpoint=") ||
			strings.HasPrefix(a, "--log-dir=") {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return hookio.NotApplicable() // unknown wrapper flag
		}
		break // first non-flag argument starts CMD
	}
	if check {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "pg-nix-log-wrapped: --check only reports the resolved telemetry configuration",
			Module:   r.Name(),
		}, nil
	}
	if i >= len(args) || r.exprEval == nil {
		return hookio.NotApplicable()
	}
	rest := args[i:]
	source := quoteJoin(rest)
	leaves := cmdparse.Parse(source)
	if len(leaves) == 0 {
		return hookio.NotApplicable()
	}
	stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "pg-nix-log-wrapped", Expression: "pg-nix-log-wrapped " + quoteJoin(args)}}
	return hookio.FromRecursion(r.exprEval.EvaluateStructure(source, leaves, stack, input, input.InCommandVars, input.InCommandTempDirVars))
}

// quoteJoin single-quotes every element so no argument's content can be
// reinterpreted as a new word or operator on reparse (same technique as
// internal/rules/nix's quoteJoin).
func quoteJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}
