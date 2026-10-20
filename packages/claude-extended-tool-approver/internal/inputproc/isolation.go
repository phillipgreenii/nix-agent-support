package inputproc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

// This file is the DROP RULE for input-processor rewrites (bead pg2-jwilh).
//
// Claude Code's worktree-isolation guard refuses, in a session isolated with
// EnterWorktree (or an `isolation: worktree` subagent), any command whose
// program is not a known launcher (env, nice, nohup, sudo, timeout, ...) and
// that has a git-named word among its operands: "this command runs rtk with a
// git command among its operands ... Refusing to run it". The guard judges the
// REWRITTEN command (CETA hands the rewrite to Claude Code as updatedInput), so
// `git commit -m x` rewritten to `rtk git commit -m x` is refused even though
// the original would have run. The subcommand is irrelevant: `rtk git status`
// is refused exactly like `rtk git commit`, and so is `rtk rg -n git src`.
//
// The hook payload carries no isolation field, so the only available signal is
// the payload cwd: an isolated session's cwd is always inside a linked git
// worktree. DropRewriteForIsolation combines that signal with a predicate over
// the rewritten command. Both halves are deliberately a SUPERSET of what the
// guard refuses, because dropping a rewrite is always safe (the original
// command runs, merely without compaction) while emitting a refused one breaks
// the agent. Only rewrites that are BOTH in a linked worktree AND guard-hostile
// are dropped, so compaction of every other command, and in every non-worktree
// session, is kept. See ADR 0070's pg2-jwilh amendment.

// gitNamedRE is Claude Code's git-name test on a normalised word.
var gitNamedRE = regexp.MustCompile(`^git(?:\.exe|\.real|-[a-z][\w-]*)?$`)

// gitNamed applies Claude Code's normalisation to one word and reports whether
// it is git-named: strip a leading `-opt=` prefix, take the part after the last
// `/`, strip a `:...` or `@...` suffix, lowercase, then match gitNamedRE.
func gitNamed(word string) bool {
	if strings.HasPrefix(word, "-") {
		if i := strings.Index(word, "="); i >= 0 {
			word = word[i+1:]
		}
	}
	if i := strings.LastIndex(word, "/"); i >= 0 {
		word = word[i+1:]
	}
	if i := strings.IndexAny(word, ":@"); i >= 0 {
		word = word[:i]
	}
	return gitNamedRE.MatchString(strings.ToLower(word))
}

// InLinkedWorktree reports whether cwd is inside a linked git worktree: walking
// up from cwd, the first directory that holds a `.git` entry has a `.git` that
// is a regular FILE beginning with `gitdir:` (pathspec.IsWorktreeRoot). A `.git`
// directory (a primary clone), or no `.git` ancestor at all, is false.
// Filesystem-only; it never runs a git subprocess.
func InLinkedWorktree(cwd string) bool {
	if cwd == "" {
		return false
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return pathspec.IsWorktreeRoot(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// GuardHostile reports whether Claude Code's worktree-isolation guard could
// refuse command: some simple command in it has an executable that is not
// git-named and a git-named word among its arguments. It ignores the guard's
// last-operand exception and known-launcher exemption (a superset). A command
// that cannot be parsed is treated as hostile.
func GuardHostile(command string) bool {
	parsed := cmdparse.ParseShell(command)
	if parsed.Unparseable {
		return true
	}
	for _, leaf := range parsed.Leaves {
		if gitNamed(leaf.Executable) {
			continue
		}
		for _, arg := range leaf.Args {
			if gitNamed(arg) {
				return true
			}
		}
	}
	return false
}

// DropRewriteForIsolation reports whether an input-processor rewrite MUST be
// dropped (the original command run instead): the payload cwd is inside a
// linked worktree AND the rewritten command is guard-hostile.
func DropRewriteForIsolation(cwd, rewritten string) bool {
	return InLinkedWorktree(cwd) && GuardHostile(rewritten)
}
