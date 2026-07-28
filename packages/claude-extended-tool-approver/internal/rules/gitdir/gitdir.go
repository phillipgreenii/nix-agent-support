// Package gitdir blocks direct access to a repository's `.git` directory —
// reading or writing files under `.git/` — so git metadata is only ever
// modified through the git porcelain, never by an agent poking at the object
// store, refs, config, or hooks directly (a hook-support parity capability;
// GitDirectoryEvaluator).
//
// Decision policy: Reject. This is a hard security block (matching
// hook-support's DENY with confidence 1.0), consistent with ceta's other
// hard-block rules (`assume` Rejects assume-role; `config-rules` Rejects
// blocked basenames). It runs EARLY in the chain — after the consumer
// `config-rules` so an explicit consumer decision still wins, but before the
// generic path/command approvers so a `.git` read is never silently approved
// by `path-safety` or `safe-commands`.
package gitdir

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "git-directory" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	switch input.ToolName {
	case "Bash":
		if cmd, err := input.BashCommand(); err == nil && isGitDirAccess(cmd) {
			return r.reject()
		}
	case "Read", "Write", "Edit", "MultiEdit", "Delete":
		if path, err := input.FilePath(); err == nil && isGitDirAccess(path) {
			return r.reject()
		}
	case "Glob", "Grep":
		if path, err := input.SearchPath(); err == nil && isGitDirAccess(path) {
			return r.reject()
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) reject() hookio.RuleResult {
	return hookio.RuleResult{
		Decision: hookio.Reject,
		Reason:   "access to .git directory is blocked — modify git metadata through git commands only",
		Module:   r.Name(),
	}
}

// isGitDirAccess reports whether s references a path inside a `.git` directory.
// The substring set mirrors hook-support's GitDirectoryEvaluator so parity is
// behaviorally faithful: it matches a `.git/` path component anywhere, a
// trailing `/.git`, and a leading `.git/` token — while NOT matching the bare
// `git` executable (e.g. `git status` carries no `.git/` token).
func isGitDirAccess(s string) bool {
	return s == ".git" ||
		s == ".git/" ||
		strings.Contains(s, "/.git/") ||
		strings.HasSuffix(s, "/.git") ||
		strings.Contains(s, "/.git ") ||
		strings.Contains(s, " .git/") ||
		strings.HasPrefix(s, ".git/")
}
