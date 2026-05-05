package git

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var readOnlySubcommands = map[string]bool{
	// Porcelain inspection
	"log": true, "diff": true, "status": true, "show": true, "blame": true,
	"describe": true, "shortlog": true, "reflog": true, "grep": true,
	"show-branch": true, "whatchanged": true, "range-diff": true,
	// Plumbing: ref/object inspection
	"for-each-ref": true, "ls-files": true, "ls-remote": true, "ls-tree": true,
	"merge-base": true, "rev-list": true, "rev-parse": true, "show-ref": true,
	"name-rev": true, "cat-file": true, "count-objects": true,
	// Plumbing: diff variants
	"diff-tree": true, "diff-index": true, "diff-files": true,
	// Plumbing: verification/integrity
	"verify-commit": true, "verify-tag": true, "verify-pack": true, "fsck": true,
	// Plumbing: gitignore/gitattributes checks
	"check-ignore": true, "check-attr": true, "check-mailmap": true, "check-ref-format": true,
}

var remoteBlockedSubcommands = map[string]bool{
	"add": true, "remove": true, "rm": true, "rename": true,
	"set-url": true, "set-head": true, "set-branches": true,
}

var modifyingSubcommands = map[string]bool{
	"add": true, "commit": true, "branch": true, "fetch": true,
	"push": true, "stash": true, "config": true, "mu": true,
	"mv": true, "rm": true, "cherry-pick": true, "merge": true,
	"worktree": true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "git"
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
		if !isGitExecutable(pc.Executable) {
			continue
		}
		subcmd, rest := extractGitSubcommand(pc.Args)
		if subcmd == "" {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		if isDestructive(subcmd, rest) {
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "destructive git command",
				Module:   r.Name(),
			}
		}
		if readOnlySubcommands[subcmd] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only git command",
				Module:   r.Name(),
			}
		}
		if subcmd == "checkout" {
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "git checkout", Module: r.Name()}
		}
		// rebase: approve unless interactive without automated editor
		if subcmd == "rebase" {
			if hasFlag(rest, "-i") || hasFlag(rest, "--interactive") {
				if !hasSequenceEditorEnvVar(pc) {
					return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git rebase -i requires editor", Module: r.Name()}
				}
			}
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
		}
		// filter-branch: approve (history rewriting used by agents for commit cleanup)
		if subcmd == "filter-branch" {
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
		}
		// tag: always reject — tags cause confusion in this workflow
		if subcmd == "tag" {
			return hookio.RuleResult{Decision: hookio.Reject, Reason: "git: git tag is prohibited — tags cause confusion in this workflow", Module: r.Name()}
		}
		// remote: special handling
		if subcmd == "remote" {
			remoteSub := ""
			if len(rest) > 0 {
				remoteSub = rest[0]
			}
			if remoteBlockedSubcommands[remoteSub] {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git remote modifying command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only git remote", Module: r.Name()}
		}
		// modifying: approve (includes tag, mv, rm, worktree, etc.)
		if modifyingSubcommands[subcmd] {
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
		}
		// reset: approve unless --hard
		if subcmd == "reset" {
			if hasFlag(rest, "--hard") {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git:destructive: git reset --hard is destructive", Module: r.Name()}
			}
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "git:modifying: git reset (soft) is safe", Module: r.Name()}
		}
		if subcmd == "clean" {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git:destructive: git clean is destructive", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func isGitExecutable(exec string) bool {
	return exec == "git" || filepath.Base(exec) == "git"
}

func extractGitSubcommand(args []string) (subcmd string, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			i++
			if i < len(args) {
				i++
			}
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return a, args[i+1:]
		}
	}
	return "", nil
}

func hasNonFlagArg(args []string, target string) bool {
	for _, a := range args {
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if a == target {
			return true
		}
	}
	return false
}

func hasTagDestructiveFlags(args []string) bool {
	for _, a := range args {
		switch a {
		case "-d", "-f", "--delete", "-a":
			return true
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func hasRedirectEnvVar(pc cmdparse.ParsedCommand) bool {
	for _, ev := range pc.EnvVars {
		if ev.Name == "GIT_DIR" || ev.Name == "GIT_WORK_TREE" {
			return true
		}
	}
	return false
}

func hasSequenceEditorEnvVar(pc cmdparse.ParsedCommand) bool {
	for _, ev := range pc.EnvVars {
		if ev.Name == "GIT_SEQUENCE_EDITOR" {
			return true
		}
	}
	return false
}

func isDestructive(subcmd string, args []string) bool {
	if subcmd == "push" {
		hasForce := false
		hasForceWithLease := false
		for _, a := range args {
			switch a {
			case "--force", "-f":
				hasForce = true
			case "--force-with-lease":
				hasForceWithLease = true
			}
		}
		if hasForce {
			return true
		}
		if hasForceWithLease {
			return isPushCrossBranch(args)
		}
	}
	if subcmd == "branch" {
		for _, a := range args {
			if a == "-D" {
				return true
			}
		}
	}
	return false
}

// isPushCrossBranch checks if a git push has a cross-branch refspec (local:different).
// Returns true (destructive) if the refspec pushes to a different remote branch name.
// Returns false (safe) if remote is "origin" or absent, and branch names match or no refspec given.
func isPushCrossBranch(args []string) bool {
	// Extract positional args (non-flag) from push args
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		positional = append(positional, a)
	}
	// positional: [remote] [refspec...]
	// If remote is present and not "origin", treat as potentially unsafe
	remote := ""
	refspec := ""
	switch len(positional) {
	case 0:
		// git push --force-with-lease (defaults)
		return false
	case 1:
		// Could be remote or refspec; if it contains ":", it's a refspec
		if strings.Contains(positional[0], ":") {
			refspec = positional[0]
		} else {
			remote = positional[0]
		}
	default:
		remote = positional[0]
		refspec = positional[1]
	}
	// If remote is specified and not "origin", be cautious — treat as destructive
	if remote != "" && remote != "origin" {
		return true
	}
	// Check refspec for cross-branch push
	if refspec != "" && strings.Contains(refspec, ":") {
		parts := strings.SplitN(refspec, ":", 2)
		local := parts[0]
		remoteBranch := parts[1]
		if local != remoteBranch {
			return true
		}
	}
	return false
}
