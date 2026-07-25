package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
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

type Rule struct {
	// eval gates a `-C <path>` chdir against path-safety zones. When nil (legacy
	// construction) the `-C` path check is skipped and behavior is unchanged.
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
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
		// A pre-subcommand -c / --config-env injects arbitrary git config (e.g.
		// -c core.pager="touch /tmp/pwned") that runs on an otherwise read-only
		// subcommand — a known RCE class. Defer to Claude's prompt. Scoped to the
		// pre-subcommand span so `git commit -c <commit>` (a different flag) and
		// `git -C <path>` are NOT falsely abstained.
		if hasGitConfigInjection(pc.Args) {
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git: -c/--config-env injects config; deferring to prompt", Module: r.Name()}
		}
		chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
		if subcmd == "" {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		res := r.classify(pc, subcmd, rest)
		// A `-C <path>` chdir runs the subcommand against a directory other than
		// the invocation CWD. When the rule would otherwise Approve, demote to
		// Abstain if that directory is unsafe for the subcommand's access class:
		// a read-only subcommand needs the dir to be readable, a modifying one
		// needs it writable. Most-restrictive aggregation then defers to the
		// prompt (Abstain, never a hard Reject — the check uses CanRead/CanWrite,
		// not IsDeny*) instead of auto-approving a write into an unknown zone
		// (pg2-b3eow). Gated to a non-empty -C so a bare git command keeps its
		// verdict regardless of the CWD's zone.
		if res.Decision == hookio.Approve && !r.chdirSafe(input.CWD, chdirs, subcmd) {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "git: -C target directory is unsafe for a " + subcmd + " (deferred to claude-code)",
				Module:   r.Name(),
			}
		}
		return res
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// classify returns the base verdict for a git subcommand, independent of any
// `-C` path-safety concern (which Evaluate layers on top via chdirSafe).
func (r *Rule) classify(pc cmdparse.ParsedCommand, subcmd string, rest []string) hookio.RuleResult {
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

// chdirSafe reports whether the `-C` target directory is in a zone appropriate
// for the subcommand's access class. Read-only subcommands — and a read-only
// `git remote` — require the directory be READABLE; every other approvable
// subcommand (checkout, rebase, filter-branch, soft reset, and the modifying
// set) writes and requires it be WRITABLE. Returns true (no gate) when no `-C`
// is present or no evaluator is configured, preserving legacy behavior.
func (r *Rule) chdirSafe(cwd string, chdirs []string, subcmd string) bool {
	if r.eval == nil || len(chdirs) == 0 {
		return true
	}
	access := r.eval.Evaluate(effectiveDir(cwd, chdirs))
	if readOnlySubcommands[subcmd] || subcmd == "remote" {
		return access.CanRead()
	}
	return access.CanWrite()
}

// effectiveDir folds git's `-C` chdir values onto cwd the way git does: an
// absolute chdir resets the running directory, a relative one is joined onto
// it. A leading `~` is expanded to the user's home so an unexpanded tilde
// cannot be mistaken for an in-project directory (a shell would expand it
// before git runs). Mirrors primarycommit.effectiveDir (unexported there); any
// env var in the folded path is expanded by patheval.PathEvaluator.Evaluate.
func effectiveDir(cwd string, chdirs []string) string {
	dir := cwd
	for _, c := range chdirs {
		c = expandTilde(c)
		if filepath.IsAbs(c) {
			dir = c
		} else {
			dir = filepath.Join(dir, c)
		}
	}
	return dir
}

// expandTilde expands a leading `~` or `~/` to the user's home directory,
// mirroring patheval's cleanPath so a `-C ~/...` value is resolved to an
// absolute path before zone classification. Non-tilde paths are returned as-is.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func isGitExecutable(exec string) bool {
	return exec == "git" || filepath.Base(exec) == "git"
}

// hasGitConfigInjection reports whether a pre-subcommand -c or --config-env
// flag is present. It scans only the option span before the git subcommand
// (mirroring cmdparse.GitInvocation's flag-consuming walk) so that a -c appearing
// AFTER the subcommand (e.g. `git commit -c <commit>`) is not matched.
func hasGitConfigInjection(args []string) bool {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-c" {
			return true
		}
		if a == "--config-env" || strings.HasPrefix(a, "--config-env=") {
			return true
		}
		switch a {
		case "-C", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return false // first non-flag token is the subcommand
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
