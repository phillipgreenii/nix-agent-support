// Package pnworkspace approves the routine, non-destructive `pn workspace`
// subcommand family per an explicit OPERATOR RULING (Phillip, 2026-09-17,
// bead pg2-4zyqf, implemented in pg2-vlpe1): `pn workspace build`, `pn
// workspace status`, `pn workspace push`, `pn workspace doctor`, `pn
// workspace update`, `pn workspace workforest add` and
// `pn workspace workforest list` all resolve to Approve.
//
// No existing rule module recognized `pn workspace <subcommand>` as a class
// (checked nix, buildtools, safecmds — none reference it), so these commands
// fell through to Abstain, and a handful were additionally DENIED outright by
// the auto-mode classifier under "[Modify Shared Resources]"/"Blocked by
// classifier" reasons (pg2-4zyqf's triage). This is a small, HARDCODED
// (not consumer-configured) rule: unlike kubectl/buildtools/vault's
// config-driven DATA-injection template, the ruling authorizing this relief
// applies to every consumer of this repo uniformly, so there is no per-consumer
// verb list to inject.
//
// # Scope — do not widen this
//
// The ruling explicitly does NOT extend relief to two commands that are a
// DIFFERENT, more dangerous risk class and MUST keep their current scrutiny:
//
//   - `pn workspace workforest remove` — deletes a pre-existing workforest
//     set's worktrees/branches. Deliberately ABSENT from
//     approvedWorkforestSubcommands below, so it falls through to
//     NotApplicable (deferred, unchanged) rather than being swept in by a
//     broad "workspace workforest" prefix match. internal/pathspec's pnKind
//     independently marks a workforest-set directory Delete: Forbidden for
//     the same reason.
//   - a direct write to pn-workspace.toml (e.g. `cp x.toml pn-workspace.toml`)
//     — the user-only cutover step for the active canonical workspace config.
//     This is not even a `pn` invocation, so this rule's basename check never
//     reaches it at all; the exclusion here is only about not letting the
//     broad "pn workspace" prefix accidentally cover it too (it can't: this
//     rule requires the executable to BE "pn").
//
// Any OTHER `pn workspace <subcommand>` (e.g. a future/unlisted verb) and any
// OTHER `pn workspace workforest <subcommand>` are left unmatched and fall
// through to NotApplicable — under-matching here only costs a prompt, never a
// wrong Approve, which is the deliberate bias (see e.g. buildtools'
// parseFlagName doc for the same principle applied elsewhere in this repo).
package pnworkspace

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// approvedSubcommands are the routine, non-destructive `pn workspace <sub>`
// spellings the 2026-09-17 operator ruling covers directly (i.e. every listed
// subcommand EXCEPT the "workforest" family, which is handled separately
// below because only two of its own subcommands are routine).
var approvedSubcommands = map[string]bool{
	"build": true, "status": true, "push": true, "doctor": true, "update": true,
}

// approvedWorkforestSubcommands are the routine `pn workspace workforest
// <sub>` spellings. "remove" is DELIBERATELY ABSENT — see the package doc's
// Scope section; it must keep its current (non-relaxed) scrutiny.
var approvedWorkforestSubcommands = map[string]bool{
	"add": true, "list": true,
}

type Rule struct{}

// New constructs the pn-workspace rule. It takes no configuration: the base
// allowlist is fixed by the 2026-09-17 operator ruling and applies uniformly,
// unlike the kubectl/buildtools/vault consumer-config template.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pn-workspace" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pn-workspace: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pn" {
			continue
		}
		// Require the LITERAL shape "pn workspace <sub> [...]" — no flag-skipping
		// before "workspace". A consumer session that puts a global flag ahead of
		// it simply does not match and stays at its current scrutiny (the
		// under-matching bias this package's doc explains), rather than this rule
		// growing its own flag-arity table to chase every spelling.
		if len(pc.Args) < 2 || pc.Args[0] != "workspace" {
			continue
		}
		sub := pc.Args[1]
		if sub == "workforest" {
			if len(pc.Args) >= 3 && approvedWorkforestSubcommands[pc.Args[2]] {
				return r.approve("pn workspace workforest " + pc.Args[2])
			}
			// "pn workspace workforest remove ..." (or any other/incomplete
			// workforest invocation) stays unmatched: fall through to the next
			// leaf/rule at its current scrutiny.
			continue
		}
		if approvedSubcommands[sub] {
			return r.approve("pn workspace " + sub)
		}
		// Some other, unlisted "pn workspace" subcommand: defer.
		continue
	}
	return hookio.NotApplicable()
}

func (r *Rule) approve(what string) (hookio.RuleResult, error) {
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "pn-workspace: routine subcommand " + what + " (operator ruling, Phillip, 2026-09-17)",
		Module:   r.Name(),
	}, nil
}
