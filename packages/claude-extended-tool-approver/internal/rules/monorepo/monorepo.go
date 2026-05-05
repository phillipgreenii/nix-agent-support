package monorepo

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var approvedCommands = map[string]bool{
	"grazr": true, "gozr": true, "pyzr": true, "shzr": true,
	"stevedore": true, "epoxy": true, "validate_format": true,
	"check-airflow-dags.sh": true, "agent-code-review-support": true, "pre-merge-py-check": true,
	"zr-proto-regenerate.sh": true, "generate-build-deps": true,
}

var dangerousEnvByWrapper = map[string]map[string]bool{
	"pyzr": {"PYTHONSTARTUP": true, "PYTHONHOME": true},
	"gozr": {"GOROOT": true, "GOPROXY": true, "GONOSUMCHECK": true, "GONOSUMDB": true},
	"grazr": {
		"GRADLE_USER_HOME":  true,
		"GRADLE_OPTS":       true,
		"JAVA_HOME":         true,
		"JAVA_OPTS":         true,
		"JAVA_TOOL_OPTIONS": true,
		"_JAVA_OPTIONS":     true,
	},
}

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
