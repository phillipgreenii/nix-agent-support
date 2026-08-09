package buildtools

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// baseApprovedTools are the generic, non-consumer-specific build tools approved
// unconditionally. Consumer tools (e.g. ZR's Perl runners prove/yath, project
// scripts) are NOT here — they arrive via BuildtoolsConfig (ADR 0033).
var baseApprovedTools = map[string]bool{
	"go":     true,
	"gradle": true, "gradlew": true, "pre-commit": true, "prek": true, "bats": true, "bd": true,
	"tilt": true,
}

type Rule struct {
	approvedTools   map[string]bool
	approvedScripts map[string]bool
	verbScoped      map[string]map[string]bool // tool -> approved first-subcommand set
	valueFlags      map[string]map[string]int  // tool -> flag -> tokens consumed
}

// New constructs the build-tools rule. cfg carries the consumer-specific tool /
// script approvals injected by factory.go; a zero cfg yields the base generic
// tool set only (go/gradle/bats/… plus devbox search / cue vet / jar xf).
func New(cfg configrules.BuildtoolsConfig) *Rule {
	r := &Rule{
		approvedTools:   mergeSet(baseApprovedTools, cfg.ApprovedTools),
		approvedScripts: toSet(cfg.ApprovedScripts),
		verbScoped:      map[string]map[string]bool{},
		valueFlags:      map[string]map[string]int{},
	}
	for _, vs := range cfg.VerbScopedApprovals {
		if r.verbScoped[vs.Tool] == nil {
			r.verbScoped[vs.Tool] = map[string]bool{}
		}
		r.verbScoped[vs.Tool][vs.Verb] = true
	}
	for tool, specs := range cfg.ValueFlags {
		for _, spec := range specs {
			name, arity, ok := parseValueFlagSpec(spec)
			if !ok {
				continue
			}
			if r.valueFlags[tool] == nil {
				r.valueFlags[tool] = map[string]int{}
			}
			r.valueFlags[tool][name] = arity
		}
	}
	return r
}

// parseValueFlagSpec splits a valueFlags entry into its flag name and the number
// of following tokens it consumes: "--justfile" -> ("--justfile", 1),
// "--set:2" -> ("--set", 2).
//
// A spec whose ":<n>" suffix is not a positive integer, or that does not look
// like a flag, is REJECTED (ok=false) and dropped by the caller. It is
// deliberately NOT assumed to be arity 1: over-skipping tokens is the only
// direction in which a value-flag declaration can manufacture a wrong Approve,
// whereas dropping the entry merely restores the pre-existing behavior (the
// flag's value lands in the verb slot and the command Abstains).
func parseValueFlagSpec(spec string) (string, int, bool) {
	name, arity := spec, 1
	if i := strings.LastIndexByte(spec, ':'); i >= 0 {
		n, err := strconv.Atoi(spec[i+1:])
		if err != nil || n < 1 {
			return "", 0, false
		}
		name, arity = spec[:i], n
	}
	if !strings.HasPrefix(name, "-") || name == "-" || name == "--" {
		return "", 0, false
	}
	return name, arity, true
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func mergeSet(base map[string]bool, extra []string) map[string]bool {
	m := make(map[string]bool, len(base)+len(extra))
	for k := range base {
		m[k] = true
	}
	for _, e := range extra {
		m[e] = true
	}
	return m
}

func (r *Rule) Name() string {
	return "build-tools"
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
		basename := filepath.Base(pc.Executable)
		if r.approvedTools[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool",
				Module:   r.Name(),
			}
		}
		// Base-generic verb-scoped approvals (stay in the base, not config).
		if basename == "devbox" && hasSubcommand(pc.Args, "search") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "devbox search is approved",
				Module:   r.Name(),
			}
		}
		if basename == "cue" && hasSubcommand(pc.Args, "vet") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "cue vet is approved (read-only validation)",
				Module:   r.Name(),
			}
		}
		if basename == "jar" && hasSubcommand(pc.Args, "xf") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool: jar xf (extraction)",
				Module:   r.Name(),
			}
		}
		// Consumer-configured verb-scoped approvals (additive over the base).
		if verbs := r.verbScoped[basename]; verbs != nil {
			if sub := firstSubcommand(pc.Args, r.valueFlags[basename]); sub != "" && verbs[sub] {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "approved verb-scoped tool: " + basename + " " + sub,
					Module:   r.Name(),
				}
			}
		}
		if r.approvedScripts[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved project script: " + basename,
				Module:   r.Name(),
			}
		}
		// bash/sh <script> — check if the script arg is an approved script/tool.
		if (basename == "bash" || basename == "sh") && len(pc.Args) > 0 {
			scriptBase := filepath.Base(pc.Args[0])
			if r.approvedScripts[scriptBase] || r.approvedTools[scriptBase] {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "approved project script via " + basename + ": " + scriptBase,
					Module:   r.Name(),
				}
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func hasSubcommand(args []string, sub string) bool {
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		return a == sub
	}
	return false
}

// firstSubcommand returns the first non-flag argument that is not consumed as a
// flag's VALUE, or "".
//
// valueFlags maps a flag name to the number of following tokens it consumes; it
// is the per-tool data from BuildtoolsConfig.ValueFlags. A nil/empty map yields
// exactly the historical behavior (skip dash-prefixed tokens, return the first
// remaining token) — so a tool with no declared value flags is unaffected.
//
// Forms handled:
//   - separated  `-f <path> <verb>`   -> the path is skipped, verb resolves
//   - glued      `-f=<path> <verb>`   -> the value is inline, so nothing extra is
//     skipped (an n-value flag still consumes n-1 further tokens)
//   - multi-value `--set <NAME> <VALUE> <verb>` when declared as `--set:2`
//   - trailing   `just -f`            -> the loop simply runs off the end and
//     returns "", which cannot approve anything
//
// An UNDECLARED value-taking flag stays fail-safe: its value is returned as the
// "verb", which matches no approval entry, so the command Abstains.
func firstSubcommand(args []string, valueFlags map[string]int) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" || a[0] != '-' {
			return a
		}
		name, glued := a, false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name, glued = a[:eq], true
		}
		n, ok := valueFlags[name]
		if !ok {
			continue
		}
		if glued {
			n--
		}
		i += n
	}
	return ""
}
