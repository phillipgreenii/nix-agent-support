// Package primarypush gates a `git push` that would ADVANCE the PRIMARY branch of
// the CANONICAL clone (the main working tree — the real .git directory, never a
// linked worktree) on the shared remote. It is the push-side sibling of the
// primary-commit rule and reuses primary-commit's PrimaryResolver so the two rules
// share canonical/primary/current-branch detection.
//
// A push that advances primary gets a DECISIVE verdict (Reject or Ask, never a
// deferring NotApplicable) in primarycommit.GatedModes — sessions where nobody is
// necessarily watching each command, so R-6's "a human is directing this" trust does
// not apply. WITHIN that set, WHICH verdict is a MEASURED, per-mode property (see
// primarycommit's package doc comment for the full basis): bypassPermissions silently
// accepts an Ask (pg2-2t9wz) and so is hard-denied (primarycommit.AutoApprovingModes);
// auto PROMPTS on one instead (operator-confirmed 2026-08-14/2026-08-15) and so gets
// Ask, not Reject — hard-denying there would only add friction a human is already
// going to see, but it stays in GatedModes because an unattended `auto` session earns
// no more trust than that; dontAsk is unmeasured and kept hard-denied fail-closed
// pending evidence. Interactive modes (default/plan/acceptEdits/empty) — everything NOT
// in GatedModes — get Abstain instead: no every-push prompt, a human directing a push
// to primary is permitted (R-6) and left to the normal flow. The ONE exception, and the
// only verdict this rule ever puts in front of a human from OUTSIDE GatedModes, is the
// unresolved-directory branch described below (decisive in every mode, GatedModes or
// not).
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
// The one case that is DECISIVE IN EVERY MODE is a push whose target directory does not
// resolve to a literal path — `git -C $WT push`, `cd $WT && git push`, where nothing in
// the command says what `$WT` is. There the rule has not established that the push
// advances primary; it has established that it CANNOT TELL. So an unresolved target gets
// a fail-safe verdict of its own — Ask interactively, Reject in an auto-approving session
// where an Ask would be silently accepted — and NEVER reaches Approve. This mirrors
// primary-commit exactly (pg2-h2npt), and the resolution model, its three directory
// sources and its reconciliation with the fail-open posture above are documented on
// inspectPush and in primarycommit's dirresolve.go DIRECTORY RESOLUTION comment. This
// rule holds NO directory-resolution code of its own: it consumes primarycommit's
// exported ResolveDir/LeafVars seam, so the two rules cannot drift (pg2-eqacu).
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
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/primarycommit"
)

// pushValueFlags are `git push` options that CONSUME the following token as their
// value, so that token must not be mistaken for a refspec (e.g. `--push-option main`).
var pushValueFlags = map[string]bool{
	"-o": true, "--push-option": true, "--repo": true,
	"--receive-pack": true, "--exec": true,
}

type Rule struct{ resolver primarycommit.PrimaryResolver }

func New(resolver primarycommit.PrimaryResolver) *Rule { return &Rule{resolver: resolver} }

func (r *Rule) Name() string { return "primary-push" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	// As in primary-commit: every former `return abstain` meant "keep going", and
	// this rule MUST precede the generic git rule without shadowing it, so all of
	// them are ErrNotApplicable and none may become a terminal NoOpinion.
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	leaves, err := cmdparse.LeavesOf(input)
	if err != nil {
		// Genuine failure: the tool IS Bash, so this rule governs the input.
		return hookio.RuleResult{}, fmt.Errorf("primary-push: read bash command: %w", err)
	}
	for i, pc := range leaves {
		if !isGit(pc.Executable) {
			continue
		}
		// No resolver injected: a construction condition, not a runtime failure.
		if r.resolver == nil {
			return hookio.NotApplicable()
		}
		// The variables this command establishes for itself (pg2-wq3ki), read through
		// the same seam primary-commit uses. Under the engine they arrive on the input,
		// already computed from the EARLIER leaves; a direct caller is handed the whole
		// expression and LeafVars reads them off the leaves before this one. Empty in the
		// ordinary case, and an empty environment resolves nothing — so every verdict is
		// then the one this rule reached before the environment existed.
		f := r.inspectPush(pc, input.CWD, primarycommit.LeafVars(input.InCommandVars, leaves, i), true)
		silentlyAccepts := primarycommit.AutoApprovingModes[input.PermissionMode]
		switch f.kind {
		case findingUnresolved:
			// FAIL-SAFE, and the ONLY branch of this rule that is decisive in an
			// interactive session. See inspectPush for why this does not contradict the
			// fail-open posture the rest of the rule keeps.
			if silentlyAccepts {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: f.unresolvedReason(true), Module: r.Name()}, nil
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: f.unresolvedReason(false), Module: r.Name()}, nil
		case findingPrimary:
			// Push advances the canonical primary. Interactive/default sessions are
			// trusted (R-6) and get NotApplicable, letting the git rule after us judge
			// the command on its own merits — that MAY reach Approve, which is fine
			// there because a human is directing it. A GatedModes session (nobody is
			// necessarily watching) gets a decisive verdict instead: a silently-accepting
			// mode gets the hard Reject; the rest of GatedModes (currently just "auto")
			// gets Ask, which is enough because an Ask actually reaches a human there.
			if !primarycommit.GatedModes[input.PermissionMode] {
				return hookio.NotApplicable()
			}
			if silentlyAccepts {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: f.primaryReason(true), Module: r.Name()}, nil
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: f.primaryReason(false), Module: r.Name()}, nil
		}
	}
	// No git leaf, or none that advances primary.
	return hookio.NotApplicable()
}

// findingKind classifies what the rule established about one parsed git invocation.
// The third member is the pg2-eqacu fix: "this push advances the canonical primary" and
// "I could not work out which repository this push would even reach" are different facts
// with different remedies, and folding the second into the first is the false REJECT
// pg2-eqacu reports — an auto-approving `git -C $WT push` was denied for "advancing
// primary" when the push was really headed for a nested worktree on a feature branch.
type findingKind int

const (
	findingNone       findingKind = iota // not a push, not canonical, or a feature-branch push
	findingPrimary                       // a push that advances the canonical clone's primary
	findingUnresolved                    // a push whose target directory is not statically resolvable
)

// pushFinding carries the finding plus the evidence its reason text cites. The evidence
// fields are populated per kind: primary/dir/chosen for findingPrimary, token/source for
// findingUnresolved — the same split primary-commit's commitFinding uses, because the two
// reasons answer different questions (see unresolvedReason).
type pushFinding struct {
	kind    findingKind
	primary string // the primary branch the push would advance
	dir     string // the directory that was evaluated
	chosen  string // how that directory was chosen (primarycommit.DirResolution.Chosen)
	token   string // the text that defeated static resolution, as written
	source  string // which input that text came from (primarycommit.DirResolution.Source)
}

// primaryReason states WHICH directory was evaluated, HOW it was chosen, WHY that made
// this a primary-advancing push on the canonical clone, and what to do instead — the
// wording convention pg2-h2npt introduced for primary-commit. `deny` picks the wording
// for the hard-denying Reject (a silently-accepting mode) over the Ask a GatedModes
// member that still prompts gets instead (currently just "auto") — see
// primarycommit.AutoApprovingModes' doc comment for which is which.
//
// The old text named only the primary branch and R-6/R-8. That was all the diagnosis an
// agent got, so one whose real problem was a mis-resolved directory was told it was
// advancing a branch it had never targeted, with no way to discover which directory the
// rule had actually looked at (pg2-eqacu).
func (f pushFinding) primaryReason(deny bool) string {
	s := "primary-push: refusing this push. Directory evaluated: " + f.dir +
		" (chosen from " + f.chosen + "). That directory is the CANONICAL clone — its .git is a real directory, not a linked worktree — and the push would advance \"" +
		f.primary + "\", its primary branch, on the shared remote. Advancing shared primary requires explicit human direction / PR flow (R-6/R-8), and this session is not one where a human is necessarily watching each command. " +
		"Push a feature branch instead. If you meant to push from inside a worktree, name it with a LITERAL absolute path: `git -C /abs/worktree push …` or `cd /abs/worktree && git push …`."
	if deny {
		return s + " Denied rather than asked because this session auto-accepts prompts."
	}
	return s
}

// unresolvedReason states that the rule COULD NOT DETERMINE the target, names the text
// that defeated it, and explicitly DENIES the conclusion the old text implied — an agent
// that reads "advances the primary branch" here goes hunting for a branch problem it does
// not have, which is the round trip pg2-eqacu was filed for. `deny` picks the wording for
// the auto-approving Reject over the interactive Ask.
//
// It deliberately does NOT name a directory. DirResolution.Dir is populated even when the
// resolution failed, but it is the best-effort value — for `git -C $WT push` from the
// canonical clone it is `<canonical>/$WT`, which resolves UP to the canonical clone — so
// printing it would hand the agent the very fabricated provenance this branch exists to
// refuse. The TOKEN and its SOURCE are what the agent has to fix, so those are what it
// gets, exactly as primary-commit does it.
func (f pushFinding) unresolvedReason(deny bool) string {
	s := "primary-push: cannot determine which repository or branch this push would advance — " + f.source +
		" is not a literal path (`" + f.token + "` is expanded by the shell, after this hook has already decided). " +
		"This is NOT a finding that the push targets a primary branch: the target repository is simply unknown, and guessing it would mean either denying a legitimate feature-branch push from a worktree or approving a push that advances shared primary. " +
		"Re-run naming the directory literally: `git -C /abs/path/to/worktree push …` (a `cd`/`pushd` target must be literal too), or assign it in the SAME command: `WT=/abs/path/to/worktree && git -C \"$WT\" push …`."
	if deny {
		return s + " Denied rather than asked because this session auto-accepts prompts."
	}
	return s
}

// inspectPush classifies a single parsed git invocation: findingPrimary for a `git push`
// that would advance the canonical clone's primary branch on the shared remote,
// findingUnresolved for one whose target directory does not resolve to a literal path,
// findingNone otherwise. When expandAliases is true it first expands a git alias hiding
// the subcommand (tc-2phi8): a normal alias is expanded once and re-checked; a SHELL alias
// (`!…`) has its body re-parsed and each git command in it checked with expansion OFF
// (single-pass, which also bounds recursion).
//
// THE DIRECTORY COMES FROM primarycommit.ResolveDir AND FROM NOWHERE ELSE (pg2-eqacu).
// This rule used to carry a private effectiveDir that read `git -C` and the cwd only —
// it never consulted the in-command variable environment, and nothing told it when the
// text it was joining could not be a path. That is DELETED, not wrapped: ResolveDir is
// the seam primarycommit exports for exactly this (see its doc comment), so the three
// directory sources — the session cwd, a `cd`/`pushd` the ENGINE already advanced the cwd
// past (EvaluateExpression, pg2-opclh; this rule MUST NOT grow a `cd` model of its own),
// and the `git -C` options with in-command expansion — are evaluated by ONE
// implementation that the two rules share and cannot drift apart from.
//
// FAIL-OPEN AND FAIL-SAFE, RECONCILED. These are not two postures in tension; they answer
// two different questions, and which one applies is decided by WHETHER THE RULE HAS AN
// ANSWER AT ALL:
//
//   - A resolver error, a linked worktree, a non-push subcommand, or a feature-branch
//     push all yield findingNone and stay FAIL-OPEN — unchanged by pg2-eqacu. There the
//     rule has a real answer about a KNOWN directory ("this is not a push that advances
//     the canonical primary"), and R-2/R-8 — the worktree discipline — is the primary
//     control, so declining to gate is correct rather than merely tolerable.
//   - An UNRESOLVED directory is different in kind, and fail-open is not available to it
//     for a mechanical reason: the best-effort directory is SYSTEMATICALLY BIASED. The
//     resolver's gitRoot walks UP from `<cwd>/$WT`, so in the layout this rule protects
//     (a nested worktree under a canonical clone that is on its primary branch) it lands
//     on the canonical clone and reads `cur == primary`. Answering from that value is not
//     a permissive abstention, it is a CONFIDENT WRONG ANSWER — the false Reject of a
//     legitimate feature-branch push that pg2-eqacu reports. Refusing to answer is
//     therefore the only honest option, and the refusal MUST NOT be spelled
//     ErrNotApplicable: the generic git rule behind this one approves a non-force push,
//     so not-applicable would turn an unknowable target into an APPROVAL. Hence Ask, and
//     Reject where an Ask is silently accepted — which is also strictly no more permissive
//     than the Reject those modes already produced for this spelling.
//
// The interactive Ask is the one verdict this change ADDS, and it is deliberately narrow:
// it fires only for a push whose directory cannot be read out of the command text, never
// for the every-push prompt the package comment rules out. A directory the command itself
// writes down (`WT=/abs/wt && git -C "$WT" push`) RESOLVES and costs nothing — before this
// change that spelling was hard-denied in an auto-approving session while the literal
// `git -C /abs/wt push` beside it was approved.
//
// DECLINED: narrowing the unresolved verdict by reading the refspec, so that
// `git -C $WT push origin feat:feat` could stay fail-open on the grounds that its remote
// side is not the primary branch. It cannot: "is this the primary branch" is answered by
// PrimaryBranch of the TARGET repository (`.git/config`'s pgii-integrate-branch.primaryBranch,
// else "main"), and that repository is precisely what is unknown here. Deciding it from the
// biased directory, or from a hardcoded "main", would reintroduce the guess this branch
// exists to refuse.
func (r *Rule) inspectPush(pc cmdparse.ParsedCommand, cwd string, vars map[string]string, expandAliases bool) pushFinding {
	chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
	res := primarycommit.ResolveDir(cwd, chdirs, vars)
	dir := res.Dir
	if expandAliases {
		effSubcmd, effRest, shellBody := primarycommit.ResolveGitAlias(subcmd, rest, r.mergedAliases(dir, pc.Args))
		if shellBody != "" {
			for _, sub := range cmdparse.Parse(shellBody) {
				if !isGit(sub.Executable) {
					continue
				}
				// dir is passed as the recursion's cwd, so an unresolved OUTER `-C` is
				// still visible to the inner resolution below (its token survives in the
				// best-effort Dir). The environment is threaded too: an alias body is part
				// of the SAME command, so a variable the command established is in scope.
				if f := r.inspectPush(sub, dir, vars, false); f.kind != findingNone {
					return f
				}
			}
			return pushFinding{}
		}
		subcmd, rest = effSubcmd, effRest
	}
	if subcmd != "push" {
		return pushFinding{}
	}
	// The subcommand IS a push, so from here the DIRECTORY decides the verdict — and an
	// unresolved one decides nothing. Tested HERE, after the subcommand and before the
	// resolver, for two reasons: `git -C $WT status` must stay untouched (this rule only
	// ever governs a push), and the resolver's walk-up is precisely what turns an
	// unresolvable path into a confident wrong answer, so it must not run on one.
	if res.Unresolved() {
		return pushFinding{kind: findingUnresolved, token: res.Token, source: res.Source}
	}
	canonical, err := r.resolver.IsCanonical(dir)
	if err != nil || !canonical {
		return pushFinding{}
	}
	primary, err := r.resolver.PrimaryBranch(dir)
	if err != nil || primary == "" {
		return pushFinding{}
	}
	cur, err := r.resolver.CurrentBranch(dir)
	if err != nil {
		return pushFinding{}
	}
	// `-c push.default=matching` is consumed by GitInvocation before `rest`, so detect
	// the injected form from the full arg list; the AMBIENT form (set in ~/.gitconfig,
	// not injected) is read from the resolver. Either makes a bare push advance primary.
	matching := hasMatchingPushDefault(pc.Args) || r.pushDefaultIsMatching(dir)
	if !pushTargetsPrimary(rest, primary, cur, matching) {
		return pushFinding{}
	}
	return pushFinding{kind: findingPrimary, primary: primary, dir: dir, chosen: res.Chosen}
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

// NOTE (pg2-eqacu): the private effectiveDir that used to live here is GONE, and nothing
// in this package may reintroduce one. It is primarycommit.ResolveDir's job — see
// inspectPush. A local copy is what let this rule fall behind pg2-h2npt in the first place.
