package kubectl

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var readOnlyOperations = map[string]bool{
	"get": true, "describe": true, "logs": true, "top": true,
	"cluster-info": true, "config": true, "api-resources": true,
	"api-versions": true, "version": true, "explain": true, "auth": true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
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
		// Everything else (apply, delete, scale, exec, etc.) -> ASK
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "modifying kubectl command",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func isKubectlExecutable(exec string) bool {
	base := filepath.Base(exec)
	return base == "kubectl" || base == "kc" || strings.HasSuffix(base, "kubectl")
}

func extractOperation(args []string) string {
	for _, a := range args {
		if a == "--" {
			return ""
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}
