// Package primarypush gates a `git push` that would ADVANCE the PRIMARY branch of
// the CANONICAL clone (the main working tree — the real .git directory, never a
// linked worktree) on the shared remote. It is the push-side sibling of the
// primary-commit rule and reuses primary-commit's PrimaryResolver so the two rules
// share canonical/primary/current-branch detection.
//
// It returns Reject only when the session is in an AUTO-ACCEPTING permission mode —
// the set {bypassPermissions, auto, dontAsk} — because such a session would silently
// accept an Ask, so a hard deny is the only thing that stops it. Interactive modes
// (default/plan/acceptEdits/empty) get Abstain — no every-push prompt; a human
// directing a push to primary is permitted (R-6) and left to the normal flow.
//
// A push is judged to advance primary when any of:
//   - a refspec's REMOTE side names the primary branch — `origin HEAD:main`,
//     `origin feat:main`, `origin main`, `origin :main`, `+HEAD:main`,
//     `HEAD:refs/heads/main`;
//   - a same-name `HEAD`/`@` source (`git push origin HEAD`) while the canonical
//     clone is currently ON primary (the remote branch is then the current branch);
//   - a refspec's remote side is DYNAMIC (`$…`/backtick) — unprovable, so it fails
//     safe (denied in an auto-approving session);
//   - `--all` / `--mirror` (push every local branch, primary included);
//   - no refspec (bare `git push` / `git push origin`) while the canonical clone is
//     ON primary, OR with push.default=matching — whether injected as
//     `-c push.default=matching` OR set AMBIENTLY in git config (local .git/config or
//     the user-global file) — which pushes all same-name branches, primary included,
//     regardless of the current branch.
//
// A push of a FEATURE branch (remote side != primary) stays Approve; pushes from a
// linked worktree, non-push git, and any resolver error all Abstain (fail-open; the
// worktree discipline is the primary control — R-2/R-8).
//
// A push hidden behind a git alias — command-line `git -c alias.p='push …' p` or a
// config `[alias] p = push …` (global or local) — IS recognized: the rule expands the
// alias (once, git's single-pass rule) via the resolver's Aliases plus the injected
// `-c alias.X=` before gating on the subcommand (tc-2phi8). A SHELL alias (`!…`) has
// its body re-parsed and its git commands re-checked. Residual limitation: an exotic
// shell-alias body whose command parser cannot recover the underlying `git push`
// (e.g. one assembled by string interpolation) is not seen — it matters only in an
// already auto-approving session, where the worktree discipline remains the control.
package primarypush

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarycommit"
)

// autoApprovingModes is the set of permission modes that silently accept an Ask, so a
// push advancing the canonical primary under one of these MUST be hard-denied. Kept in
// lockstep with primarycommit's identical set (both encode the same R-6/R-8 posture).
var autoApprovingModes = map[string]bool{
	"bypassPermissions": true,
	"auto":              true,
	"dontAsk":           true,
}

// pushValueFlags are `git push` options that CONSUME the following token as their
// value, so that token must not be mistaken for a refspec (e.g. `--push-option main`).
var pushValueFlags = map[string]bool{
	"-o": true, "--push-option": true, "--repo": true,
	"--receive-pack": true, "--exec": true,
}

type Rule struct{ resolver primarycommit.PrimaryResolver }

func New(resolver primarycommit.PrimaryResolver) *Rule { return &Rule{resolver: resolver} }

func (r *Rule) Name() string { return "primary-push" }

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
		if r.resolver == nil {
			return abstain
		}
		advances, primary := r.pushAdvancesPrimary(pc, input.CWD, true)
		if !advances {
			continue
		}
		// Push advances the canonical primary. Block only an auto-approving session
		// (which would otherwise silently accept); trust interactive/default sessions (R-6).
		if autoApprovingModes[input.PermissionMode] {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "primary-push: refusing a push that advances the primary branch (" + primary + ") of the canonical clone in an auto-approving session — advancing shared primary requires explicit human direction / PR flow (R-6/R-8); push a feature branch instead.",
				Module:   r.Name(),
			}
		}
		return abstain
	}
	return abstain
}

// pushAdvancesPrimary reports whether a single parsed git invocation would advance the
// canonical clone's primary branch on the shared remote, and returns that primary's
// name. When expandAliases is true it first expands a git alias hiding the subcommand
// (tc-2phi8): a normal alias is expanded once and re-checked; a SHELL alias (`!…`) has
// its body re-parsed and each git command in it checked with expansion OFF (single-pass,
// which also bounds recursion). A resolver error, a linked worktree, or a feature-branch
// push all return false — the fail-open posture (R-2/R-8: the worktree discipline is the
// primary control).
func (r *Rule) pushAdvancesPrimary(pc cmdparse.ParsedCommand, cwd string, expandAliases bool) (bool, string) {
	chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
	dir := effectiveDir(cwd, chdirs)
	if expandAliases {
		effSubcmd, effRest, shellBody := primarycommit.ResolveGitAlias(subcmd, rest, r.mergedAliases(dir, pc.Args))
		if shellBody != "" {
			for _, sub := range cmdparse.Parse(shellBody) {
				if !isGit(sub.Executable) {
					continue
				}
				if advances, primary := r.pushAdvancesPrimary(sub, dir, false); advances {
					return true, primary
				}
			}
			return false, ""
		}
		subcmd, rest = effSubcmd, effRest
	}
	if subcmd != "push" {
		return false, ""
	}
	canonical, err := r.resolver.IsCanonical(dir)
	if err != nil || !canonical {
		return false, ""
	}
	primary, err := r.resolver.PrimaryBranch(dir)
	if err != nil || primary == "" {
		return false, ""
	}
	cur, err := r.resolver.CurrentBranch(dir)
	if err != nil {
		return false, ""
	}
	// `-c push.default=matching` is consumed by GitInvocation before `rest`, so detect
	// the injected form from the full arg list; the AMBIENT form (set in ~/.gitconfig,
	// not injected) is read from the resolver. Either makes a bare push advance primary.
	matching := hasMatchingPushDefault(pc.Args) || r.pushDefaultIsMatching(dir)
	if !pushTargetsPrimary(rest, primary, cur, matching) {
		return false, ""
	}
	return true, primary
}

// mergedAliases returns the aliases visible to this invocation — config-defined
// (resolver, tolerating an error as none) merged with command-line-injected `-c
// alias.X=`, injected overriding (git: `-c` beats config).
func (r *Rule) mergedAliases(dir string, args []string) map[string]string {
	cfg, err := r.resolver.Aliases(dir)
	if err != nil {
		cfg = nil
	}
	return primarycommit.MergeAliases(cfg, primarycommit.InjectedAliases(args))
}

// pushDefaultIsMatching reports whether the effective push.default (per the resolver)
// is "matching" (case-insensitive). A resolver error is tolerated as false (fail-open).
func (r *Rule) pushDefaultIsMatching(dir string) bool {
	v, err := r.resolver.PushDefault(dir)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(v), "matching")
}

// pushTargetsPrimary reports whether a `git push` whose args are `rest` (everything
// after the "push" subcommand) would advance the `primary` branch on the shared
// remote. `cur` is the canonical clone's current branch (consulted for the no-refspec
// and same-name `HEAD` cases); `matchingPushDefault` is true when the invocation
// injected `-c push.default=matching`.
func pushTargetsPrimary(rest []string, primary, cur string, matchingPushDefault bool) bool {
	// --all / --mirror push every local branch (primary included) regardless of the
	// current branch or any refspec.
	for _, a := range rest {
		if a == "--all" || a == "--mirror" {
			return true
		}
	}
	// Collect positional (non-flag) args, skipping the VALUE of value-consuming push
	// options so e.g. `--push-option main` is not read as a `main` refspec.
	var positional []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if pushValueFlags[a] {
			i++ // skip this flag's value token
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		positional = append(positional, a)
	}
	// positional is [remote?] [refspec...]; positional[0] is a remote name when it
	// has no ":".
	refspecs := positional
	if len(positional) > 0 && !strings.Contains(positional[0], ":") {
		refspecs = positional[1:]
	}
	if len(refspecs) == 0 {
		// bare `git push` / `git push origin`. push.default=matching pushes ALL
		// same-name branches, so primary advances regardless of `cur`; otherwise the
		// default push advances primary iff currently ON primary.
		if matchingPushDefault {
			return true
		}
		return cur != "" && cur == primary
	}
	for _, rs := range refspecs {
		if refspecTargetsPrimary(rs, primary, cur) {
			return true
		}
	}
	return false
}

// refspecTargetsPrimary reports whether a single push refspec advances `primary`.
// It looks only at the REMOTE (destination) side, normalizing a leading "+" (force)
// and any "refs/heads/"/"heads/" prefix. A same-name `HEAD`/`@` source resolves to
// the current branch `cur`. A dynamic remote side ($…/backtick) cannot be proven safe,
// so it is treated as targeting primary (fails safe under an auto-approving session).
func refspecTargetsPrimary(refspec, primary, cur string) bool {
	spec := strings.TrimPrefix(refspec, "+")
	hasColon := strings.Contains(spec, ":")
	remote := spec
	if hasColon {
		remote = spec[strings.Index(spec, ":")+1:]
	}
	if strings.ContainsAny(remote, "$`") {
		return true // unresolved dynamic remote side — fail safe
	}
	remote = strings.TrimPrefix(remote, "refs/heads/")
	remote = strings.TrimPrefix(remote, "heads/")
	if !hasColon && (remote == "HEAD" || remote == "@") {
		// `git push origin HEAD` pushes the CURRENT branch to its same-name remote.
		return cur != "" && cur == primary
	}
	return remote == primary
}

// hasMatchingPushDefault reports whether the git args carry `-c push.default=matching`
// (the value token "push.default=matching" survives in the arg list).
func hasMatchingPushDefault(args []string) bool {
	for _, a := range args {
		if eq := strings.Index(a, "="); eq >= 0 {
			key := strings.ToLower(strings.TrimSpace(a[:eq]))
			val := strings.ToLower(strings.TrimSpace(a[eq+1:]))
			if key == "push.default" && val == "matching" {
				return true
			}
		}
	}
	return false
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
