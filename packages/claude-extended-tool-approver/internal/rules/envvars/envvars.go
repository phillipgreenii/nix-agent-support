package envvars

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var dangerousVars = map[string]bool{
	"LD_PRELOAD":            true,
	"DYLD_INSERT_LIBRARIES": true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_LIBRARY_PATH":     true,
	"PATH":                  true,
	"HOME":                  true,
	"BASH_ENV":              true,
	"ENV":                   true,
	"ZDOTDIR":               true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "env-vars"
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
		for _, ev := range pc.EnvVars {
			if dangerousVars[ev.Name] || strings.HasPrefix(ev.Name, "BASH_FUNC_") {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "dangerous env var: " + ev.Name + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			if ev.Expansion == cmdparse.ExpansionUnknown {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "env var contains unclassifiable expression: " + ev.Name + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
