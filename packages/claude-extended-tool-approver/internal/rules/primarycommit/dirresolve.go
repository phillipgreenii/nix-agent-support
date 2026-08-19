package primarycommit

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
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
//
// ==================== THE ONE VALUE THAT IS NOT A GUESS (pg2-wq3ki) ====================
//
// "REFUSE TO GUESS" is about run-time values. It is NOT about a value the command
// itself writes down:
//
//	WT=/abs/worktree && git -C "$WT" commit -m x
//	WT=/abs/worktree; cd "$WT" && git commit -m x
//
// There `$WT` is a property of the command TEXT, and reading it is not guessing — it is
// the same static reading the literal `git -C /abs/worktree commit` already gets. So
// ResolveDir first expands each `-C` argument through cmdparse.ExpandInCommand against
// the in-command environment (hookio.HookInput.InCommandVars, computed by the engine per
// leaf), and the ENGINE does the same for a `cd`/`pushd` target before its verbatim join
// — it must be done there, because the join is what loses the value's absoluteness.
// Everything the environment cannot resolve keeps EXACTLY the verdict above: an unknown
// name is ABSENT from that map, never empty, so the fail-safe path is unchanged.
//
// DECLINED, and this is the half the bead left open: DERIVING A `$(…)` VALUE. The
// tempting case is `WT=$(git rev-parse --show-toplevel)`, whose answer this package
// already computes for the cwd (resolver.go's gitRoot walks up to the first `.git`). It
// is declined on the FAILURE mode, not on the happy path, and the failure mode was
// MEASURED (git 2.54.0 / bash, 2026-08-13):
//
//   - When the substitution FAILS — a bare repo, a cwd inside `.git`, an ambient
//     `GIT_DIR`/`GIT_WORK_TREE` this hook cannot see (CETA receives no environment) —
//     the shell assigns the EMPTY STRING, and empty is not inert:
//     `git -C "" commit` EXITS 0 AND COMMITS IN THE CURRENT DIRECTORY (measured: the
//     commit landed in the cwd's repo), and unquoted `cd $WT` with an unset value is
//     `cd` with no argument, i.e. $HOME. In the layout this rule exists to protect the
//     current directory IS the canonical clone on its primary branch, so a derivation
//     that is wrong in exactly the way a derivation is wrong turns an Ask into an
//     APPROVAL of the commit this rule was written to stop. (Only the QUOTED
//     `cd "$WT"` form fails closed: bash rejects a null directory with exit 1, so the
//     `&&` short-circuits.)
//   - The value would also have to be derived AT THE ASSIGNMENT'S cwd, not the
//     consuming leaf's: in `cd /a && WT=$(git rev-parse --show-toplevel) && cd /b &&
//     git -C "$WT" commit` those differ, and the seam that carries the environment
//     carries no per-assignment cwd. Adding one is a different, stateful seam.
//
// So the ALLOWLIST IDEA IS NOT REJECTED IN PRINCIPLE, but it is not admissible until
// the empty/failed value reaches the UNRESOLVED verdict rather than a directory. A
// future attempt MUST start there. Until then a `$(…)` value is refused by
// cmdparse.literalAssignedValue and this rule's verdict for it is unchanged — which is
// what internal/engine's in-command-vars integration cases pin.

// DirResolution is the directory a git invocation will run in, plus — when that cannot
// be established — the evidence for saying so.
//
// Dir is ALWAYS populated, including when the resolution failed: callers that need a
// directory for a best-effort side lookup (this rule reads git aliases out of whatever
// `.git/config` encloses it) keep the value they had before, while callers deciding a
// VERDICT must branch on Unresolved first.
type DirResolution struct {
	Dir    string // the directory the invocation runs in (best-effort when Unresolved)
	Chosen string // how Dir was chosen, for a reason text (dirProvenance)
	Token  string // the text that defeated static resolution, as written; "" when resolved
	Source string // which input that text came from, for a reason text
}

// Unresolved reports that the directory could not be established from the command text.
// A caller MUST NOT derive a verdict from Dir in that case — that is the false deny
// pg2-h2npt was filed for.
func (d DirResolution) Unresolved() bool { return d.Token != "" }

// ResolveDir resolves the directory a git invocation runs in from the three sources the
// DIRECTORY RESOLUTION comment names: the running cwd, the `git -C` options in order,
// and the in-command variable environment (nil when there is none, which is the ordinary
// case and reproduces the pre-pg2-wq3ki behaviour exactly).
//
// EXPORTED AS A SEAM, and deliberately not inlined into this rule's own flow: the
// primary-push rule has the identical directory-resolution gap (bead pg2-eqacu, where
// the failure mode is a Reject rather than an Ask) and already imports this package for
// PrimaryResolver and the alias helpers, so this is the one place both rules can consume
// ONE resolution model instead of two that drift. pg2-eqacu is expected to replace
// primarypush's private effectiveDir with a call to this.
//
// The `git -C` arguments are checked BEFORE the cwd because a `-C` is what decides the
// directory when present — it is the text the author wrote and the one they must fix.
func ResolveDir(cwd string, chdirs []string, vars map[string]string) DirResolution {
	resolved := make([]string, 0, len(chdirs))
	notes := make([]string, 0, len(chdirs))
	for _, c := range chdirs {
		lit, note := c, ""
		if unresolvableToken(c) != "" {
			// Only ever consulted for a token that is NOT already literal, so a plain
			// path can never be rewritten by a same-named variable.
			if expanded, ok := cmdparse.ExpandInCommand(c, vars); ok && unresolvableToken(expanded) == "" {
				lit = expanded
				note = " (resolved to " + expanded + " from an assignment earlier in the same command)"
			}
		}
		if t := unresolvableToken(lit); t != "" {
			return DirResolution{
				Dir:    effectiveDir(cwd, chdirs),
				Token:  t,
				Source: "the `git -C " + c + "` argument",
			}
		}
		resolved = append(resolved, lit)
		notes = append(notes, note)
	}
	if t := unresolvableToken(cwd); t != "" {
		// The cwd arrives ALREADY EXPANDED where the engine could expand it (a
		// `cd "$WT"` whose value the command establishes), so a token surviving into
		// it here is one nothing could resolve.
		return DirResolution{
			Dir:    effectiveDir(cwd, chdirs),
			Token:  t,
			Source: "the command's working directory (" + cwd + ")",
		}
	}
	return DirResolution{Dir: effectiveDir(cwd, resolved), Chosen: dirProvenance(chdirs, notes)}
}

// LeafVars overlays the variables established by the leaves BEFORE index i onto the
// environment the engine supplied, and is the second half of the pg2-eqacu seam.
//
// Under the engine `base` is hookio.HookInput.InCommandVars and `leaves` is the ONE leaf
// the rule was handed, so the overlay is empty and this returns base unchanged. A DIRECT
// caller (a unit test, or any non-engine caller) is handed the WHOLE expression instead,
// and then the earlier leaves of that expression are the assignments — the same
// leaf-scope-vs-expression-scope fallback internal/rules/gitdir applies to
// RootExpression.
func LeafVars(base map[string]string, leaves []cmdparse.ParsedCommand, i int) map[string]string {
	local := cmdparse.InCommandVars(leaves, i)
	if len(local) == 0 {
		return base
	}
	if len(base) == 0 {
		return local
	}
	merged := make(map[string]string, len(base)+len(local))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range local { // the nearer assignment wins
		merged[k] = v
	}
	return merged
}

// LeafTempDirVars is LeafVars' sibling for the fresh-temp-dir MARKER scan
// (cmdparse.InCommandTempDirVars) rather than the literal-value scan
// (cmdparse.InCommandVars) — same base/local overlay, same pg2-eqacu reason:
// under the engine `base` is hookio.HookInput.InCommandTempDirVars, precomputed
// once per leaf from the WHOLE expression, and `leaves` is the ONE leaf the
// rule was handed, so the local scan is empty and this returns base unchanged.
// A direct caller (a unit test, or envvars' own top-level reparse of a whole
// command) is handed the WHOLE expression instead, and the local scan finds
// everything itself. internal/rules/envvars (pg2-d71my) is currently its only
// consumer.
func LeafTempDirVars(base map[string]string, leaves []cmdparse.ParsedCommand, i int) map[string]string {
	local := cmdparse.InCommandTempDirVars(leaves, i)
	if len(local) == 0 {
		return base
	}
	if len(base) == 0 {
		return local
	}
	merged := make(map[string]string, len(base)+len(local))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range local { // the nearer assignment wins
		merged[k] = v
	}
	return merged
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
//
// notes carries a per-chdir suffix naming an expansion this resolution performed (empty
// for an argument that was already literal). It is positional — notes[i] belongs to
// chdirs[i] — and a short or nil slice contributes nothing, so a caller with no
// expansions to report may pass nil. The chdir is quoted AS WRITTEN and the resolved
// value stated beside it: an agent reading the prompt has to recognise its own text
// first, and telling it only the resolved path would hide which token was read.
func dirProvenance(chdirs []string, notes []string) string {
	note := func(i int) string {
		if i < len(notes) {
			return notes[i]
		}
		return ""
	}
	switch len(chdirs) {
	case 0:
		return "the command's working directory (no `git -C` given)"
	case 1:
		return "the `git -C " + chdirs[0] + "` option" + note(0)
	default:
		parts := make([]string, len(chdirs))
		for i, c := range chdirs {
			parts[i] = c + note(i)
		}
		return "the `git -C` options " + strings.Join(parts, " then ")
	}
}
