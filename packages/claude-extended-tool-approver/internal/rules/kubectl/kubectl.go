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
	"events": true, "diff": true, "wait": true,
	"wslogs": true, "zrlog": true, "wsfirstpod": true,
}

var rolloutReadOnlySubcommands = map[string]bool{"status": true, "history": true}

// scopedApproveOperations are kc plugin verbs that mutate a dev workspace only;
// auto-approved iff the command targets a personal dev workspace.
var scopedApproveOperations = map[string]bool{
	"sync": true, "syncdev": true, "workspace": true,
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
		if operation == "rollout" {
			if rolloutReadOnlySubcommands[rolloutSubcommand(pc.Args)] {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only kubectl command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "modifying kubectl command (defer)", Module: r.Name()}
		}
		if scopedApproveOperations[operation] {
			if isDevWorkspaceScope(pc.Args, pc.EnvVars) {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "kc dev-workspace command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "non-dev kc command (defer)", Module: r.Name()}
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

// nonDevAWSAccounts are AWS_PROFILE accounts (the part before '/') that name a
// prod/shared cluster; their presence forces a non-dev classification.
var nonDevAWSAccounts = map[string]bool{
	"prod": true, "dprod": true, "euprod": true,
	"build": true, "fastlane": true, "pdx": true, "test": true,
}

// devScopeFlags name a workspace/namespace we can check for the personal-dev prefix.
var devScopeFlags = map[string]bool{
	"--ws": true, "--workspace": true, "-n": true, "--namespace": true,
}

func isPersonalDevName(v string) bool { return strings.HasPrefix(v, "d-") }

// isDevWorkspaceScope reports whether a kc/kubectl invocation targets a personal
// dev workspace (see "Devxp scope" contract).
func isDevWorkspaceScope(args []string, env []cmdparse.EnvAssignment) bool {
	for _, e := range env {
		switch e.Name {
		case "AWS_PROFILE":
			acct := e.Value
			if i := strings.IndexByte(acct, '/'); i >= 0 {
				acct = acct[:i]
			}
			if nonDevAWSAccounts[acct] {
				return false
			}
		case "KC_CLUSTER":
			if e.Value != "" && !strings.HasPrefix(e.Value, "d1-") && !strings.HasPrefix(e.Value, "dd1-") {
				return false
			}
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if devScopeFlags[a] && i+1 < len(args) && isPersonalDevName(args[i+1]) {
			return true
		}
		for _, pfx := range []string{"--ws=", "--workspace=", "--namespace="} {
			if strings.HasPrefix(a, pfx) && isPersonalDevName(strings.TrimPrefix(a, pfx)) {
				return true
			}
		}
	}
	return false
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

// rolloutSubcommand returns the sub-verb after `rollout` (the second bare token).
func rolloutSubcommand(args []string) string {
	seen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if valueFlags[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if !seen {
			seen = true
			continue
		}
		return a
	}
	return ""
}
