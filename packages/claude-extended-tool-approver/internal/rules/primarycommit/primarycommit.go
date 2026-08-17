// Package primarycommit gates a `git commit` on the PRIMARY branch of the CANONICAL
// clone (the main working tree — the real .git directory, never a linked worktree).
//
// A commit on primary gets a DECISIVE verdict (Reject or Ask, never a deferring
// NotApplicable) in GatedModes — sessions where nobody is necessarily watching each
// command as it runs, so R-6's "a human is directing this" trust does not apply.
// WITHIN that set, which verdict is a MEASURED, per-mode property, not an inference
// from the mode's name:
//
//   - bypassPermissions silently accepts an Ask — measured empirically (pg2-2t9wz) —
//     so it MUST be hard-denied (Reject); an Ask there is never actually seen.
//   - auto PROMPTS on an Ask instead of silently accepting it — operator-confirmed
//     2026-08-14 and 2026-08-15 ("auto, acceptEdits both will ask if ceta returns
//     ASK") — so it gets Ask, not Reject: hard-denying there would only add friction a
//     human is already going to see. `auto` was WRONGLY hard-denied before this
//     package's correction, from an incorrect inference that bypass's behavior
//     generalizes to every auto-accepting-sounding mode name; it does not. It is
//     still in GatedModes, though — an unattended `auto` session gets no more trust
//     than one is measured to deserve, so a primary commit there must still surface a
//     decisive verdict rather than being silently approved by the git rule behind
//     this one, or silently accepted as the empty NoOpinion an auto-accepting session
//     also treats as approval.
//   - dontAsk is UNMEASURED — it is not observed as a real Claude Code permission mode
//     in this machine's asklog (~352k rows) or in any settings.json here, and looks
//     like a guessed name rather than a live one. It stays hard-denied fail-closed
//     pending real evidence: removing it on the ABSENCE of a measurement would be
//     relaxing a security rule on a guess, which is the opposite of what the auto
//     correction above did (that correction rests on a measurement, not an absence).
//
// Interactive modes (default/plan/acceptEdits/empty) — i.e. everything NOT in
// GatedModes — get Abstain instead: no every-commit prompt, because a human directing
// a primary commit is permitted (R-6) and left to the normal flow. acceptEdits reaches
// this bucket for a second, independent reason too: it auto-accepts Edits, not Bash, so
// a Bash primary commit there still goes through Claude Code's ordinary per-command
// prompt exactly as default does — unlike auto, whose own unattended nature is what
// keeps it in GatedModes. Worktrees, feature branches, non-commit git, and any resolver
// error all Abstain regardless of mode (fail-open; the worktree discipline is the
// primary control).
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
// not resolve to a literal path — `git -C $WT commit`, `cd $WT && git commit`, where
// nothing in the command says what `$WT` is. There the rule has not established that the
// commit is on primary; it has established that it CANNOT TELL, and the fail-open Abstain
// above is unavailable because the generic git rule behind it approves a plain
// `git commit`. So an unresolved target gets a fail-safe verdict of its own — Ask
// interactively, Reject in an auto-approving session where an Ask would be silently
// accepted — and NEVER reaches Approve. This is the same "identity check I could not
// complete" carve-out ADR 0043's error policy names for killshell.
//
// A target the COMMAND ITSELF establishes is not that case: in
// `WT=/abs/worktree && git -C "$WT" commit` the value is written down in the command
// text, so the rule resolves it and judges the real directory (pg2-wq3ki). The
// rationale, the resolution model, the DECLINED `$(…)` derivation and the coupling to
// the engine's `cd` handling are all in dirresolve.go's DIRECTORY RESOLUTION comment.
package primarycommit

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// GatedModes is the set of permission modes in which a commit on the canonical primary
// MUST get a decisive verdict (Reject or Ask) rather than the R-6 trust that lets it
// defer (NotApplicable) to the generic git rule — deferring there can reach Approve,
// and for an unresolved target it can reach the empty NoOpinion verdict, which these
// modes' own auto-accepting behavior treats just like an Approve. See the package doc
// comment for why interactive modes (default/plan/acceptEdits/empty) are NOT in this
// set.
//
// AutoApprovingModes (below) is the STRICT SUBSET of GatedModes whose sessions
// additionally silently accept an Ask — the measured, per-mode basis is in the package
// doc comment. A GatedModes member outside that subset (currently just "auto") gets
// Ask instead of Reject: still decisive, but not hard-denied, because an Ask actually
// reaches a human there.
//
// Both are exported so primarypush can share one pair of definitions instead of a
// second, independently-maintained copy (the two rules encode the same R-6/R-8 posture
// and must not drift apart on WHICH modes are in either set).
var GatedModes = map[string]bool{
	"bypassPermissions": true,
	"auto":              true,
	"dontAsk":           true,
}

var AutoApprovingModes = map[string]bool{
	"bypassPermissions": true,
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
	leaves := cmdparse.Parse(cmdStr)
	for i, pc := range leaves {
		if !isGit(pc.Executable) {
			continue
		}
		// No resolver injected: the rule has no way to know what the primary is, so
		// it does not govern this input. This is a CONSTRUCTION condition, not a
		// runtime failure, so it is not-applicable rather than an error.
		if r.resolver == nil {
			return hookio.NotApplicable()
		}
		// The variables this command establishes for itself (pg2-wq3ki). Under the
		// engine they arrive on the input, already computed from the EARLIER leaves of
		// the expression; a direct caller is handed the whole expression, and LeafVars
		// reads them off the leaves before this one. Empty in the ordinary case, and an
		// empty environment resolves nothing — every verdict is then the one this rule
		// reached before the environment existed.
		f := r.inspectCommit(pc, input.CWD, LeafVars(input.InCommandVars, leaves, i), true)
		silentlyAccepts := AutoApprovingModes[input.PermissionMode]
		switch f.kind {
		case findingUnresolved:
			// FAIL-SAFE, and the ONLY branch of this rule that is decisive in an
			// interactive session. The rule has NOT found a commit on primary — it has
			// found that it cannot tell where the commit lands, which is the one state
			// the fail-open Abstain cannot express: behind this rule the generic git
			// rule approves a plain `git commit`, so returning ErrNotApplicable here
			// would let an unresolvable target reach Approve. Ask keeps the verdict
			// non-approving and puts the diagnosis in front of the agent; the modes that
			// silently accept an Ask get Reject instead — the old behaviour there was
			// already a (wrongly-reasoned) Reject for "auto" too, but no spelling may
			// come out of this change more permissive than it went in, so `auto` moving
			// from that Reject to this branch's Ask (below) is the intended correction,
			// not a regression.
			if silentlyAccepts {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: f.unresolvedReason(true), Module: r.Name()}, nil
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: f.unresolvedReason(false), Module: r.Name()}, nil
		case findingPrimary:
			// Commit on canonical primary. Interactive/default sessions are trusted
			// (R-6) and get NotApplicable, letting the git rule behind this one judge
			// the command on its own merits — that MAY reach Approve, which is fine
			// there because a human is directing it. A GatedModes session gets a
			// decisive verdict instead, because NEITHER Approve NOR the empty NoOpinion
			// a deferral could produce is safe when nobody is necessarily watching: a
			// silently-accepting mode gets the hard Reject; the rest of GatedModes
			// (currently just "auto") gets Ask, which is enough because an Ask actually
			// reaches a human there.
			if !GatedModes[input.PermissionMode] {
				return hookio.NotApplicable()
			}
			if silentlyAccepts {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: f.primaryReason(true), Module: r.Name()}, nil
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: f.primaryReason(false), Module: r.Name()}, nil
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
// this a primary commit on the canonical clone, and what to do instead. `deny` picks
// the wording for the hard-denying Reject (a silently-accepting mode) over the Ask a
// GatedModes member that still prompts gets instead (currently just "auto") — see
// AutoApprovingModes' doc comment for which is which.
//
// The R-6 citation is kept but DEMOTED to the last clause. Naming the rule was all the
// old text did, so an agent whose real problem was a mis-resolved directory was told it
// was on a primary branch — and had no way to discover which directory the rule had
// actually looked at (pg2-h2npt).
func (f commitFinding) primaryReason(deny bool) string {
	s := "primary-commit: refusing this commit. Directory evaluated: " + f.dir +
		" (chosen from " + f.chosen + "). That directory is the CANONICAL clone — its .git is a real directory, not a linked worktree — and its HEAD is on \"" +
		f.primary + "\", the primary branch. Advancing shared primary needs explicit human direction (R-6), and this session is not one where a human is necessarily watching each command. " +
		"If you meant to commit inside a worktree, name it with a LITERAL absolute path: `git -C /abs/worktree commit …` or `cd /abs/worktree && git commit …`."
	if deny {
		return s + " Denied rather than asked because this session auto-accepts prompts."
	}
	return s
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
func (r *Rule) inspectCommit(pc cmdparse.ParsedCommand, cwd string, vars map[string]string, expandAliases bool) commitFinding {
	chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
	res := ResolveDir(cwd, chdirs, vars)
	dir := res.Dir
	if expandAliases {
		effSubcmd, _, shellBody := ResolveGitAlias(subcmd, rest, r.mergedAliases(dir, pc.Args))
		if shellBody != "" {
			for _, sub := range cmdparse.Parse(shellBody) {
				if !isGit(sub.Executable) {
					continue
				}
				// dir is passed as the recursion's cwd, so an unresolved OUTER `-C` is
				// still visible to the inner resolution below. The environment is
				// threaded too: an alias body is part of the SAME command, so a
				// variable the command established is in scope inside it.
				if f := r.inspectCommit(sub, dir, vars, false); f.kind != findingNone {
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
	if res.Unresolved() {
		return commitFinding{kind: findingUnresolved, token: res.Token, source: res.Source}
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
	return commitFinding{kind: findingPrimary, primary: primary, dir: dir, chosen: res.Chosen}
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
