// Package vault is a config-driven MECHANISM for classifying HashiCorp Vault
// CLI commands via a read/write verb split (a hook-support parity capability;
// VaultCommandEvaluator). It follows the kubectl/buildtools template: the
// evaluation logic lives here in ceta-core, and the verb DATA (which
// subcommands are reads vs writes) arrives via an injected Config.
//
// SAFE DEFAULT: an empty config makes the rule Abstain on every command, so a
// consumer that ships no `vault` block has vault deferred entirely. The verb
// DATA arrives via an injected configrules.VaultConfig — the rules.json `vault`
// block, wired in by internal/setup/factory.go. Only once a consumer supplies
// verbs does the mechanism classify:
//   - a read verb (e.g. read/status/version, "kv get") -> Approve;
//   - a write verb (e.g. write/delete, "kv put") -> Ask;
//   - any other/unknown subcommand -> Abstain (defer to mode/settings).
//
// Verbs may be single tokens ("read") or two-token compounds ("kv get",
// "policy write"); the two-token form is matched first so "kv get" is a read
// even though bare "kv" is unclassified.
package vault

import (
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

type Rule struct {
	configured bool
	readVerbs  map[string]bool
	writeVerbs map[string]bool
}

// New constructs the vault rule from cfg (the rules.json `vault` block). A zero
// cfg makes the rule Abstain on every command (the safe base default).
func New(cfg configrules.VaultConfig) *Rule {
	return &Rule{
		configured: len(cfg.ReadVerbs) > 0 || len(cfg.WriteVerbs) > 0,
		readVerbs:  toSet(cfg.ReadVerbs),
		writeVerbs: toSet(cfg.WriteVerbs),
	}
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func (r *Rule) Name() string { return "vault" }

// notApplicable replaces the former abstain() helper. Every call site meant "this
// rule does not govern this input" and relied on the engine continuing — now the
// ErrNotApplicable control signal, NOT a terminal NoOpinion, which would shadow
// safe-commands and everything else registered after this rule.
func (r *Rule) notApplicable() (hookio.RuleResult, error) { return hookio.NotApplicable() }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return r.notApplicable()
	}
	// WS2 safe default: with no injected verbs, defer entirely. WS3 wires the
	// rules.json config that flips `configured` on.
	if !r.configured {
		return r.notApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		// Genuine failure: the tool is Bash and the rule IS configured, so it
		// governs this input and merely could not read it.
		return hookio.RuleResult{}, fmt.Errorf("vault: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "vault" {
			continue
		}
		return r.classify(pc.Args)
	}
	// No `vault` leaf in the command.
	return r.notApplicable()
}

// classify decides based on the vault subcommand, preferring a two-token
// compound match (e.g. "kv get") over the single leading token (e.g. "kv").
//
// It returns the (RuleResult, error) pair rather than a bare result because its
// fall-through is a not-applicable, which has no representation as a RuleResult:
// an unrecognized subcommand must let the chain continue exactly as it did when
// this returned Abstain.
func (r *Rule) classify(args []string) (hookio.RuleResult, error) {
	if len(args) >= 2 {
		compound := args[0] + " " + args[1]
		if r.readVerbs[compound] {
			return r.approve("kv/compound read verb: " + compound), nil
		}
		if r.writeVerbs[compound] {
			return r.ask("vault write verb requires approval: " + compound), nil
		}
	}
	if len(args) >= 1 {
		sub := args[0]
		if r.readVerbs[sub] {
			return r.approve("vault read verb: " + sub), nil
		}
		if r.writeVerbs[sub] {
			return r.ask("vault write verb requires approval: " + sub), nil
		}
	}
	// Subcommand in neither verb set: unclassified, so defer to the chain.
	return r.notApplicable()
}

func (r *Rule) approve(reason string) hookio.RuleResult {
	return hookio.RuleResult{Decision: hookio.Approve, Reason: reason, Module: r.Name()}
}

func (r *Rule) ask(reason string) hookio.RuleResult {
	return hookio.RuleResult{Decision: hookio.Ask, Reason: reason, Module: r.Name()}
}
