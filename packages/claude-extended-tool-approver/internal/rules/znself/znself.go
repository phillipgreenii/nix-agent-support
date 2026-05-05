package znself

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var blockedCommands = map[string]bool{
	"zn-self-apply":   true,
	"zn-self-upgrade": true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "znself"
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
		if !strings.HasPrefix(basename, "zn-self-") {
			continue
		}
		if blockedCommands[basename] {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "znself: " + basename + " requires human (system modification)",
				Module:   r.Name(),
			}
		}
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "znself: " + basename + " is approved",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
