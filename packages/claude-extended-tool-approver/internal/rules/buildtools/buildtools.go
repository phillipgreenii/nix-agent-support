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
	// allowedFlags is tool -> flag -> true. The PRESENCE of a tool key (even with
	// an empty set) puts that tool in STRICT flag checking; see flagPolicy.
	allowedFlags map[string]map[string]bool
}

// flagPolicy is the per-tool flag knowledge firstSubcommand resolves a verb with.
//
// strict is the allowlist switch: it is set iff the consumer declared an
// allowedFlags entry for the tool. When strict, a flag that is in neither map
// stops verb resolution (fail CLOSED); when not strict, it is skipped (the
// historical behavior).
type flagPolicy struct {
	valueFlags map[string]int
	allowed    map[string]bool
	strict     bool
}

func (r *Rule) flagPolicyFor(tool string) flagPolicy {
	allowed, strict := r.allowedFlags[tool]
	return flagPolicy{valueFlags: r.valueFlags[tool], allowed: allowed, strict: strict}
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
		allowedFlags:    map[string]map[string]bool{},
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
	// The tool key is created even when the list is empty or every entry is
	// rejected: the KEY is the strict-mode switch, so an operator can declare
	// `"allowedFlags": {"just": []}` to mean "no bare flag is acceptable".
	for tool, flags := range cfg.AllowedFlags {
		set := map[string]bool{}
		for _, f := range flags {
			if name, ok := parseFlagName(f); ok {
				set[name] = true
			}
		}
		r.allowedFlags[tool] = set
	}
	return r
}

// parseFlagName validates an allowedFlags entry. It accepts a bare flag NAME and
// nothing else — no `:<n>` arity (an allowed flag consumes no tokens by
// definition) and no `=value` (matching is done on the name half of a glued
// token). Bare `-` and `--` are rejected so the end-of-flags separator can never
// be declared acceptable.
//
// A rejected entry is DROPPED, which NARROWS what may resolve a verb. That is the
// opposite bias from parseValueFlagSpec — but both are the same rule applied to
// opposite fields: on failure, prefer the outcome that can only Abstain.
func parseFlagName(spec string) (string, bool) {
	if !strings.HasPrefix(spec, "-") || spec == "-" || spec == "--" {
		return "", false
	}
	if strings.ContainsAny(spec, "=:") {
		return "", false
	}
	return spec, true
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
			if sub := firstSubcommand(pc.Args, r.flagPolicyFor(basename)); sub != "" && verbs[sub] {
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
// flag's VALUE, or "" when no verb can be resolved. "" can never approve.
//
// p.valueFlags maps a flag name to the number of following tokens it consumes
// (BuildtoolsConfig.ValueFlags). A nil/empty policy yields exactly the historical
// behavior — skip dash-prefixed tokens, return the first remaining token — so a
// tool the consumer has said nothing about is unaffected.
//
// Forms handled:
//   - separated  `-f <path> <verb>`   -> the path is skipped, verb resolves
//   - glued      `-f=<path> <verb>`   -> the value is inline, so nothing extra is
//     skipped (an n-value flag still consumes n-1 further tokens)
//   - multi-value `--set <NAME> <VALUE> <verb>` when declared as `--set:2`
//   - trailing   `just -f`            -> the loop simply runs off the end and
//     returns "", which cannot approve anything
//
// An UNDECLARED value-taking flag is fail-safe in the SEPARATED form only: its
// value is returned as the "verb", which matches no approval entry. That is NOT
// enough on its own — in the GLUED form (`--shell=/bin/x <verb>`) the flag and its
// value are ONE dash-token, so the loop skips both and the real verb resolves.
//
// p.strict closes that hole. When the consumer has declared an allowedFlags entry
// for the tool, a dash-token is skipped ONLY if it is a declared value flag, or a
// declared allowed flag spelled BARE; anything else ends resolution with "".
// Every non-canonical spelling lands there: `--shell=/bin/x`, an attached short
// value (`-E/tmp/x`), a clustered short group (`-nq`), a value glued onto a flag
// declared boolean (`--quiet=x`), and the end-of-flags separator `--`.
func firstSubcommand(args []string, p flagPolicy) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" || a[0] != '-' {
			return a
		}
		name, glued := a, false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name, glued = a[:eq], true
		}
		n, ok := p.valueFlags[name]
		if p.strict && !ok && !(p.allowed[name] && !glued) {
			// Not a declared value flag, so it must be a declared allowed flag
			// AND spelled bare. An allowed flag is boolean by declaration, so
			// `--allowed=value` contradicts the declaration — which is exactly
			// how a mis-declared dangerous flag (`--shell` listed as allowed)
			// would otherwise smuggle its value past the verb slot.
			return ""
		}
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
