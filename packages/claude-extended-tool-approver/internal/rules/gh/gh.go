package gh

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var readOnlyPR = map[string]bool{
	"view": true, "list": true, "status": true, "diff": true, "checks": true,
}

var readOnlyIssue = map[string]bool{
	"view": true, "list": true, "status": true,
}

var readOnlyRepo = map[string]bool{
	"view": true, "list": true,
}

var readOnlyRun = map[string]bool{
	"view": true, "list": true, "watch": true,
}

var readOnlyRelease = map[string]bool{
	"view": true, "list": true,
}

var modifyingPR = map[string]bool{
	"create": true,
}

var modifyingIssue = map[string]bool{
	"create": true,
}

// BranchResolver looks up branch context for runtime decisions.
type BranchResolver interface {
	CurrentBranch(cwd string) (string, error)
	RunBranch(runID string) (string, error)
}

type Rule struct {
	resolver BranchResolver
}

func New(resolver BranchResolver) *Rule {
	return &Rule{resolver: resolver}
}

func (r *Rule) Name() string {
	return "gh"
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
		if !isGhExecutable(pc.Executable) {
			continue
		}
		resource, subcmd := "", ""
		if len(pc.Args) >= 1 {
			resource = pc.Args[0]
		}
		if len(pc.Args) >= 2 {
			subcmd = pc.Args[1]
		}
		if resource == "status" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh status",
				Module:   r.Name(),
			}
		}
		if resource == "auth" && subcmd == "status" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh auth status",
				Module:   r.Name(),
			}
		}
		if resource == "api" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh api",
				Module:   r.Name(),
			}
		}
		if resource == "search" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh search",
				Module:   r.Name(),
			}
		}
		if resource == "pr" && subcmd == "merge" {
			if hasFlag(pc.Args, "--auto") {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh pr merge --auto (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "gh pr merge (immediate)",
				Module:   r.Name(),
			}
		}
		if modifyingPR[subcmd] && resource == "pr" {
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "modifying gh pr command",
				Module:   r.Name(),
			}
		}
		if modifyingIssue[subcmd] && resource == "issue" {
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "modifying gh issue command",
				Module:   r.Name(),
			}
		}
		if readOnlyPR[subcmd] && resource == "pr" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh pr",
				Module:   r.Name(),
			}
		}
		if readOnlyIssue[subcmd] && resource == "issue" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh issue",
				Module:   r.Name(),
			}
		}
		if readOnlyRepo[subcmd] && resource == "repo" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh repo",
				Module:   r.Name(),
			}
		}
		if resource == "run" && subcmd == "rerun" {
			runID := extractRunID(pc.Args)
			if runID == "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: no run ID found",
					Module:   r.Name(),
				}
			}
			if r.resolver == nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: no resolver configured",
					Module:   r.Name(),
				}
			}
			currentBranch, err := r.resolver.CurrentBranch(input.CWD)
			if err != nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine current branch",
					Module:   r.Name(),
				}
			}
			runBranch, err := r.resolver.RunBranch(runID)
			if err != nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine run branch",
					Module:   r.Name(),
				}
			}
			if currentBranch == runBranch {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "gh run rerun for current branch",
					Module:   r.Name(),
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "gh run rerun for different branch",
				Module:   r.Name(),
			}
		}
		if readOnlyRun[subcmd] && resource == "run" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh run",
				Module:   r.Name(),
			}
		}
		if readOnlyRelease[subcmd] && resource == "release" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh release",
				Module:   r.Name(),
			}
		}
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func isGhExecutable(exec string) bool {
	return exec == "gh" || filepath.Base(exec) == "gh"
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// extractRunID returns the first positional (non-flag) argument after the
// "rerun" subcommand in a gh run rerun invocation. Returns "" if not found.
func extractRunID(args []string) string {
	// args layout: ["run", "rerun", ...rest]
	// Find "rerun" index and scan after it for first non-flag arg.
	rerunIdx := -1
	for i, a := range args {
		if a == "rerun" {
			rerunIdx = i
			break
		}
	}
	if rerunIdx < 0 {
		return ""
	}
	for _, a := range args[rerunIdx+1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		// Only return if all characters are digits (run IDs are numeric).
		allDigits := len(a) > 0
		for _, c := range a {
			if !unicode.IsDigit(c) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return a
		}
	}
	return ""
}
