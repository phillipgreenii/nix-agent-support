package primarycommit

import (
	"path/filepath"
	"strings"
)

// DIRECTORY RESOLUTION (pg2-h2npt)
//
// Every verdict this rule reaches rests on ONE input: the directory the `git commit`
// will actually run in. That directory is assembled from three sources, and the rule
// is only as sound as the weakest of them:
//
//  1. the SESSION cwd, arriving as hookio.HookInput.CWD;
//  2. a `cd` / `pushd` earlier in the same compound command — the ENGINE models this
//     and hands each leaf an ALREADY-ADVANCED CWD (internal/engine/engine.go's
//     EvaluateExpression, landed as pg2-opclh), so this rule needs no `cd` model of
//     its own and MUST NOT grow one;
//  3. `git -C <path>` options on the invocation itself (cmdparse.GitInvocation),
//     applied in order by effectiveDir.
//
// Sources 2 and 3 both carry the SHELL'S TEXT, not the shell's result: the command
// parser deliberately does not expand parameters, so `git -C $WT commit` arrives as
// the three literal tokens `-C`, `$WT`, `commit`, and `cd $WT` arrives as a `cd` whose
// argument is the three characters `$WT`. filepath.Join then yields a path that CANNOT
// EXIST — `<cwd>/$WT` — and FileResolver's gitRoot walks UP from it until it finds a
// `.git`, landing on whichever repository ENCLOSES the session cwd. In the very layout
// this rule exists to protect, that enclosing repository is the canonical clone sitting
// on the primary branch. So the rule concluded "commit on primary" about a commit
// really headed for a nested worktree on a feature branch, denied it, and blamed R-6 —
// telling the agent it was on a primary branch it had never been on.
//
// The fix is NOT to guess the expansion. It is to REFUSE TO GUESS: a directory that
// does not resolve to a literal path is UNRESOLVED, and an unresolved directory MUST
// NOT be silently replaced by the session cwd. unresolvedDir below is that test, and
// the rule's verdict for it is fail-safe in BOTH permission postures — never Approve.
//
// The engine's VERBATIM JOIN is load-bearing here and MUST NOT be "tidied away".
// Because the engine joins an unexpandable `cd` target into the running cwd exactly as
// written, the offending token SURVIVES into this rule's CWD and can be detected. An
// engine that instead skipped the advance would hand this rule a pristine session cwd,
// and the false deny would come back with nothing left to detect it by.
// TestIntegration_PrimaryCommitDirectoryResolution pins that coupling end to end.
//
// RESIDUAL: `--git-dir` / `--work-tree` are consumed by cmdparse.GitInvocation but not
// returned, so a commit redirected by them is judged against the cwd. That predates
// pg2-h2npt and is deliberately not widened here — the redirect forms are gated
// separately by internal/rules/git (a reset under a redirected GIT_DIR / GIT_WORK_TREE
// context Asks), and adding a fourth directory source to this rule without that rule's
// env-var half would model only one of the two spellings.

// unresolvedDir reports the FIRST directory input that does not resolve to a literal
// path, as (token, source): token is the offending text as written, and source is a
// phrase naming where it came from, for the reason text. Both are "" when every input
// is literal, which is the ordinary case.
//
// The `git -C` arguments are checked BEFORE the cwd because a `-C` is what decides the
// directory when present — it is the text the author wrote and the one they must fix.
func unresolvedDir(cwd string, chdirs []string) (token, source string) {
	for _, c := range chdirs {
		if t := unresolvableToken(c); t != "" {
			return t, "the `git -C " + c + "` argument"
		}
	}
	if t := unresolvableToken(cwd); t != "" {
		return t, "the command's working directory (" + cwd + ")"
	}
	return "", ""
}

// unresolvableToken returns the first path COMPONENT of p that the shell would rewrite
// before git ever saw it, or "" when p is fully literal. A RELATIVE path is literal —
// it joins onto the cwd deterministically, so `git -C sub commit` stays resolved and
// keeps its existing verdict.
//
// The markers are exactly the constructs whose value is a property of run time rather
// than of the command text:
//
//	$    parameter expansion and `$(…)` command substitution — `$WT`, `${WT}`, `$(pwd)`
//	`    backtick command substitution
//	* ?  pathname expansion (a glob's match set is a property of the filesystem)
//	~    tilde expansion, at the start of a component
//
// This is the same fail-safe test internal/rules/primarypush/primarypush.go's
// refspecTargetsPrimary already applies to a refspec's remote side ("unresolved dynamic
// remote side — fail safe"), widened from `$`/backtick to the two other expansions that
// can reach a PATH. Deliberately NOT treated as unresolvable: a `[…]` bracket, because
// a literal bracket in a directory name is likelier than a bracket glob in a `-C`
// argument, and a leading `-`, which is a git option this rule never receives as a path.
func unresolvableToken(p string) string {
	for _, comp := range strings.Split(filepath.ToSlash(p), "/") {
		if comp == "" {
			continue
		}
		if strings.ContainsAny(comp, "$`*?") || strings.HasPrefix(comp, "~") {
			return comp
		}
	}
	return ""
}

// dirProvenance names HOW the evaluated directory was chosen, so the reason text can
// STATE it rather than leave the agent to guess which of the three sources won. The
// wording is deliberately neutral about the cwd: this rule cannot tell a session cwd
// apart from one the engine advanced past a `cd`, and claiming either would be
// fabricated provenance on a user-facing prompt.
func dirProvenance(chdirs []string) string {
	switch len(chdirs) {
	case 0:
		return "the command's working directory (no `git -C` given)"
	case 1:
		return "the `git -C " + chdirs[0] + "` option"
	default:
		return "the `git -C` options " + strings.Join(chdirs, " then ")
	}
}
