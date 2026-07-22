package kubectl

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var readOnlyOperations = map[string]bool{
	"get": true, "describe": true, "logs": true, "top": true,
	"cluster-info": true, "config": true, "api-resources": true,
	"api-versions": true, "version": true, "explain": true, "auth": true,
}

type Rule struct {
	exprEval hookio.Evaluator
	pe       *patheval.PathEvaluator
}

func New(eval hookio.Evaluator, pe *patheval.PathEvaluator) *Rule {
	return &Rule{exprEval: eval, pe: pe}
}

func (r *Rule) Name() string {
	return "kubectl"
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
		if !isKubectlExecutable(pc.Executable) {
			continue
		}
		operation := extractOperation(pc.Args)
		if operation == "" {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		if readOnlyOperations[operation] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only kubectl command",
				Module:   r.Name(),
			}
		}
		// Everything else (apply, delete, scale, exec, etc.) -> defer to mode/settings
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "modifying kubectl command (defer)",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func isKubectlExecutable(exec string) bool {
	base := filepath.Base(exec)
	return base == "kubectl" || base == "kc" || strings.HasSuffix(base, "kubectl")
}

// valueFlags consume the following token as their value; that token must not be
// mistaken for the kubectl operation.
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true, "-c": true, "--container": true,
	"-f": true, "--filename": true, "--ws": true, "--workspace": true,
	"--context": true, "--kubeconfig": true, "-o": true, "--output": true,
	"-l": true, "--selector": true,
}

// extractOperation returns the first bare (non-flag, non-flag-value) token
// before any `--`, i.e. the kubectl verb. Returns "" if none.
func extractOperation(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if valueFlags[a] {
			i++ // skip the flag's value
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue // bare flag or --flag=value
		}
		return a
	}
	return ""
}
