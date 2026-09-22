// Package pgccaudit approves the pg-ccaudit CLI's command family — the
// first-party Claude Code transcript-audit tool this workspace ships as
// packages/pg-ccaudit and prescribes throughout the pg-ccaudit plugin
// (claude-marketplace/pg-ccaudit's tool-error-waste-review skill/command and
// improvement-retro command).
//
// # Why a dedicated module (bead pg2-xu4aq)
//
// pg2-amzvw's plugin-conformance-check found ~20 unrecognized pg-ccaudit
// invocations in this repo's own claude-marketplace/ tree — every one of
// them a prescribed shape from the tool-error-waste-review skill — and
// allowlisted them via flake.nix's `--allow "pg-ccaudit"` rather than fixing
// them. Given that hit volume (the highest of the four gaps pg2-amzvw
// surfaced), this mirrors the existing first-party-CLI pattern
// (internal/rules/pnworkspace, internal/rules/killprobe): a small, HARDCODED
// (not consumer-configured) rule module, because the safety classification
// below is a property of the tool itself, uniform across every consumer,
// with no per-consumer data to inject.
//
// # The classification, and its source
//
// packages/pg-ccaudit/cmd/pg-ccaudit/main.go's own `usage` string is the
// authoritative source for this split — it groups the CLI's subcommands
// into two bands:
//
//   - "MISTAKE CENSUS (read-only; the three tiers, in order)": candidates,
//     classify, report, evaluate, gold, cost. The tool's own author labels
//     this whole band read-only. `classify`/`report` DO spend money calling
//     an LLM classifier ("with a reported run cost") and `gold seed`/`gold
//     sample` DO grow the on-disk gold-set cache, but both stay entirely
//     within pg-ccaudit's own SQLite index and cost ledger under
//     $XDG_DATA_HOME/pg-ccaudit — no arbitrary file writes, no credential
//     reads, no untrusted network egress (curl/ssh/vault's config-driven
//     host/verb gating exists for exactly that risk, and does not apply
//     here). `status`/`query`/`queries`/`schema`/`version`/`help` are
//     unambiguous metadata/read paths the usage string doesn't even bother
//     to caveat.
//   - `ingest` (top-level, deliberately NOT under the read-only band above):
//     "index new/appended transcripts (incremental, resumable,
//     single-instance)" — a real, potentially long-running write to the
//     persistent on-disk index. The tool-error-waste-review SKILL.md is
//     explicit that an agent must NOT run this on its own initiative
//     ("do not run `pg-ccaudit ingest` yourself unless the operator asks —
//     a first full ingest is a long job and publishing/priming machine
//     state is theirs to authorize"). That is a human-in-the-loop
//     requirement, not a "this is dangerous" one, so `ingest` resolves to
//     Ask rather than Reject.
//
// Any OTHER/unlisted subcommand (a future addition, or a typo) is left
// unmatched and falls through to NotApplicable — under-matching here only
// costs a prompt, never a wrong Approve (the same bias pnworkspace's own
// doc states).
package pgccaudit

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// approvedSubcommands are the pg-ccaudit subcommands classified read-only
// (metadata reads, or reads/writes confined to pg-ccaudit's own SQLite index
// and cost ledger) per main.go's usage string — see the package doc. A
// second-level argument (e.g. "classify status", "gold seed") does not
// change the verdict, so this matches on args[0] alone.
var approvedSubcommands = map[string]bool{
	"status": true, "query": true, "queries": true, "schema": true,
	"candidates": true, "classify": true, "report": true, "evaluate": true,
	"gold": true, "cost": true, "version": true, "help": true,
	"--version": true, "-V": true, "--help": true, "-h": true,
}

type Rule struct{}

// New constructs the pg-ccaudit rule. It takes no configuration: the
// classification is fixed by pg-ccaudit's own usage text and applies
// uniformly to every consumer, like pnworkspace/killprobe.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pg-ccaudit" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-ccaudit: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pg-ccaudit" {
			continue
		}
		if len(pc.Args) == 0 {
			// Bare invocation: prints the usage string and exits. Same as
			// an explicit "help".
			return r.approve("bare invocation (prints usage)")
		}
		sub := pc.Args[0]
		if sub == "ingest" {
			return r.ask("ingest is a long-running write to the persistent index; the tool-error-waste-review skill requires operator authorization before running it")
		}
		if approvedSubcommands[sub] {
			return r.approve("read-only subcommand " + sub)
		}
		// Some other, unlisted pg-ccaudit subcommand: defer.
		continue
	}
	return hookio.NotApplicable()
}

func (r *Rule) approve(what string) (hookio.RuleResult, error) {
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "pg-ccaudit: " + what,
		Module:   r.Name(),
	}, nil
}

func (r *Rule) ask(why string) (hookio.RuleResult, error) {
	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "pg-ccaudit: " + why,
		Module:   r.Name(),
	}, nil
}
