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
//
// pg2-ursuo audit: this table must cover the FULL kubectl global-flag surface,
// not just the handful discovered ad hoc. Source of truth is the literal
// output of `kubectl options` (client v1.25.16, kubectl v1.25.16, run
// 2026-08-21) — the flags kubectl itself documents as common to every
// subcommand. Rows below the first (pre-existing, non-global per-command
// flags) are grouped to mirror that output's own groupings.
//
// Every value-taking global flag from that output is listed here. The
// following global flags are DELIBERATELY EXCLUDED because `kubectl options`
// documents them as booleans (bare `--flag`/`--flag=true|false`, no separate
// value token): --add-dir-header, --alsologtostderr,
// --insecure-skip-tls-verify, --logtostderr, --match-server-version,
// --one-output, --skip-headers, --skip-log-headers, --warnings-as-errors.
// Adding a boolean flag here would be the OPPOSITE bug from the one this
// audit closes: for the bare (`--flag`, no `=value`) spelling this table
// would then skip the NEXT real token as if it were that flag's value —
// approval-widening, not fail-safe.
var baseValueFlags = map[string]bool{
	// Non-global per-command flags (pre-existing; several subcommands, not
	// literally part of `kubectl options`, but still tokens whose next arg is
	// a value rather than a verb).
	"-n": true, "--namespace": true, "-c": true, "--container": true,
	"-f": true, "--filename": true, "-o": true, "--output": true,
	"-l": true, "--selector": true,

	// Global: kubeconfig / connection / server selection.
	"--context": true, "--cluster": true, "--kubeconfig": true,
	"-s": true, "--server": true,

	// Global: authentication and impersonation.
	"--token": true, "--user": true, "--username": true, "--password": true,
	"--as": true, "--as-group": true, "--as-uid": true,

	// Global: TLS/certificates.
	"--certificate-authority": true, "--client-certificate": true,
	"--client-key": true, "--tls-server-name": true,

	// Global: request/runtime behavior.
	"--request-timeout": true, "--cache-dir": true,

	// Global: klog logging flags that take a value (verbosity level, paths,
	// patterns, sizes, durations, thresholds).
	"-v": true, "--v": true, "--vmodule": true,
	"--log-backtrace-at": true, "--log-dir": true, "--log-file": true,
	"--log-file-max-size": true, "--log-flush-frequency": true,
	"--stderrthreshold": true,

	// Global: profiling.
	"--profile": true, "--profile-output": true,
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

	// execReadOnlyClusters / execMutableClusters classify a kubectl exec TARGET
	// (the --context/--cluster value) as safe-to-recurse vs mutable/production;
	// see execTargetClass and classifyExecTarget below and
	// configrules.KubectlConfig's doc comment for the design.
	execReadOnlyClusters map[string]bool
	execMutableClusters  map[string]bool
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

		execReadOnlyClusters: toSet(cfg.ExecReadOnlyClusters),
		execMutableClusters:  toSet(cfg.ExecMutableClusters),
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

// refuse is this rule's ADR 0044 refusal-and-continue return. Every site that uses it
// has the same shape: the executable IS a kubectl (or a configured alias), the verb HAS
// been classified, this rule will not clear the invocation — and yet it must not stop
// the chain, because build-tools and sqlite3 still run after it.
//
// ADR 0043 had no outcome for that, so these sites became ErrNotApplicable and their
// reasons were demoted to comments opening "Former Reason, kept because it is the only
// record of WHY". This restores each one as a real Reason, and the restoration is not
// cosmetic: reported as a not-applicable, `kubectl delete pod x` is indistinguishable
// from a basename no rule has ever heard of — an EXHAUSTION — and exhaustion is the half
// a consumer may act on to clear a body. Under-conversion is therefore the
// APPROVAL-WIDENING direction, which is why each of these five sites is a refusal and
// not a not-applicable.
//
// It can only make a leaf MORE restrictive: the engine folds it as a floor and keeps
// going, so a later rule's Ask or Reject still wins and nothing is shadowed.
func (r *Rule) refuse(reason string) (hookio.RuleResult, error) {
	return hookio.Refused(r.Name(), reason)
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("kubectl: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if !r.isKubectlExecutable(pc.Executable) {
			continue
		}
		operation := r.extractOperation(pc.Args)
		if operation == "" {
			// DELIBERATELY NOT a refusal (ADR 0044). No verb was found, so nothing was
			// classified and this rule has examined nothing it could withhold — `kubectl`
			// with no operand, or an argv whose bare tokens are all consumed as flag
			// values. Reporting it as a refusal would claim an examination that did not
			// happen, and the fail-safe direction only runs one way: a refusal must be
			// backed by a judgement.
			return hookio.NotApplicable()
		}
		if r.execOps[operation] {
			if r.isDevWorkspaceScope(operation, pc.Args, pc.EnvVars) {
				return r.evaluateExec(pc.Args, input)
			}
			switch r.classifyExecTarget(r.execTarget(pc.Args)) {
			case execTargetReadOnly:
				return r.evaluateExec(pc.Args, input)
			case execTargetMutable:
				// A real, terminal Ask — NOT the refuse-and-continue floor. A
				// mutable/production-classified target must not resolve to the
				// blanket "defer to mode/settings" abstain the unclassified and
				// pre-classification cases get; it needs its own opinion.
				return hookio.RuleResult{
					Decision: hookio.Ask,
					Reason:   "kubectl exec against mutable/production-classified target",
					Module:   r.Name(),
				}, nil
			default: // execTargetUnclassified
				return r.refuse("kubectl: non-dev kubectl exec against unclassified target (defer to mode/settings)")
			}
		}
		if operation == "rollout" {
			if rolloutReadOnlySubcommands[r.rolloutSubcommand(pc.Args)] {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only kubectl command", Module: r.Name()}, nil
			}
			return r.refuse("kubectl: modifying kubectl command (defer)")
		}
		if r.scopedApproveOps[operation] {
			if r.isDevWorkspaceScope(operation, pc.Args, pc.EnvVars) {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "kc dev-workspace command", Module: r.Name()}, nil
			}
			return r.refuse("kubectl: non-dev kc command (defer)")
		}
		if r.readOnlyOps[operation] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only kubectl command",
				Module:   r.Name(),
			}, nil
		}
		// Everything else (apply, delete, scale, exec, etc.) -> defer to mode/settings
		return r.refuse("kubectl: modifying kubectl command (defer)")
	}
	return hookio.NotApplicable()
}

// evaluateExec recurses into the inner command after `--` through the full
// rule chain, using a pod-internal path evaluator (docker-exec pattern).
func (r *Rule) evaluateExec(args []string, input *hookio.HookInput) (hookio.RuleResult, error) {
	inner := innerAfterDoubleDash(args)
	if len(inner) == 0 {
		return r.refuse("kubectl: kc exec without inner command")
	}
	if r.exprEval == nil {
		// DELIBERATELY NOT a refusal (ADR 0044). A nil evaluator is a CONSTRUCTION
		// state, not a judgement about this command: the rule was built without the
		// recursion it needs, so it never looked at the inner expression at all. That is
		// the "could not determine" shape, and ADR 0043's error policy keeps it out of
		// the refusal channel — claiming a refusal here would attribute a judgement to a
		// rule that is structurally unable to make one.
		return hookio.NotApplicable()
	}
	source, leaves, ok := structuralInnerCommand(inner)
	if !ok {
		return r.refuse("kubectl: kc exec inner command could not be parsed as structure (deferred to claude-code)")
	}
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
	return hookio.FromRecursion(r.exprEval.EvaluateStructure(source, leaves, stack, &scoped))
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

// structuralInnerCommand derives the kc-exec inner command as PARSED
// STRUCTURE — never rule-constructed text handed back to the engine for
// re-evaluation (I13; pg2-9aqol closes this rule's instance of the docker/
// safecmds/nix/kubectl migration). cmdArgs are the already-unquoted argv
// tokens after `--`. Two shapes:
//
//   - "bash|sh -c SCRIPT": SCRIPT is cmdArgs[2] ALONE — already-unquoted text
//     that IS genuine shell source, the exact string a pod's bash/sh would
//     itself re-parse and run — so it is parsed AS-IS, with no join at all.
//     Any further cmdArgs are bash -c's own positional parameters ($0, $1,
//     ...), never part of the script, and MUST NOT be appended to it: the
//     former `strings.Join(cmdArgs[2:], " ")` folded them into the script
//     text, which was itself an instance of the quoting-loss defect this
//     migration removes — a positional parameter could smuggle in extra
//     script text the pod's bash never actually runs as script.
//   - anything else: a literal argv kubectl exec hands directly to the pod's
//     execve, with NO shell in between — none of these bytes is ever
//     shell-interpreted there. It is encoded here as single-quoted words,
//     the one quoting form that suppresses every kind of expansion, so each
//     token survives as one literal, non-expanding word — matching that
//     "no shell in the pod" reality exactly: a token spelled "$(rm -rf /)"
//     or containing ";" is DATA, never a live substitution or operator, and
//     single-quoting (rather than the former bare-space join) is what keeps
//     the structural parse from treating it as one.
//
// Both shapes end in one real cmdparse.ParseShell call over the returned
// source, so leaves' Raw fields are genuine parsed-source substrings the
// engine can independently re-derive from (evaluateParsed's
// `cmdparse.Parse(pc.Raw)`) — never a hand-built ParsedCommand whose fields
// could drift from its own Raw. ok is false only when the derived source
// itself fails to parse (a malformed inline script), in which case the
// caller MUST fail closed rather than call EvaluateStructure with an empty
// leaf set.
func structuralInnerCommand(cmdArgs []string) (source string, leaves []cmdparse.ParsedCommand, ok bool) {
	if len(cmdArgs) >= 3 && (cmdArgs[0] == "bash" || cmdArgs[0] == "sh") && cmdArgs[1] == "-c" {
		source = cmdArgs[2]
	} else {
		source = quoteArgsAsLiteralWords(cmdArgs)
	}
	sp := cmdparse.ParseShell(source)
	if sp.Unparseable {
		return "", nil, false
	}
	return source, sp.Leaves, true
}

// quoteArgsAsLiteralWords single-quote-encodes each arg so cmdparse.ParseShell
// lowers it back to EXACTLY these tokens, each its own literal (non-expanding)
// word, whatever bytes it contains — the correct structural stand-in for an
// argv passed straight to execve with no intervening shell. Single quotes
// need only their own embedded occurrences escaped (close, escaped quote,
// reopen); every other byte, including "$", "`", ";", "&&" and whitespace,
// passes through completely inert.
func quoteArgsAsLiteralWords(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
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

// execTargetFlags name the flags whose value identifies the kubectl exec
// TARGET cluster/context. Both are base generic kubeconfig flags already in
// baseValueFlags; checked in order, the first one present wins.
var execTargetFlags = []string{"--context", "--cluster"}

// execTarget resolves the cluster/context name a kubectl invocation names via
// --context/--cluster, or "" if neither flag is present (an ambient
// current-context kubectl would resolve from its kubeconfig, which this rule
// cannot see). An empty return MUST be classified execTargetUnclassified by
// classifyExecTarget — never treated as read-only.
func (r *Rule) execTarget(args []string) string {
	// NOTE: not range-over-int — the i++ below intentionally skips a flag's value.
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		for _, f := range execTargetFlags {
			if a == f && i+1 < len(args) {
				return args[i+1]
			}
			if v, ok := strings.CutPrefix(a, f+"="); ok {
				return v
			}
		}
	}
	return ""
}

// execTargetClass is classifyExecTarget's three-way verdict on a kubectl exec
// TARGET (see configrules.KubectlConfig's ExecReadOnlyClusters/
// ExecMutableClusters doc comment for the full design).
type execTargetClass int

const (
	// execTargetUnclassified means the target is either unnamed (no
	// --context/--cluster) or named but present in neither configured list.
	// The caller MUST treat this conservatively — it is NOT a green light.
	execTargetUnclassified execTargetClass = iota
	execTargetReadOnly
	execTargetMutable
)

// classifyExecTarget classifies a resolved exec target against the injected
// KubectlConfig cluster lists. A target in BOTH lists (a config error) is
// treated as mutable — checked first, the fail-safe direction. With no config
// (both sets empty, the base/default case) every target is unclassified,
// exactly matching pre-existing behavior.
func (r *Rule) classifyExecTarget(target string) execTargetClass {
	if target == "" {
		return execTargetUnclassified
	}
	if r.execMutableClusters[target] {
		return execTargetMutable
	}
	if r.execReadOnlyClusters[target] {
		return execTargetReadOnly
	}
	return execTargetUnclassified
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
