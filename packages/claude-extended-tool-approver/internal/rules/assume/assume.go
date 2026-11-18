package assume

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Rule rejects every `assume` invocation. The production-access gate is that
// an agent reaches production systems only from an environment the operator
// assumed (operator ruling, Phillip, 2026-11-17: "as long as assume isn't
// allowed to be called by the agent, then it would only be able to access
// prod systems if it was run in an assumed environment which is only possible
// if i have explicitly done so"). That includes `assume ... --exec <cmd>`,
// direct or through `source assume` / `. assume`: the flag still performs the
// assume, so it is rejected exactly like a bare assume (operator ruling,
// 2026-11-17: "yes reject assume --exec like assume."). The earlier design
// that delegated the --exec payload to the rule chain is superseded.
type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "assume" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("assume: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if !isAssume(pc) {
			continue
		}
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "assume: AWS assume-role commands must be run outside of Claude sessions. Exit the session, run assume externally, then resume.",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

// isAssume reports whether pc is an `assume` invocation: either Executable's
// basename is "assume", or the `source`/`.` shell builtins name "assume" as
// the script to run (cmdparse has no special model for shell builtins, so
// Executable is literally "source" or "." and Args[0] is the script name).
//
// A `bash -c '...'`-wrapped invocation is deliberately out of scope: it
// produces a leaf whose Executable is "bash".
func isAssume(pc cmdparse.ParsedCommand) bool {
	if filepath.Base(pc.Executable) == "assume" {
		return true
	}
	return (pc.Executable == "source" || pc.Executable == ".") &&
		len(pc.Args) > 0 && filepath.Base(pc.Args[0]) == "assume"
}
