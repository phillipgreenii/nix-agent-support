// Package primarycommit gates a `git commit` on the PRIMARY branch of the CANONICAL
// clone (the main working tree — the real .git directory, never a linked worktree).
// It returns Reject only when the session is auto-approving (permission_mode ==
// "bypassPermissions"): such a session would silently accept an Ask, so a hard deny is
// the only thing that stops it. Interactive/default sessions get Abstain — no
// every-commit prompt; a human directing a primary commit is permitted (R-6) and left
// to the normal flow. Worktrees, feature branches, non-commit git, and any resolver
// error all Abstain (fail-open; the worktree discipline is the primary control).
package primarycommit

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

const bypassMode = "bypassPermissions"

type PrimaryResolver interface {
	IsCanonical(dir string) (bool, error)     // main working tree (real .git dir), not a worktree
	PrimaryBranch(dir string) (string, error) // .git/config override, else "main"
	CurrentBranch(dir string) (string, error) // "" on detached HEAD
}

type Rule struct{ resolver PrimaryResolver }

func New(resolver PrimaryResolver) *Rule { return &Rule{resolver: resolver} }

func (r *Rule) Name() string { return "primary-commit" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	abstain := hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	if input.ToolName != "Bash" {
		return abstain
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return abstain
	}
	for _, pc := range cmdparse.Parse(cmdStr) {
		if !isGit(pc.Executable) {
			continue
		}
		chdirs, subcmd, _ := cmdparse.GitInvocation(pc.Args)
		if subcmd != "commit" {
			continue
		}
		if r.resolver == nil {
			return abstain
		}
		dir := effectiveDir(input.CWD, chdirs)
		canonical, err := r.resolver.IsCanonical(dir)
		if err != nil || !canonical {
			return abstain
		}
		primary, err := r.resolver.PrimaryBranch(dir)
		if err != nil || primary == "" {
			return abstain
		}
		cur, err := r.resolver.CurrentBranch(dir)
		if err != nil || cur == "" || cur != primary {
			return abstain
		}
		// Commit on canonical primary. Block only an auto-approving session (which would
		// otherwise silently accept); trust interactive/default sessions (R-6).
		if input.PermissionMode == bypassMode {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "primary-commit: refusing a commit on the primary branch (" + primary + ") of the canonical clone in an auto-approving session — advancing shared primary requires explicit human direction (R-6); use a feature branch/worktree.",
				Module:   r.Name(),
			}
		}
		return abstain
	}
	return abstain
}

func isGit(exec string) bool { return exec == "git" || filepath.Base(exec) == "git" }

func effectiveDir(cwd string, chdirs []string) string {
	dir := cwd
	for _, c := range chdirs {
		if filepath.IsAbs(c) {
			dir = c
		} else {
			dir = filepath.Join(dir, c)
		}
	}
	return dir
}
