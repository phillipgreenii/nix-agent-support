package assume

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "assume" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("assume: read bash command: %w", err)
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) == "assume" {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "assume: AWS assume-role commands must be run outside of Claude sessions. Exit the session, run assume externally, then resume.",
				Module:   r.Name(),
			}, nil
		}
	}
	return hookio.NotApplicable()
}
