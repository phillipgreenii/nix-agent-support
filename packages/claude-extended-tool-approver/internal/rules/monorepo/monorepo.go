package monorepo

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var approvedCommands = map[string]bool{}

var dangerousEnvByWrapper = map[string]map[string]bool{}

type Rule struct {
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

func (r *Rule) Name() string {
	return "monorepo"
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
	projectRoot := r.eval.ProjectRoot()
	cwd := input.CWD
	if cwd == "" {
		cwd = projectRoot
	}
	for _, pc := range parsed {
		norm := cmdparse.NormalizeExecutable(pc.Executable, projectRoot, cwd)
		basename := filepath.Base(norm)
		if approvedCommands[basename] {
			if dangerousEnvs, ok := dangerousEnvByWrapper[basename]; ok {
				for _, ev := range pc.EnvVars {
					if dangerousEnvs[ev.Name] {
						return hookio.RuleResult{
							Decision: hookio.Abstain,
							Reason:   "monorepo: " + basename + " with dangerous env var: " + ev.Name + " (deferred to claude-code)",
							Module:   r.Name(),
						}
					}
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "monorepo approved command",
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
