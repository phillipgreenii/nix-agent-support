// Package primarycommit gates a `git commit` on the PRIMARY branch of the CANONICAL
// clone (the main working tree — the real .git directory, never a linked worktree).
// It returns Reject only when the session is in an AUTO-ACCEPTING permission mode —
// the set {bypassPermissions, auto, dontAsk} — because such a session would silently
// accept an Ask, so a hard deny is the only thing that stops it. The deny-honoring
// mechanism is validated empirically for bypassPermissions (pg2-2t9wz); auto/dontAsk
// are covered by inference (bypass ⊃ auto) plus a unit case here. Interactive modes
// (default/plan/acceptEdits/empty) get Abstain — no every-commit prompt; a human
// directing a primary commit is permitted (R-6) and left to the normal flow.
// acceptEdits is moot: it auto-accepts edits, not Bash. Worktrees, feature branches,
// non-commit git, and any resolver error all Abstain (fail-open; the worktree
// discipline is the primary control).
//
// A `git commit` hidden behind an alias — command-line `git -c alias.ci='commit …' ci`
// or a config `[alias] ci = commit …` (global or local) — IS recognized: the rule
// expands the alias (once, git's single-pass rule) via the resolver's Aliases plus the
// injected `-c alias.X=` before gating on the subcommand (tc-2phi8). A SHELL alias
// (`!…`) has its body re-parsed and its git commands re-checked. Residual: an exotic
// shell-alias body whose command parser cannot recover the `git commit` (e.g. one built
// by string interpolation) is not seen — it only matters in an already auto-approving
// session, and the worktree discipline remains the primary control.
package primarycommit

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// autoApprovingModes is the set of permission modes that silently accept an Ask,
// so a commit on the canonical primary under one of these MUST be hard-denied.
var autoApprovingModes = map[string]bool{
	"bypassPermissions": true,
	"auto":              true,
	"dontAsk":           true,
}

type PrimaryResolver interface {
	IsCanonical(dir string) (bool, error)     // main working tree (real .git dir), not a worktree
	PrimaryBranch(dir string) (string, error) // .git/config override, else "main"
	CurrentBranch(dir string) (string, error) // "" on detached HEAD
	// PushDefault returns the effective push.default value (local .git/config overrides
	// the global config), or "" if unset. Consulted by primary-push to catch an ambient
	// push.default=matching that a bare push would ride into the primary branch.
	PushDefault(dir string) (string, error)
	// Aliases returns merged git aliases (global config first, local .git/config
	// overriding), keyed by alias name (lowered — git config keys are case-insensitive);
	// values are the raw alias bodies. Consulted to expand an alias that hides a guarded
	// subcommand (`git -c alias.p='push …' p`). nil when none are defined.
	Aliases(dir string) (map[string]string, error)
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
		if r.resolver == nil {
			return abstain
		}
		targets, primary := r.commitTargetsPrimary(pc, input.CWD, true)
		if !targets {
			continue
		}
		// Commit on canonical primary. Block only an auto-approving session (which would
		// otherwise silently accept); trust interactive/default sessions (R-6).
		if autoApprovingModes[input.PermissionMode] {
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

// commitTargetsPrimary reports whether a single parsed git invocation is a `git commit`
// on the canonical clone's primary branch, and returns that primary's name. When
// expandAliases is true it first expands a git alias hiding the subcommand (tc-2phi8):
// a normal alias is expanded once and re-checked; a SHELL alias (`!…`) has its body
// re-parsed and each git command in it checked with expansion OFF (single-pass, which
// also bounds recursion). A resolver error, a linked worktree, or being off primary all
// return false — the fail-open posture the worktree discipline relies on.
func (r *Rule) commitTargetsPrimary(pc cmdparse.ParsedCommand, cwd string, expandAliases bool) (bool, string) {
	chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
	dir := effectiveDir(cwd, chdirs)
	if expandAliases {
		effSubcmd, _, shellBody := ResolveGitAlias(subcmd, rest, r.mergedAliases(dir, pc.Args))
		if shellBody != "" {
			for _, sub := range cmdparse.Parse(shellBody) {
				if !isGit(sub.Executable) {
					continue
				}
				if targets, primary := r.commitTargetsPrimary(sub, dir, false); targets {
					return true, primary
				}
			}
			return false, ""
		}
		subcmd = effSubcmd
	}
	if subcmd != "commit" {
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
	if err != nil || cur == "" || cur != primary {
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
	return MergeAliases(cfg, InjectedAliases(args))
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
