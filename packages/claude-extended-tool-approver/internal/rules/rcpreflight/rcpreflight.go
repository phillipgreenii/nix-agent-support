// Package rcpreflight approves/gates phillipg-nix-ziprecruiter's zr-refactor plugin's
// worktree-pool lifecycle script `rc-preflight` (bead pg2-o9rcj, follow-up to pg2-xf564).
//
// # Why this exists
//
// rc-preflight's "subcommands" are dash-prefixed FLAGS (--verify, --release, --list,
// --force-release), plus a bare `rc-preflight <actor>` acquire form with no fixed verb
// text at all. buildtools.verbScopedApprovals' matcher (firstSubcommand, in
// internal/rules/buildtools/buildtools.go) structurally cannot resolve a dash-token as a
// verb — under a non-strict policy a dash-prefixed token is only ever SKIPPED, and under a
// strict policy it only ever ENDS resolution ("" cannot approve anything); a dash-literal
// can never itself become the returned "sub" a verbScoped entry is keyed on. That
// extension point can therefore never cover this tool, confirmed by reading the code
// during pg2-o9rcj's investigation — this package is the Go-level fix, following the
// one-module-per-tool precedent of ghstack/pgpr/pgccaudit/pnwf rather than restructuring
// buildtools' resolver.
//
// # Source of the classification
//
// Checked directly against the vendored script, phillipg-nix-ziprecruiter's
// modules/zr-refactor/rc-preflight/rc-preflight.sh (read in full, 2026-09-22), and its
// "operator-only" documentation at that repo's
// claude-marketplace/zr-refactor/commands/status.md: "rc-preflight never guesses liveness
// from a PID — there is deliberately no automatic dead-holder detection, so the operator
// judges deadness ... and decides whether to run the force-release command. Do not run it
// yourself." The script's own top comment states the same design: "Locks: ...NO
// pid-liveness — a lock is held until released; --force-release is the operator's only
// reclaim."
//
// # The classification, by shape
//
// Approve, unconditionally, argument-blind — every shape has no destructive or
// privileged side effect:
//
//	-h / --help            prints usage and exits 0; no side effect at all.
//	--verify <wt> <actor>  read-only assertion only (toplevel + own-lock + clean-tree
//	                       check) — the script's own comment states it "never cleans",
//	                       even when the tree is dirty ("a person rules; never
//	                       auto-clean").
//	--release <actor>      removes ONLY the lock file(s) whose recorded owner string
//	                       equals the given actor, and always exits 0. Every documented
//	                       call site (phillipg-nix-ziprecruiter's zr-refactor work.md
//	                       LOOP-exit step: "Always release the pool member") passes the
//	                       caller's OWN actor id to hand back its own member — unlike
//	                       --force-release below, no documentation anywhere marks this
//	                       flag operator-only, and it carries no pool-slot-number
//	                       argument that would let it target a member by POSITION rather
//	                       than by the caller's own actor identity.
//	--list                 read-only: prints pool members, holders, and held-since
//	                       timestamps.
//	<actor> (bare acquire) idempotent for the caller's own actor (re-acquire/repair of a
//	                       crashed create), or reserves a free pool slot under a
//	                       flock-guarded mutex and creates its worktree via `bin/worktree
//	                       create ... --no-push`. Never touches or evicts another actor's
//	                       held member.
//
// Ask, unconditionally — the one operator-only reclaim, in its one recognized spelling:
//
//	--force-release <n>   force-evicts an ARBITRARY pool member's held lock
//	                       (`rm -f "$POOL/$n.lock"`) with NO owner check at all — unlike
//	                       --release above, it targets a member by pool-slot NUMBER
//	                       (discoverable via --list) rather than by the caller's own
//	                       actor identity, so it can evict a session that never
//	                       authorized it. Explicitly documented as the operator's
//	                       decision to make ("do not run it yourself" — see the citation
//	                       above). Ask rather than Reject: reclaiming a dead session's
//	                       member is a legitimate, deliberate operator action the tool
//	                       explicitly supports — just not one this rule clears without a
//	                       human, mirroring pnwf's cleanup force-flag verdicts.
//
// Every other spelling is left UNCLAIMED rather than guessed at: rc-preflight.sh's own
// `--*` case arm matches any OTHER token starting with two literal dashes (including a
// glued form like `--force-release=3`, which the script's exact-string case pattern does
// NOT recognize) and dies "unknown option" — harmless, but not a shape this rule
// classifies. A single-dash token that is not exactly "-h" (e.g. `-x`) is NOT caught by
// that `--*` arm (it only matches strings starting with TWO dashes) and instead falls
// through to the script's bare-acquire default arm, becoming the ACTOR value — this rule
// mirrors that exactly rather than treating every leading "-" as "unrecognized flag".
package rcpreflight

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type Rule struct{}

// New constructs the rc-preflight rule. It takes no configuration: like
// pnwf/ghstack/pgpr/pgccaudit, the classification is fixed by rc-preflight.sh's own
// source and applies uniformly, with no per-consumer data to inject.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "rc-preflight" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("rc-preflight: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "rc-preflight" {
			continue
		}
		if len(pc.Args) == 0 {
			// Bare `rc-preflight` (no flag, no actor) dies with a usage error and
			// mutates nothing, but there is no argument to classify at all. Leave
			// unclaimed rather than guess.
			continue
		}
		switch first := pc.Args[0]; {
		case first == "-h" || first == "--help":
			return r.approve("-h/--help: prints usage and exits 0, no side effect")
		case first == "--verify":
			return r.approve("--verify <wt> <actor>: read-only toplevel+lock+clean-tree assertion; the script's own comment states it never cleans, even on a dirty tree")
		case first == "--release":
			return r.approve("--release <actor>: removes only the lock file(s) owned by the given actor and always exits 0; every documented call site hands back the caller's OWN actor id (phillipg-nix-ziprecruiter zr-refactor work.md's unconditional release-on-exit step) — unlike --force-release, no documentation marks this operator-only, and it targets by actor identity, not by pool-slot number")
		case first == "--list":
			return r.approve("--list: read-only, prints pool members/holders/held-since")
		case first == "--force-release":
			return r.ask("--force-release <n>: force-evicts an ARBITRARY pool member's held lock with no owner check at all (unconditional rm -f of the lock file), targeting by pool-slot NUMBER rather than by the caller's own actor identity — explicitly documented as the operator's call to make (\"the operator judges deadness ... do not run it yourself\", phillipg-nix-ziprecruiter claude-marketplace/zr-refactor/commands/status.md)")
		case strings.HasPrefix(first, "--"):
			// rc-preflight.sh's own `--*` catch-all: any other TWO-dash-prefixed
			// token (including a glued spelling the script's exact-string case
			// match does not otherwise recognize, e.g. "--force-release=3") dies
			// "unknown option" — harmless, but not a shape this rule classifies.
			// Leave unclaimed rather than guess.
			continue
		default:
			// The bare acquire form: `rc-preflight <actor>`. This also covers a
			// single-dash token that is not exactly "-h" (e.g. "-x") — the real
			// script's case statement does not match it against any flag arm
			// either, so it falls through to become the ACTOR value there too.
			return r.approve(first + ": bare acquire — idempotent for the caller's own actor (re-acquire/repair), or reserves a free pool slot and creates its worktree; never touches another actor's held member")
		}
	}
	return hookio.NotApplicable()
}

func (r *Rule) approve(what string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "rc-preflight " + what, Module: r.Name()}, nil
}

func (r *Rule) ask(why string) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Ask, Reason: "rc-preflight " + why, Module: r.Name()}, nil
}
