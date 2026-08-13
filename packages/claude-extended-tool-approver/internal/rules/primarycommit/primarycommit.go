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
//
// The one case that is DECISIVE IN EVERY MODE is a commit whose target directory does
// not resolve to a literal path — `git -C $WT commit`, `cd $WT && git commit`. There
// the rule has not established that the commit is on primary; it has established that
// it CANNOT TELL, and the fail-open Abstain above is unavailable because the generic
// git rule behind it approves a plain `git commit`. So an unresolved target gets a
// fail-safe verdict of its own — Ask interactively, Reject in an auto-approving session
// where an Ask would be silently accepted — and NEVER reaches Approve. This is the
// same "identity check I could not complete" carve-out ADR 0043's error policy names
// for killshell, and its rationale, the resolution model, and the coupling to the
// engine's `cd` handling are all in dirresolve.go's DIRECTORY RESOLUTION comment.
package primarycommit

import (
	"fmt"
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

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	// Every former `return abstain` in this function meant "keep going": this rule
	// is registered BEFORE the generic git rule precisely so it can hard-deny an
	// auto-approving commit on primary before git approves it, which requires git
	// to still be reached in every other case. So they are all ErrNotApplicable and
	// none may become a terminal NoOpinion — including the unresolved-directory
	// branch below, which stops the chain with a DECISIVE Ask/Reject rather than the
	// NoOpinion that an auto-approving session would accept.
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		// Genuine failure: the tool IS Bash, so this rule governs the input.
		return hookio.RuleResult{}, fmt.Errorf("primary-commit: read bash command: %w", err)
	}
	for _, pc := range cmdparse.Parse(cmdStr) {
		if !isGit(pc.Executable) {
			continue
		}
		// No resolver injected: the rule has no way to know what the primary is, so
		// it does not govern this input. This is a CONSTRUCTION condition, not a
		// runtime failure, so it is not-applicable rather than an error.
		if r.resolver == nil {
			return hookio.NotApplicable()
		}
		f := r.inspectCommit(pc, input.CWD, true)
		auto := autoApprovingModes[input.PermissionMode]
		switch f.kind {
		case findingUnresolved:
			// FAIL-SAFE, and the ONLY branch of this rule that is decisive in an
			// interactive session. The rule has NOT found a commit on primary — it has
			// found that it cannot tell where the commit lands, which is the one state
			// the fail-open Abstain cannot express: behind this rule the generic git
			// rule approves a plain `git commit`, so returning ErrNotApplicable here
			// would let an unresolvable target reach Approve. Ask keeps the verdict
			// non-approving and puts the diagnosis in front of the agent; the
			// auto-approving modes get Reject instead, because they silently accept an
			// Ask and the old behaviour there was already a (wrongly-reasoned) Reject —
			// no spelling may come out of this change more permissive than it went in.
			if auto {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: f.unresolvedReason(true), Module: r.Name()}, nil
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: f.unresolvedReason(false), Module: r.Name()}, nil
		case findingPrimary:
			// Commit on canonical primary. Block only an auto-approving session (which
			// would otherwise silently accept); trust interactive/default sessions (R-6).
			if auto {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: f.primaryReason(), Module: r.Name()}, nil
			}
			// Commit on primary in an interactive/default session: trusted (R-6), and the
			// git rule after us still gets to judge the command on its own merits.
			return hookio.NotApplicable()
		}
	}
	// No git leaf, or none that targets primary.
	return hookio.NotApplicable()
}

// findingKind classifies what the rule established about one parsed git invocation.
// The third member is why this is an enum rather than the former bool: "this commit
// lands on the canonical primary" and "I could not work out where this commit lands at
// all" are different facts with different remedies, and folding the second into the
// first is exactly the false deny pg2-h2npt reports.
type findingKind int

const (
	findingNone       findingKind = iota // not a commit, not canonical, or off primary
	findingPrimary                       // a commit on the canonical clone's primary branch
	findingUnresolved                    // a commit whose target directory is not statically resolvable
)

// commitFinding carries the finding plus the evidence its reason text cites. The
// evidence fields are populated per kind: primary/dir/chosen for findingPrimary,
// token/source for findingUnresolved.
type commitFinding struct {
	kind    findingKind
	primary string // the primary branch the commit would advance
	dir     string // the directory that was evaluated
	chosen  string // how that directory was chosen (dirProvenance)
	token   string // the text that defeated static resolution, as written
	source  string // which input that text came from (unresolvedDir)
}

// primaryReason states WHICH directory was evaluated, HOW it was chosen, WHY that made
// this a primary commit on the canonical clone, and what to do instead.
//
// The R-6 citation is kept but DEMOTED to the last clause. Naming the rule was all the
// old text did, so an agent whose real problem was a mis-resolved directory was told it
// was on a primary branch — and had no way to discover which directory the rule had
// actually looked at (pg2-h2npt).
func (f commitFinding) primaryReason() string {
	return "primary-commit: refusing this commit. Directory evaluated: " + f.dir +
		" (chosen from " + f.chosen + "). That directory is the CANONICAL clone — its .git is a real directory, not a linked worktree — and its HEAD is on \"" +
		f.primary + "\", the primary branch. This session auto-accepts prompts, so a hard deny is the only thing that stops it; advancing shared primary needs explicit human direction (R-6). " +
		"If you meant to commit inside a worktree, name it with a LITERAL absolute path: `git -C /abs/worktree commit …` or `cd /abs/worktree && git commit …`."
}

// unresolvedReason states that the rule COULD NOT DETERMINE the target, names the text
// that defeated it, and explicitly DENIES the conclusion the old text implied — an
// agent that reads "primary branch" here goes hunting for a branch problem it does not
// have, which is half the round trips pg2-h2npt was filed for. `deny` picks the wording
// for the auto-approving Reject over the interactive Ask.
func (f commitFinding) unresolvedReason(deny bool) string {
	s := "primary-commit: cannot determine which repository or branch this commit lands in — " + f.source +
		" is not a literal path (`" + f.token + "` is expanded by the shell, after this hook has already decided). " +
		"This is NOT a finding that you are on a primary branch: the target is simply unknown, and guessing it would mean either denying a legitimate worktree commit or approving a commit on shared primary. " +
		"Re-run naming the directory literally: `git -C /abs/path/to/worktree commit …` (a `cd`/`pushd` target must be literal too)."
	if deny {
		return s + " Denied rather than asked because this session auto-accepts prompts."
	}
	return s
}

// inspectCommit classifies a single parsed git invocation: findingPrimary for a
// `git commit` on the canonical clone's primary branch, findingUnresolved for one whose
// target directory does not resolve to a literal path, findingNone otherwise. When
// expandAliases is true it first expands a git alias hiding the subcommand (tc-2phi8):
// a normal alias is expanded once and re-checked; a SHELL alias (`!…`) has its body
// re-parsed and each git command in it checked with expansion OFF (single-pass, which
// also bounds recursion). A resolver error, a linked worktree, or being off primary all
// yield findingNone — the fail-open posture the worktree discipline relies on.
func (r *Rule) inspectCommit(pc cmdparse.ParsedCommand, cwd string, expandAliases bool) commitFinding {
	chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
	dir := effectiveDir(cwd, chdirs)
	if expandAliases {
		effSubcmd, _, shellBody := ResolveGitAlias(subcmd, rest, r.mergedAliases(dir, pc.Args))
		if shellBody != "" {
			for _, sub := range cmdparse.Parse(shellBody) {
				if !isGit(sub.Executable) {
					continue
				}
				// dir is passed as the recursion's cwd, so an unresolved OUTER `-C` is
				// still visible to the inner unresolvedDir check below.
				if f := r.inspectCommit(sub, dir, false); f.kind != findingNone {
					return f
				}
			}
			return commitFinding{}
		}
		subcmd = effSubcmd
	}
	if subcmd != "commit" {
		return commitFinding{}
	}
	// The subcommand IS a commit, so from here the DIRECTORY decides the verdict — and
	// an unresolved one decides nothing. Tested HERE, after the subcommand and before
	// the resolver, for two reasons: `git -C $WT status` must stay untouched (this rule
	// only ever governs a commit), and the resolver's walk-up is precisely what turns an
	// unresolvable path into a confident wrong answer, so it must not run on one.
	if token, source := unresolvedDir(cwd, chdirs); token != "" {
		return commitFinding{kind: findingUnresolved, token: token, source: source}
	}
	canonical, err := r.resolver.IsCanonical(dir)
	if err != nil || !canonical {
		return commitFinding{}
	}
	primary, err := r.resolver.PrimaryBranch(dir)
	if err != nil || primary == "" {
		return commitFinding{}
	}
	cur, err := r.resolver.CurrentBranch(dir)
	if err != nil || cur == "" || cur != primary {
		return commitFinding{}
	}
	return commitFinding{kind: findingPrimary, primary: primary, dir: dir, chosen: dirProvenance(chdirs)}
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
