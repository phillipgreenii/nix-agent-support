// Package monorepo is a config-driven MECHANISM for approving consumer
// monorepo-local commands/scripts. It follows the kubectl/buildtools template:
// the classification logic lives here in ceta-core, and the consumer-specific
// command DATA (approved basenames + per-wrapper dangerous env vars) arrives via
// an injected configrules.MonorepoConfig — the rules.json `monorepo` block,
// wired in by internal/setup/factory.go.
//
// The executable is first normalized relative to the project root/CWD, then its
// basename is matched against ApprovedCommands. A matched command carrying an
// inline assignment of one of its DangerousEnvByWrapper vars is NOT approved
// (Abstain, deferred to Claude).
//
// SAFE DEFAULT: an empty config makes the rule Abstain on every command — a
// consumer that ships no `monorepo` block has this rule defer entirely.
package monorepo

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

type Rule struct {
	eval                  *patheval.PathEvaluator
	approvedCommands      map[string]bool
	dangerousEnvByWrapper map[string]map[string]bool
}

// New constructs the monorepo rule from cfg (the rules.json `monorepo` block). A
// zero cfg makes the rule Abstain on every command (the safe base default).
func New(eval *patheval.PathEvaluator, cfg configrules.MonorepoConfig) *Rule {
	r := &Rule{
		eval:                  eval,
		approvedCommands:      make(map[string]bool, len(cfg.ApprovedCommands)),
		dangerousEnvByWrapper: make(map[string]map[string]bool, len(cfg.DangerousEnvByWrapper)),
	}
	for _, cmd := range cfg.ApprovedCommands {
		r.approvedCommands[cmd] = true
	}
	for wrapper, vars := range cfg.DangerousEnvByWrapper {
		set := make(map[string]bool, len(vars))
		for _, v := range vars {
			set[v] = true
		}
		r.dangerousEnvByWrapper[wrapper] = set
	}
	return r
}

func (r *Rule) Name() string {
	return "monorepo"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("monorepo: read bash command: %w", err)
	}
	projectRoot := r.eval.ProjectRoot()
	cwd := input.CWD
	if cwd == "" {
		cwd = projectRoot
	}
	for _, pc := range parsed {
		norm := cmdparse.NormalizeExecutable(pc.Executable, projectRoot, cwd)
		basename := filepath.Base(norm)
		if r.approvedCommands[basename] {
			if dangerousEnvs, ok := r.dangerousEnvByWrapper[basename]; ok {
				for _, ev := range pc.EnvVars {
					if dangerousEnvs[ev.Name] {
						// ADR 0044 REFUSAL, not a not-applicable. The basename IS on this
						// consumer's approved list — the rule was one step from approving it —
						// and the dangerous env assignment is the reason it does not. Reported
						// as a not-applicable that judgement is indistinguishable from a
						// wrapper no rule has ever heard of (an EXHAUSTION), and exhaustion is
						// the half a consumer may clear, so under-conversion is the
						// APPROVAL-WIDENING direction. As a floor the chain still continues, so
						// a later rule's Ask/Reject wins and only an Approve of this same
						// wrapper (e.g. build-tools reaching the same basename) is demoted —
						// which is the intended effect, not a side effect.
						return hookio.Refused(r.Name(), "monorepo: "+basename+" with dangerous env var: "+ev.Name+" (deferred to claude-code)")
					}
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "monorepo approved command",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}
