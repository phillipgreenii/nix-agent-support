package kubectl

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// baseReadOnlyOperations are the generic, upstream kubectl read-only verbs.
// Consumer-specific plugin verbs (e.g. ZR's wslogs/zrlog/wsfirstpod) are NOT
// here — they arrive via KubectlConfig.ReadOnlyVerbs (ADR 0033).
var baseReadOnlyOperations = map[string]bool{
	"get": true, "describe": true, "logs": true, "top": true,
	"cluster-info": true, "config": true, "api-resources": true,
	"api-versions": true, "version": true, "explain": true, "auth": true,
	"events": true, "diff": true, "wait": true,
}

var rolloutReadOnlySubcommands = map[string]bool{"status": true, "history": true}

// baseExecOperations are the generic exec-family verbs. Consumer exec verbs
// (ZR's exe/shell/wsexec) arrive via KubectlConfig.ExecVerbs.
var baseExecOperations = map[string]bool{"exec": true}

// baseValueFlags consume the following token as their value; that token must not
// be mistaken for the kubectl operation. Consumer workspace flags (--ws/
// --workspace) are added from KubectlConfig.DevWorkspaceFlags at construction.
var baseValueFlags = map[string]bool{
	"-n": true, "--namespace": true, "-c": true, "--container": true,
	"-f": true, "--filename": true,
	"--context": true, "--kubeconfig": true, "-o": true, "--output": true,
	"-l": true, "--selector": true,
}

// baseDevScopeFlags name a workspace/namespace we can check for the personal-dev
// prefix. The generic k8s flags -n/--namespace are base; consumer workspace flags
// (--ws/--workspace) are added from KubectlConfig.DevWorkspaceFlags.
var baseDevScopeFlags = map[string]bool{
	"-n": true, "--namespace": true,
}

type Rule struct {
	exprEval hookio.Evaluator
	pe       *patheval.PathEvaluator

	// Resolved sets: base defaults merged with the injected KubectlConfig.
	execAliases        map[string]bool
	readOnlyOps        map[string]bool
	execOps            map[string]bool
	scopedApproveOps   map[string]bool
	positionalWSOps    map[string]bool
	devScopeFlags      map[string]bool
	devScopeGlued      []string // "--<flag>=" forms of the long devScopeFlags
	valueFlags         map[string]bool
	nonDevAccounts     map[string]bool
	clusterEnvVar      string
	devClusterPrefixes []string
	devWorkspacePrefix string
}

// New constructs the kubectl rule. cfg carries the consumer-specific extensions
// (aliases, plugin verbs, dev-workspace scope) injected by factory.go; a zero
// cfg yields the base generic kubectl behavior only.
func New(eval hookio.Evaluator, pe *patheval.PathEvaluator, cfg configrules.KubectlConfig) *Rule {
	r := &Rule{
		exprEval:           eval,
		pe:                 pe,
		execAliases:        toSet(cfg.ExecutableAliases),
		readOnlyOps:        mergeSet(baseReadOnlyOperations, cfg.ReadOnlyVerbs),
		execOps:            mergeSet(baseExecOperations, cfg.ExecVerbs),
		scopedApproveOps:   toSet(cfg.ScopedApproveVerbs),
		positionalWSOps:    toSet(cfg.PositionalWorkspaceVerbs),
		devScopeFlags:      mergeSet(baseDevScopeFlags, cfg.DevWorkspaceFlags),
		valueFlags:         mergeSet(baseValueFlags, cfg.DevWorkspaceFlags),
		nonDevAccounts:     toSet(cfg.NonDevAccounts),
		clusterEnvVar:      cfg.ClusterEnvVar,
		devClusterPrefixes: cfg.DevClusterPrefixes,
		devWorkspacePrefix: cfg.DevWorkspacePrefix,
	}
	for f := range r.devScopeFlags {
		if strings.HasPrefix(f, "--") {
			r.devScopeGlued = append(r.devScopeGlued, f+"=")
		}
	}
	return r
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func mergeSet(base map[string]bool, extra []string) map[string]bool {
	m := make(map[string]bool, len(base)+len(extra))
	for k := range base {
		m[k] = true
	}
	for _, e := range extra {
		m[e] = true
	}
	return m
}

func (r *Rule) Name() string {
	return "kubectl"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("kubectl: read bash command: %w", err)
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		if !r.isKubectlExecutable(pc.Executable) {
			continue
		}
		operation := r.extractOperation(pc.Args)
		if operation == "" {
			return hookio.NotApplicable()
		}
		if r.execOps[operation] {
			if r.isDevWorkspaceScope(operation, pc.Args, pc.EnvVars) {
				return r.evaluateExec(pc.Args, input)
			}
			// Not applicable (ADR 0043): the chain must continue. Former Reason,
			// kept because it is the only record of WHY: "non-dev kubectl exec (defer to mode/settings)"
			return hookio.NotApplicable()
		}
		if operation == "rollout" {
			if rolloutReadOnlySubcommands[r.rolloutSubcommand(pc.Args)] {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only kubectl command", Module: r.Name()}, nil
			}
			// Not applicable (ADR 0043): the chain must continue. Former Reason,
			// kept because it is the only record of WHY: "modifying kubectl command (defer)"
			return hookio.NotApplicable()
		}
		if r.scopedApproveOps[operation] {
			if r.isDevWorkspaceScope(operation, pc.Args, pc.EnvVars) {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "kc dev-workspace command", Module: r.Name()}, nil
			}
			// Not applicable (ADR 0043): the chain must continue. Former Reason,
			// kept because it is the only record of WHY: "non-dev kc command (defer)"
			return hookio.NotApplicable()
		}
		if r.readOnlyOps[operation] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only kubectl command",
				Module:   r.Name(),
			}, nil
		}
		// Everything else (apply, delete, scale, exec, etc.) -> defer to mode/settings
		// Not applicable (ADR 0043): the chain must continue. Former Reason,
		// kept because it is the only record of WHY: "modifying kubectl command (defer)"
		return hookio.NotApplicable()
	}
	return hookio.NotApplicable()
}

// evaluateExec recurses into the inner command after `--` through the full
// rule chain, using a pod-internal path evaluator (docker-exec pattern).
func (r *Rule) evaluateExec(args []string, input *hookio.HookInput) (hookio.RuleResult, error) {
	inner := innerAfterDoubleDash(args)
	if len(inner) == 0 {
		// Not applicable (ADR 0043): the chain must continue. Former Reason,
		// kept because it is the only record of WHY: "kc exec without inner command"
		return hookio.NotApplicable()
	}
	if r.exprEval == nil {
		return hookio.NotApplicable()
	}
	innerExpr := extractInnerCommand(inner)
	outerExpr := strings.Join(strings.Fields(strings.Join(args, " ")), " ")
	stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "kc exec", Expression: outerExpr}}
	scoped := *input
	if r.pe != nil {
		scoped.PathEval = r.pe.WithMounts([]patheval.Mount{}) // pod-internal paths
	}
	// ADR 0043 RECURSION BOUNDARY. NOT `..., nil`: an inner NoOpinion is the inner
	// chain's loop-exhaustion verdict, and returning it as this rule's own verdict
	// would STOP the outer chain where the pre-ADR forwarded Abstain continued it.
	// hookio.FromRecursion states the translation in one place.
	return hookio.FromRecursion(r.exprEval.EvaluateExpression(innerExpr, stack, &scoped))
}

// innerAfterDoubleDash returns the args after the first `--`, or nil if none.
func innerAfterDoubleDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// extractInnerCommand converts inner command args into an expression string.
// For "bash -c 'expr'" it extracts the expression; otherwise joins args.
func extractInnerCommand(cmdArgs []string) string {
	if len(cmdArgs) >= 3 && (cmdArgs[0] == "bash" || cmdArgs[0] == "sh") && cmdArgs[1] == "-c" {
		return strings.Join(cmdArgs[2:], " ")
	}
	return strings.Join(cmdArgs, " ")
}

// isPersonalDevName reports whether v names a personal dev workspace. With an
// empty configured prefix (base/no-config) NO name qualifies — critically, an
// empty prefix must NOT make strings.HasPrefix match everything.
func (r *Rule) isPersonalDevName(v string) bool {
	return r.devWorkspacePrefix != "" && strings.HasPrefix(v, r.devWorkspacePrefix)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// isDevWorkspaceScope reports whether a kc/kubectl invocation targets a personal
// dev workspace (see "Devxp scope" contract). For positional-workspace verbs the
// workspace is a bare positional arg; for every other op only the configured
// dev-workspace flags (base -n/--namespace plus consumer --ws/--workspace) count
// — so a positional dev token elsewhere (e.g. an exec pod name) is never
// mistaken for a scope signal. AWS_PROFILE is the generic env var; only the
// non-dev account names and the cluster env var/prefixes are consumer config.
func (r *Rule) isDevWorkspaceScope(operation string, args []string, env []cmdparse.EnvAssignment) bool {
	for _, e := range env {
		if e.Name == "AWS_PROFILE" {
			acct := e.Value
			if i := strings.IndexByte(acct, '/'); i >= 0 {
				acct = acct[:i]
			}
			if r.nonDevAccounts[acct] {
				return false
			}
		}
		if r.clusterEnvVar != "" && e.Name == r.clusterEnvVar {
			if e.Value != "" && !hasAnyPrefix(e.Value, r.devClusterPrefixes) {
				return false
			}
		}
	}
	seenOp := false
	// NOTE: not range-over-int — the i++ below intentionally skips a flag's value.
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if r.devScopeFlags[a] && i+1 < len(args) && r.isPersonalDevName(args[i+1]) {
			return true
		}
		for _, pfx := range r.devScopeGlued {
			if strings.HasPrefix(a, pfx) && r.isPersonalDevName(strings.TrimPrefix(a, pfx)) {
				return true
			}
		}
		// positional-workspace verbs: the workspace is a bare positional past the
		// subcommand token.
		if r.positionalWSOps[operation] {
			if r.valueFlags[a] { // e.g. `-f <path>` — consume the value, not the workspace
				i++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			if !seenOp { // the subcommand token itself (the scoped verb)
				seenOp = true
				continue
			}
			if r.isPersonalDevName(a) {
				return true
			}
		}
	}
	return false
}

func (r *Rule) isKubectlExecutable(exec string) bool {
	base := filepath.Base(exec)
	if base == "kubectl" || strings.HasSuffix(base, "kubectl") {
		return true
	}
	return r.execAliases[base]
}

// extractOperation returns the first bare (non-flag, non-flag-value) token
// before any `--`, i.e. the kubectl verb. Returns "" if none.
func (r *Rule) extractOperation(args []string) string {
	// NOTE: not range-over-int — the i++ below intentionally skips a flag's value.
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if r.valueFlags[a] {
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
func (r *Rule) rolloutSubcommand(args []string) string {
	seen := false
	// NOTE: not range-over-int — the i++ below intentionally skips a flag's value.
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if r.valueFlags[a] {
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
