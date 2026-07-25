package buildtools

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// baseApprovedTools are the generic, non-consumer-specific build tools approved
// unconditionally. Consumer tools (e.g. ZR's Perl runners prove/yath, project
// scripts) are NOT here — they arrive via BuildtoolsConfig (ADR 0033).
var baseApprovedTools = map[string]bool{
	"go":     true,
	"gradle": true, "gradlew": true, "pre-commit": true, "prek": true, "bats": true, "bd": true,
	"tilt": true,
}

type Rule struct {
	approvedTools   map[string]bool
	approvedScripts map[string]bool
	verbScoped      map[string]map[string]bool // tool -> approved first-subcommand set
}

// New constructs the build-tools rule. cfg carries the consumer-specific tool /
// script approvals injected by factory.go; a zero cfg yields the base generic
// tool set only (go/gradle/bats/… plus devbox search / cue vet / jar xf).
func New(cfg configrules.BuildtoolsConfig) *Rule {
	r := &Rule{
		approvedTools:   mergeSet(baseApprovedTools, cfg.ApprovedTools),
		approvedScripts: toSet(cfg.ApprovedScripts),
		verbScoped:      map[string]map[string]bool{},
	}
	for _, vs := range cfg.VerbScopedApprovals {
		if r.verbScoped[vs.Tool] == nil {
			r.verbScoped[vs.Tool] = map[string]bool{}
		}
		r.verbScoped[vs.Tool][vs.Verb] = true
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
	return "build-tools"
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
		if r.approvedTools[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool",
				Module:   r.Name(),
			}
		}
		// Base-generic verb-scoped approvals (stay in the base, not config).
		if basename == "devbox" && hasSubcommand(pc.Args, "search") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "devbox search is approved",
				Module:   r.Name(),
			}
		}
		if basename == "cue" && hasSubcommand(pc.Args, "vet") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "cue vet is approved (read-only validation)",
				Module:   r.Name(),
			}
		}
		if basename == "jar" && hasSubcommand(pc.Args, "xf") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool: jar xf (extraction)",
				Module:   r.Name(),
			}
		}
		// Consumer-configured verb-scoped approvals (additive over the base).
		if verbs := r.verbScoped[basename]; verbs != nil {
			if sub := firstSubcommand(pc.Args); sub != "" && verbs[sub] {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "approved verb-scoped tool: " + basename + " " + sub,
					Module:   r.Name(),
				}
			}
		}
		if r.approvedScripts[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved project script: " + basename,
				Module:   r.Name(),
			}
		}
		// bash/sh <script> — check if the script arg is an approved script/tool.
		if (basename == "bash" || basename == "sh") && len(pc.Args) > 0 {
			scriptBase := filepath.Base(pc.Args[0])
			if r.approvedScripts[scriptBase] || r.approvedTools[scriptBase] {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "approved project script via " + basename + ": " + scriptBase,
					Module:   r.Name(),
				}
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func hasSubcommand(args []string, sub string) bool {
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a == sub
	}
	return false
}

// firstSubcommand returns the first non-flag argument, or "".
func firstSubcommand(args []string) string {
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}
