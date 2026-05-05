package buildtools

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var approvedTools = map[string]bool{
	"go": true,
	"gradle": true, "gradlew": true, "pre-commit": true, "bats": true, "bd": true,
	"tilt": true,
}

// approvedScripts lists project-relative script basenames that are safe to run.
var approvedScripts = map[string]bool{
	"zr-proto-regenerate.sh":       true,
	"pre-merge-protobuf-check":     true,
	"fix-ai-tools-ownership":       true,
	"pre-merge-py-check":           true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
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
		if approvedTools[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool",
				Module:   r.Name(),
			}
		}
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
		if basename == "generate-build-deps" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool",
				Module:   r.Name(),
			}
		}
		if approvedScripts[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved project script: " + basename,
				Module:   r.Name(),
			}
		}
		// bash/sh <script> — check if the script arg is an approved script
		if (basename == "bash" || basename == "sh") && len(pc.Args) > 0 {
			scriptBase := filepath.Base(pc.Args[0])
			if approvedScripts[scriptBase] || approvedTools[scriptBase] {
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
