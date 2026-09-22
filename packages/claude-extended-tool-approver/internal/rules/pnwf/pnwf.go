// Package pnwf approves/gates the repo-base `pnwf` workforest helper's command family —
// invoked as `pnwf <subcommand> ...` — per a fresh per-subcommand design (bead pg2-9w035).
//
// # Why this exists
//
// pg2-amzvw's plugin-conformance-check enumeration of phillipg-nix-repo-base's
// pn-workspace-rules plugin (SKILL.md, commands/*.md, agents/*.md) found pnwf's entire
// prescribed invocation surface (e.g. `landing=$(pnwf land-plan "$BRANCH")`) unrecognized by
// any rule in this chain — allowlisted via --allow/--allow-reason in flake.nix rather than
// fixed. pnwf (modules/pnwf/pnwf/pnwf.sh in phillipg-nix-repo-base) is a thin, deterministic
// wrapper over `pn workspace info --json` and guarded git primitives, invoked directly as its
// own executable — never as a `pn` subcommand, so the existing pnworkspace rule's basename
// check (which requires the executable to BE "pn") never matches it at all. It is prescribed
// by phillipg-nix-repo-base's fork-workforest/validate-workforest/land-workforest/
// cleanup-workforest skills and the /pn-workspace-sync and /pn-workspace-update commands (and
// their pnwf-runner/pnwf-update-runner agents).
//
// # Source of the classification
//
// Every verdict below was checked against the vendored `pnwf --help`/pnwf.md text at
// phillipg-nix-repo-base's modules/pnwf/pnwf/{pnwf.sh,pnwf.md}. pnwf.sh's own top-level
// dispatch comment counts ten implemented subcommands, already split by the tool itself into
// two documented buckets — "Subcommands (read-only, implemented)" and "Subcommands (mutating
// WORK-recipe helpers, not read-only probes)" — which this rule's Approve/Ask split follows,
// with one addition: cleanup's own force-flag surface gets a narrower split than "mutating
// therefore not read-only" alone would give it.
//
// # The classification, by bucket
//
// Approve, unconditionally, argument-blind — read-only/informational, no mutation surface at
// all (pnwf.sh's "Subcommands (read-only, implemented)" list):
//
//	resolve [--set], repos [--set], stage [--set],
//	fork-preflight <branch> [--repos a,b] (prints proceed/resume/stop plus a reason; never
//	  mutates — even its "stop" paths only report what git already shows, they never touch
//	  the canonical clone or any worktree),
//	land-plan <branch>, status <branch>, residue [--set]
//
// Approve — mutating WORK-recipe helpers, but each one mirrors a `pn workspace` verb this
// chain ALREADY approves unconditionally (operator ruling pg2-4zyqf/pg2-vlpe1, widened by
// pg2-tvdh3 — see internal/rules/pnworkspace), with pnwf's own pre-flight making it no less
// safe than the already-approved primitive it wraps:
//
//	sync-fetch [--set] — fetch + rebase each member (mirrors the already-approved `pn
//	  workspace rebase`), plus a conditional publish of a member's CANONICAL clone's primary
//	  to origin — but only if it is ahead, and never --force (mirrors the already-approved
//	  `pn workspace push`). Its own pre-flight is STRICTER than either primitive alone: it
//	  halts the whole run, before touching any member, if any canonical is ahead-AND-behind
//	  origin (a human-only reconciliation — R-3/R-8), and it refuses any member with a dirty
//	  working tree up front, closing the `rebase.autoStash` silent-conflict hole neither plain
//	  `git rebase` nor `pn workspace rebase` closes on its own.
//	update-relock [--set] — relocks every member in place via `pn workspace update
//	  --in-place` (mirrors the already-approved `pn workspace update`). Its own pre-flight is
//	  STRICTER than the primitive it calls: it refuses any member with a configured upstream
//	  (so the relock can never push) or a dirty tracked tree, and refuses to report success if
//	  the underlying relock silently skipped a member.
//
// Approve — cleanup <branch>, with NEITHER force flag present: the only mutation this shape
// can perform is removing a member CONFIRMED (via `git merge-base --is-ancestor`, checked
// before wtdone is ever invoked) to be an ancestor of its primary — i.e. already landed — and
// even then only via `wtdone` itself (already blanket-approved in internal/rules/safecmds;
// itself liveness-guarded and bounded to a non-force worktree remove + `git branch -d`).
// Every other member is left untouched and reported "kept". This is pnwf.sh's own
// "closed flag surface" case — the same shape wtdone itself is safe-listed for — so
// argument-blind trust is warranted: nothing reachable here can discard uncommitted or
// unmerged work.
//
// Ask — cleanup <branch> with --force-dirty-worktree-removal and/or
// --force-unlanded-branch-removal present: these two flags exist SPECIFICALLY to bypass the
// two checks that make the bare form safe — force-removing a worktree that has uncommitted
// changes, and/or `git branch -D`-deleting a branch that is NOT confirmed as an ancestor of
// primary (i.e. discarding commits that may exist nowhere else). This is the inverse of
// wtdone's own justification for argument-blind trust ("the tool's own flag surface is closed
// and has no force override" — see safecmds.go's alwaysSafe doc): cleanup's flag surface is
// NOT closed, so that reasoning does not carry over to this shape. Ask rather than Reject:
// abandoning a workforest set's unlanded work is a legitimate, deliberate operator choice
// pnwf.sh explicitly documents and supports — not a misuse — just not one this rule clears
// without a human in the loop.
package pnwf

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// approvedReadOnlySubcommands are the pnwf subcommands pnwf.sh itself documents as read-only
// probes ("Subcommands (read-only, implemented)"), excluding fork-preflight (which gets its
// own case only so its verdict text can cite its specific proceed/resume/stop contract).
var approvedReadOnlySubcommands = map[string]bool{
	"resolve": true, "repos": true, "stage": true,
	"land-plan": true, "status": true, "residue": true,
}

type Rule struct{}

// New constructs the pnwf rule. It takes no configuration: like pnworkspace/ghstack/killprobe,
// the classification is fixed by pnwf.sh's own documented behavior and applies uniformly, with
// no per-consumer data to inject.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pnwf" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pnwf: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pnwf" {
			continue
		}
		if len(pc.Args) < 1 {
			// Bare `pnwf` (no subcommand) prints help and exits non-zero — no mutation,
			// but also nothing to classify per-subcommand. Leave unclaimed.
			continue
		}
		sub := pc.Args[0]
		rest := pc.Args[1:]
		switch {
		case approvedReadOnlySubcommands[sub]:
			return r.approve(sub + ": read-only probe, no mutation surface (pnwf.sh's own 'Subcommands (read-only, implemented)' list)")
		case sub == "fork-preflight":
			return r.approve("fork-preflight: pre-flight check only — prints proceed/resume/stop plus a reason, never mutates the canonical clone or any worktree")
		case sub == "sync-fetch":
			return r.approve("sync-fetch: fetch+rebase each member plus a conditional non-force publish of a member's canonical primary — mirrors the already-approved `pn workspace rebase`/`push` (operator ruling pg2-4zyqf/pg2-vlpe1, widened pg2-tvdh3), with a STRICTER own pre-flight (upfront dirty-tree refusal, whole-run halt on any ahead-and-behind canonical)")
		case sub == "update-relock":
			return r.approve("update-relock: relocks every member in place via `pn workspace update --in-place` — mirrors the already-approved `pn workspace update`, with a STRICTER own pre-flight (refuses any member with an upstream or a dirty tracked tree, and refuses to report success on a silently-skipped member)")
		case sub == "cleanup":
			return r.cleanupVerdict(rest)
		default:
			// An unrecognized/future pnwf subcommand: leave unclaimed rather than guess.
			continue
		}
	}
	return hookio.NotApplicable()
}

func (r *Rule) approve(what string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "pnwf " + what, Module: r.Name()}, nil
}

func (r *Rule) ask(why string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Ask, Reason: "pnwf " + why, Module: r.Name()}, nil
}

// cleanupVerdict returns the verdict for `pnwf cleanup <branch> […]` — rest being the tokens
// after the "cleanup" subcommand word (the branch positional plus either force flag, in any
// order — pnwf.sh's own arg-parsing loop accepts them interleaved). Both force flags are
// pure booleans with no `=value` form in pnwf.sh's parser (an exact `case` match on the
// literal spelling; anything else, including a `--force-…=true` spelling, is an unrecognized
// argument pnwf itself rejects) — so an exact-token scan is complete, no arity table needed.
func (r *Rule) cleanupVerdict(rest []string) (hookio.RuleResult, error) {
	if hasForceFlag(rest) {
		return r.ask("cleanup with --force-dirty-worktree-removal and/or --force-unlanded-branch-removal: these bypass the two checks that make a bare `pnwf cleanup` safe — force-removing a worktree with uncommitted changes, and/or `git branch -D`-deleting a branch not confirmed as an ancestor of primary (discarding possibly-unmerged commits). Unlike wtdone's closed flag surface, cleanup's is not closed — a legitimate, deliberate choice, but not one this rule clears without a human")
	}
	return r.approve("cleanup (no force flag): only removes a member already CONFIRMED as an ancestor of its primary, and only via `wtdone` (already blanket-approved, liveness-guarded, non-force) — every other member is left in place and reported 'kept'; the same closed-flag-surface shape wtdone itself is safe-listed for")
}

func hasForceFlag(rest []string) bool {
	for _, a := range rest {
		if a == "--" {
			break
		}
		switch a {
		case "--force-dirty-worktree-removal", "--force-unlanded-branch-removal":
			return true
		}
	}
	return false
}
