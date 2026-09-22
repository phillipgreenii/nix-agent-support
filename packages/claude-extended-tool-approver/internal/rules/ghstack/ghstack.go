// Package ghstack approves/gates the `gh-stack` GitHub CLI extension's command family —
// invoked as `gh stack <verb> ...` — per an explicit per-subcommand design ruled 2026-09-22
// (bead pg2-s4lzw).
//
// # Why this exists
//
// A fresh conformance enumeration (pg2-z3u4f, using plugin-conformance-check) found that
// phillipg-nix-ziprecruiter's gh-stack plugin's ENTIRE prescribed command surface — view,
// up/down/top/bottom/trunk, checkout, init, add, push, submit, link, sync, rebase, merge,
// unstack: ~20+ distinct invocations from the plugin's SKILL.md, its CORE prescribed
// workflow, not an edge case — abstained through CETA's whole rule chain. Even the existing
// `gh` rule module (internal/rules/gh) doesn't claim any of these: `gh stack` is a
// third-party extension, not one of gh's own built-in resources (pr/issue/run/...), so
// gh.go's resource/subcmd switch never matches "stack" at all.
//
// # Source of the classification
//
// Every verdict below was checked against the vendored, modified SKILL.md at
// phillipg-nix-ziprecruiter's claude-marketplace/gh-stack/skills/gh-stack/SKILL.md (MIT,
// vendor+modify of github/gh-stack, upstream metadata.version 0.0.9) — see each verdict
// function's own doc for the specific citation.
//
// # The classification, by bucket
//
// Approve, unconditionally — read-only or local-only, no PR/GitHub-API side effect:
//
//	view (with or without --json/--short), up [n], down [n], top, bottom, trunk,
//	checkout <local-branch-name> (NOT a stack/PR number or URL — see checkoutVerdict),
//	init, add <branch> bare (no -A/-u/-m — see addBundlesCommit), unstack --local,
//	rebase (mirrors plain `git rebase` — see rebaseVerdict for why this is NOT bucketed
//	with the other Ask verdicts below despite an earlier design draft doing so)
//
// Deliberately left UNCLAIMED (falls through to NotApplicable, not a NoOpinion terminal
// verdict) — the chain may still be answered by a later rule, or fall to chain exhaustion:
//
//	add -A/-u/-m <msg> <branch> (any form bundling an UNINSPECTED commit — CETA cannot see
//	  what is being committed, mirroring why a plain `git commit` is not blanket-approved),
//	modify (the SKILL.md's own exit-code table, code 10, states this skill NEVER produces
//	  it — there is nothing to classify), and any other/unknown gh-stack subcommand (a
//	  future addition, or a typo)
//
// Ask — a real remote mutation, or an uncertainty this pass deliberately declines to
// resolve into Approve or Reject (see each verdict's doc for the specific open question):
//
//	checkout <stack-number|pr-number|pr-url>, unstack / unstack <number> (no --local),
//	push, submit --auto (with or without --open), link, sync
//
// Reject, unconditionally, every flag/positional spelling:
//
//	merge
package ghstack

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/gh"
)

type Rule struct{}

// New constructs the gh-stack rule. It takes no configuration: like pnworkspace and
// killprobe, the classification is fixed by the vendored SKILL.md and applies uniformly
// to every consumer, with no per-consumer data to inject.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "gh-stack" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("gh-stack: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if !isGhExecutable(pc.Executable) {
			continue
		}
		// gh.CommandPath is the SAME cobra-aware word-finder gh.go uses for its own
		// resource/subcommand resolution (pg2-by1ij): gh registers each installed
		// extension as a real command at its cobra root, so a global flag preceding or
		// sitting inside the path (`gh --repo o/r stack view`, `gh stack --remote x
		// push`) has to be skipped exactly the way it is for a built-in resource. See
		// gh.CommandPath's doc for why this is reused rather than re-measured here.
		//
		// resource is checked by EXACT token equality, never a substring — the vendored
		// SKILL.md's own scope-gate section calls this out explicitly: "a substring
		// match on `stack` would also accept `gh stackoverflow` or `gh unstack`".
		resource, subcmd, _, rest := gh.CommandPath(pc.Args)
		if resource != "stack" {
			continue
		}
		switch subcmd {
		case "view", "up", "down", "top", "bottom", "trunk":
			return r.approve(subcmd + ": read-only or navigation-only stack command, no side effects (SKILL.md 'Navigate the stack' / 'View the stack')")
		case "init":
			return r.approve("init: creates/adopts LOCAL branches from the trunk and checks one out; no PR or GitHub-API side effect (SKILL.md 'Initialize a stack')")
		case "checkout":
			return r.checkoutVerdict(rest)
		case "add":
			if addBundlesCommit(rest) {
				// Deliberately UNCLAIMED, not a NoOpinion: CETA cannot see what is
				// being committed by -A/-u/-m, the same reason a plain `git commit`
				// is not blanket-approved. Continue scanning parsed (in case a
				// compound command has another leaf this rule DOES want to claim),
				// and fall to chain exhaustion otherwise.
				continue
			}
			return r.approve("add <branch> (bare, no -A/-u/-m): creates a new LOCAL branch on top of the stack; no commit is made and no PR is touched (SKILL.md 'Add a branch')")
		case "unstack":
			return r.unstackVerdict(rest)
		case "push":
			return r.ask("push: a batched, non-atomic multi-ref push across the WHOLE stack, each branch checked with its own --force-with-lease (SKILL.md 'Push branches to remote'). This is NOT loosened to Approve this pass even though a single-branch, same-branch --force-with-lease is already Approved in internal/rules/git ('the post-rebase idiom') — gh-stack's push is batched/non-atomic across the whole stack, a meaningfully larger blast radius. That potential loosening is left for a future pass with its own operator record.")
		case "submit":
			return r.submitVerdict(rest)
		case "link":
			return r.ask("link: creates new PRs and links them into a stack on GitHub (SKILL.md 'Link branches as a stack'). The vendored SKILL.md documents no draft/ready-for-review DEFAULT for a link with no --open, so this defaults to Ask rather than guessing Approve or Reject — the real extension's actual default needs verification before this could move either direction. Record any future verification here before changing this verdict.")
		case "sync":
			return r.ask("sync: bundles fetch + cascade-rebase + push + PR-state-sync + optional stack-sync + optional prune in ONE command (SKILL.md 'Sync the stack'), with no per-step flag to soften any part of it — the riskiest 'convenience' command in the family.")
		case "rebase":
			return r.rebaseVerdict()
		case "merge":
			return r.mergeVerdict()
		default:
			// "modify" (never produced by this skill, per its own exit-code table) or
			// any other/unknown gh-stack subcommand (a future addition, or a typo):
			// leave unclaimed rather than guess.
			continue
		}
	}
	return hookio.NotApplicable()
}

func (r *Rule) approve(what string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "gh stack " + what, Module: r.Name()}, nil
}

func (r *Rule) ask(why string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Ask, Reason: "gh stack " + why, Module: r.Name()}, nil
}

func (r *Rule) reject(why string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Reject, Reason: "gh stack " + why, Module: r.Name()}, nil
}

func isGhExecutable(exec string) bool {
	return exec == "gh" || filepath.Base(exec) == "gh"
}

// checkoutVerdict returns the verdict for `gh stack checkout <arg>` — rest being the
// tokens after the "checkout" subcommand word.
//
// THE LOCAL/REMOTE SPLIT (SKILL.md "Check out a stack" and "Known limitations" #4). A
// bare branch NAME resolves against LOCALLY TRACKED stacks only — no network — and the
// SKILL.md states plainly "this is always safe for non-interactive use". A stack number,
// PR number, or PR URL instead FETCHES from GitHub: "the command fetches the stack on
// GitHub, pulls the branches, and sets up the stack locally" — a real network operation
// this rule does not clear. Checkout takes NO flags of its own in the vendored SKILL.md
// (unlike push/submit/sync/rebase/link, it has no --remote and relies on
// remote.pushDefault), so rest holds only the one positional argument, if any.
//
// A bare number is resolved by gh-stack as a STACK number first, then a PR number, per
// SKILL.md's "Check out a stack" section — either resolution is network-mediated, so any
// all-digits argument is treated the same as a PR number/URL here: Ask, not Approve.
//
// No positional argument at all (`gh stack checkout` bare) triggers an interactive
// selection menu per the SKILL.md's own "Never do" list ("running `gh stack checkout`
// without arguments triggers an interactive selection menu") — not the safe
// branch-name-only form this rule clears, so it also gets Ask rather than a guess.
func (r *Rule) checkoutVerdict(rest []string) (hookio.RuleResult, error) {
	arg, ok := firstPositional(rest)
	if !ok {
		return r.ask("checkout with no argument triggers an interactive selection menu (SKILL.md's own 'Never do' list); this is not the safe local-branch-name form, so it cannot be cleared here")
	}
	if isAllDigits(arg) {
		return r.ask("checkout " + arg + ": a bare number resolves as a STACK number first, then a PR number (SKILL.md 'Check out a stack') — either is a real network fetch from GitHub, so only the local-branch-name form is auto-approved")
	}
	if looksLikeURL(arg) {
		return r.ask("checkout " + arg + ": a PR URL is a real network fetch from GitHub (SKILL.md 'Check out a stack'), so only the local-branch-name form is auto-approved")
	}
	return r.approve("checkout " + arg + ": a branch name resolves against LOCALLY TRACKED stacks only — no network (SKILL.md: 'this is always safe for non-interactive use')")
}

// firstPositional returns the first non-flag token in args, skipping any leading
// dash-prefixed token defensively (checkout documents NO flags of its own, so this is a
// belt-and-suspenders skip, not a modeled arity table) and honoring a `--`
// end-of-options terminator the way gh's own cobra parsing does.
func firstPositional(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
		if a != "-" && strings.HasPrefix(a, "-") {
			continue
		}
		return a, true
	}
	return "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// addBundlesCommit reports whether a `gh stack add` invocation carries any of -A/--all,
// -u/--update, or -m/--message — the flags SKILL.md's "Add a branch" section documents as
// bundling a `git add` + `git commit` into the branch-creation call itself
// (`gh stack add -Am "message" api-routes`, `gh stack add -um "Fix auth bug" auth-fix`).
// These are the ONLY flags `add` documents, so any dash-prefixed token here is composed
// solely of these three letters (in long, short, or clustered-short form,
// e.g. `-Am`/`-um`) — a defensive per-byte scan is therefore sufficient without a full
// arity table the way gh.go/pr.go need for `gh pr`'s much larger flag surface.
func addBundlesCommit(rest []string) bool {
	for _, a := range rest {
		if a == "--" {
			break
		}
		switch a {
		case "-A", "--all", "-u", "--update", "-m", "--message":
			return true
		}
		if strings.HasPrefix(a, "--message=") {
			return true
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'A', 'u', 'm':
					return true
				}
			}
		}
	}
	return false
}

// unstackVerdict returns the verdict for `gh stack unstack [<number>]` — rest being the
// tokens after the "unstack" subcommand word.
//
// `--local` is the ONLY documented flag (SKILL.md "Remove a stack"): "Only remove the
// stack locally (keep it on GitHub); never contacts GitHub." That is unconditional on
// whether a stack number is also given — SKILL.md's own agent note says combining
// `--local` with an untracked number is merely an ERROR, not a network call — so this
// rule Approves whenever --local is present, number or not.
//
// Without --local, unstack is a real GitHub API write REGARDLESS of whether a number is
// given (SKILL.md's agent note: "gh stack unstack <number> is a remote-first API wrapper
// — it unstacks on GitHub by number from anywhere in the repo"). It never deletes the
// underlying branches or PRs, but it is still a remote mutation this rule does not clear
// unconditionally: Ask.
func (r *Rule) unstackVerdict(rest []string) (hookio.RuleResult, error) {
	if hasLocalFlag(rest) {
		return r.approve("unstack --local: removes ONLY local tracking state, keeps the GitHub stack grouping intact, and never contacts GitHub (SKILL.md: '--local ... never contacts GitHub')")
	}
	return r.ask("unstack (no --local): a remote-first GitHub API write that removes the stack grouping — PRs and branches are NOT deleted (SKILL.md 'Remove a stack'), but it is still a real remote mutation without --local")
}

// hasLocalFlag reads gh-stack's `--local` the way pflag resolves a repeated bool: last
// spelling wins, so `--local=false` after a bare `--local` is NOT local. `--local` has no
// short form and no other flag begins with "--local", so this needs no arity table.
func hasLocalFlag(rest []string) bool {
	isLocal := false
	for _, a := range rest {
		if a == "--" {
			break
		}
		if a == "--local" {
			isLocal = true
			continue
		}
		if v, ok := strings.CutPrefix(a, "--local="); ok {
			b, err := strconv.ParseBool(v)
			isLocal = err == nil && b
		}
	}
	return isLocal
}

// submitVerdict returns the verdict for `gh stack submit --auto[--open]` — rest being the
// tokens after the "submit" subcommand word. Both documented forms — with and without
// --open — resolve to Ask, for related but distinct reasons recorded per SKILL.md's own
// text, so this pass declines to promote either to Approve:
//
//   - `--auto` (no `--open`) drafts PRs by DEFAULT, per the SKILL's own quick-reference
//     table ("Push branches + create draft PRs"), structurally close to the
//     already-Approved `gh pr create --draft` in internal/rules/gh/pr.go. But submit ALSO
//     pushes real commits across the whole stack (not just PR metadata the way a bare `gh
//     pr create` would) — that additional write is enough uncertainty to default to Ask
//     rather than Approve this pass. COULD BECOME APPROVE: if a future pass concludes the
//     push-plus-draft-create combination carries no more risk than the draft-create alone,
//     record that reasoning as its own ruling before promoting this branch.
//   - `--auto --open` additionally marks PRs ready for review — the SAME ACT `gh pr
//     ready` performs, which operator ruling pg2-psiqh (2026-08-24) moved from Ask to
//     Abstain standalone (see internal/rules/gh/pr.go's prReadyVerdict). Whether pg2-psiqh
//     was meant to extend to THIS bundled push+create+ready call is genuinely unclear —
//     pg2-psiqh's own record discusses only the standalone `gh pr ready`. Ask is the more
//     conservative of the two readings (Ask vs. extending the Abstain), and is the one
//     chosen here; a future operator ruling on the bundled case would supersede this.
func (r *Rule) submitVerdict(rest []string) (hookio.RuleResult, error) {
	if hasOpenFlag(rest) {
		return r.ask("submit --auto --open: pushes real commits AND marks the PRs ready for review — the same act `gh pr ready` performs, which Abstains standalone per operator ruling pg2-psiqh, but bundled here with a push+create; whether that ruling extends to this bundled call is unclear (see package doc), so this defaults to the more conservative Ask rather than extending the Abstain")
	}
	return r.ask("submit --auto: drafts PRs by default (SKILL.md's quick-reference table), structurally close to the already-Approved `gh pr create --draft`, but ALSO pushes real commits across the whole stack (not just PR metadata) — that additional uncertainty defaults this to Ask rather than Approve this pass (see package doc for the 'could become Approve' revisit note)")
}

// hasOpenFlag reports whether `--open` is present. It is `submit`'s and `link`'s only
// boolean beyond what this rule already reads, has no short form, and no other flag
// begins with "--open" in either subcommand's documented flag set, so — like --local — it
// needs no arity table; last-spelling-wins mirrors hasLocalFlag.
func hasOpenFlag(rest []string) bool {
	isOpen := false
	for _, a := range rest {
		if a == "--" {
			break
		}
		if a == "--open" {
			isOpen = true
			continue
		}
		if v, ok := strings.CutPrefix(a, "--open="); ok {
			b, err := strconv.ParseBool(v)
			isOpen = err == nil && b
		}
	}
	return isOpen
}

// rebaseVerdict returns the verdict for `gh stack rebase` in ANY of its documented forms
// (--downstack, --upstack, --no-trunk, --continue, --abort, --remote, or a target
// [branch]).
//
// THIS MIRRORS PLAIN `git rebase`'S APPROVE VERDICT in internal/rules/git/git.go, rather
// than getting its own Ask bucket the way push/submit/link/sync do, and that is a
// deliberate departure from this bead's own design draft (which had bucketed `rebase`
// under Ask by default, with an explicit instruction to instead find and match whatever
// plain `git rebase` gets today rather than invent a new precedent here).
//
// WHY THE MIRROR LANDS ON APPROVE: git.go approves a plain (non -i/--interactive) `git
// rebase` unconditionally, and gates ONLY the interactive form absent an automated
// sequence editor (`git rebase -i` without GIT_SEQUENCE_EDITOR/sequence.editor set —
// see git.go's own rebase arm and its "requires editor" refusal). `gh stack rebase` has NO
// interactive/-i mode at all: its whole documented flag surface is --downstack, --upstack,
// --no-trunk, --continue, --abort, and --remote (SKILL.md "Rebase the stack") — there is
// no flag here that opens an editor or a TUI. Every gh-stack rebase invocation is
// therefore structurally the NON-interactive case git.go already Approves; there is no
// interactive variant to gate the way git.go's editor-requirement carve-out gates one.
//
// NOT RE-EXAMINED HERE: gh-stack's rebase cascades across the WHOLE stack (potentially
// many branches) in one call, a larger blast radius than git.go's own single-branch
// rebase. The design's instruction was specifically to mirror the existing precedent
// rather than invent a narrower one, so that difference is recorded but not separately
// gated this pass; a future pass narrowing this needs its own operator ruling, the same as
// any other verdict in this file.
func (r *Rule) rebaseVerdict() (hookio.RuleResult, error) {
	return r.approve("rebase: mirrors plain `git rebase`'s Approve verdict in internal/rules/git — gh-stack's rebase has no interactive/-i mode at all (its flags are --downstack/--upstack/--no-trunk/--continue/--abort/--remote per SKILL.md 'Rebase the stack'), so every invocation is structurally the NON-interactive case git.go already approves; there is no interactive variant here for an editor-requirement carve-out to gate")
}

// mergeVerdict returns the verdict for `gh stack merge` in EVERY documented spelling —
// bare, --yes, scoped to a stack or PR number, or with an explicit
// --squash/--rebase/--merge/--merge-method.
//
// This mirrors `gh pr merge`'s existing immediate-merge Reject in
// internal/rules/gh/gh.go EXACTLY: gh-stack's own SKILL.md agent rule #10 states plainly
// that in a non-interactive terminal "gh stack merge runs without prompting and merges the
// entire stack (bottom to top) atomically" — there is no human checkpoint to rely on, the
// same reason `gh pr merge` (without --auto) is Rejected.
//
// UNLIKE `gh pr merge`, there is no deferred/queued flag spelling here — no `--auto`
// equivalent that defers the merge to a later, gated act the way `gh pr merge --auto`
// defers to `gh pr ready`. Every flag `gh stack merge` documents
// (--yes/--squash/--rebase/--merge/--merge-method) only chooses HOW the immediate merge
// happens, never WHETHER it is immediate, so there is no Approve/Ask-eligible variant at
// all and this Reject has no carve-out.
func (r *Rule) mergeVerdict() (hookio.RuleResult, error) {
	return r.reject("merge is prohibited in every spelling: it merges the whole stack NOW (bottom to top, atomically, per SKILL.md agent rule #10), the same immediate-merge risk `gh pr merge` is Rejected for. Unlike `gh pr merge --auto`, there is no deferred/queued flag spelling here — every documented flag (--yes/--squash/--rebase/--merge/--merge-method) only chooses HOW the merge happens, never whether it is immediate — so there is no Approve/Ask-eligible variant to carve out")
}
