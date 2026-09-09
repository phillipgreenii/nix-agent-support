package buildtools

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// baseApprovedTools are the generic, non-consumer-specific build tools approved
// unconditionally. Consumer tools (e.g. a consumer's Perl runners prove/yath,
// project scripts) are NOT here — they arrive via BuildtoolsConfig (ADR 0033).
//
// gogate (phillipg-nix-repo-base, bead pg2-i1hwk) belongs here rather than in a
// consumer's verbScopedApprovals: unlike `just` (deliberately excluded — see
// TestBuildtools_EmptyConfig_JustAbstains — because a recipe dispatcher's
// behavior is defined per-repo), gogate's behavior is FIXED across every
// consumer: a sequential `gofmt -l` / `go build` / `go vet` / `go test` gate
// with exactly two flags (`--pkg <pattern>`, `--quick`) plus a `--` passthrough
// to `go test`. It is not read-only (it writes the Go build cache, bead
// pg2-waxh7) — but neither is the already-unconditionally-approved `go` entry
// above, and gogate's surface is strictly narrower than bare `go` (no
// `go run`/`go generate`/arbitrary subcommand), so approving it here is no
// more permissive than the existing `go` entry.
var baseApprovedTools = map[string]bool{
	"go":     true,
	"gradle": true, "gradlew": true, "pre-commit": true, "prek": true, "bats": true, "bd": true,
	"tilt": true, "gogate": true,
}

type Rule struct {
	// pe supplies project-root context for approvedScriptDirs matching
	// (cmdparse.NormalizeExecutable). May be nil (e.g. a test constructing the
	// rule without exercising that feature); scriptInApprovedDir then always
	// reports false rather than panicking.
	pe                 *patheval.PathEvaluator
	approvedTools      map[string]bool
	approvedScripts    map[string]bool
	approvedScriptDirs []string                   // project-root-relative prefixes, each ending "/"
	verbScoped         map[string]map[string]bool // tool -> approved first-subcommand set
	// verbScopedDirs is tool -> verb -> the UNION of every VerbScopedApproval
	// entry's Dirs for that pair, each already normalized by
	// normalizeScriptDirs. A (tool,verb) absent here has no vetted directory at
	// all; see targetSensitiveVerbs and verbApprovesTarget for what that means.
	verbScopedDirs map[string]map[string][]string
	valueFlags     map[string]map[string]int // tool -> flag -> tokens consumed
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

// New constructs the build-tools rule. pe supplies project-root context for
// ApprovedScriptDirs matching (may be nil if that feature is not exercised).
// cfg carries the consumer-specific tool / script approvals injected by
// factory.go; a zero cfg yields the base generic tool set only (go/gradle/bats/…
// plus devbox search / cue vet / jar xf).
func New(pe *patheval.PathEvaluator, cfg configrules.BuildtoolsConfig) *Rule {
	r := &Rule{
		pe:                 pe,
		approvedTools:      mergeSet(baseApprovedTools, cfg.ApprovedTools),
		approvedScripts:    toSet(cfg.ApprovedScripts),
		approvedScriptDirs: normalizeScriptDirs(cfg.ApprovedScriptDirs),
		verbScoped:         map[string]map[string]bool{},
		verbScopedDirs:     map[string]map[string][]string{},
		valueFlags:         map[string]map[string]int{},
		allowedFlags:       map[string]map[string]bool{},
	}
	for _, vs := range cfg.VerbScopedApprovals {
		if r.verbScoped[vs.Tool] == nil {
			r.verbScoped[vs.Tool] = map[string]bool{}
		}
		r.verbScoped[vs.Tool][vs.Verb] = true
		if len(vs.Dirs) > 0 {
			if r.verbScopedDirs[vs.Tool] == nil {
				r.verbScopedDirs[vs.Tool] = map[string][]string{}
			}
			r.verbScopedDirs[vs.Tool][vs.Verb] = append(
				r.verbScopedDirs[vs.Tool][vs.Verb], normalizeScriptDirs(vs.Dirs)...,
			)
		}
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

// normalizeScriptDirs cleans each consumer-declared ApprovedScriptDirs entry
// into a "<relative/path>/" form: leading/trailing "/" trimmed, then exactly
// one trailing "/" appended, so prefix matching in scriptInApprovedDir cannot
// cross a directory-name boundary (".../scripts" must not match a sibling
// ".../scripts-evil"). A degenerate entry (empty, ".", "..") is DROPPED —
// narrowing what may match is the safe direction, matching parseFlagName /
// parseValueFlagSpec above.
func normalizeScriptDirs(raw []string) []string {
	dirs := make([]string, 0, len(raw))
	for _, d := range raw {
		d = strings.Trim(strings.TrimSpace(d), "/")
		if d == "" || d == "." || d == ".." {
			continue
		}
		dirs = append(dirs, d+"/")
	}
	return dirs
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

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("build-tools: read bash command: %w", err)
	}
	cwd := input.CWD
	if cwd == "" && r.pe != nil {
		cwd = r.pe.ProjectRoot()
	}
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if r.approvedTools[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool",
				Module:   r.Name(),
			}, nil
		}
		// Base-generic verb-scoped approvals (stay in the base, not config).
		if basename == "devbox" && baseVerbIs(pc.Args, "devbox", "search") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "devbox search is approved",
				Module:   r.Name(),
			}, nil
		}
		if basename == "cue" && baseVerbIs(pc.Args, "cue", "vet") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "cue vet is approved (read-only validation)",
				Module:   r.Name(),
			}, nil
		}
		if basename == "jar" && baseVerbIs(pc.Args, "jar", "xf") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved build tool: jar xf (extraction)",
				Module:   r.Name(),
			}, nil
		}
		// Consumer-configured verb-scoped approvals (additive over the base).
		if verbs := r.verbScoped[basename]; verbs != nil {
			if sub := firstSubcommand(pc.Args, r.flagPolicyFor(basename)); sub != "" && verbs[sub] {
				// tc-mgb6: a verb match alone is not enough for a TARGET-SENSITIVE
				// verb (deploy/terraform and siblings) — the justfile/working
				// directory the recipe actually runs from must also be vetted. See
				// verbApprovesTarget and VerbScopedApproval.Dirs' doc for the full
				// rule; a verb that is not target-sensitive and carries no Dirs is
				// approved unconditionally here, exactly as before this field
				// existed.
				if r.verbApprovesTarget(basename, sub, r.resolveTargetDir(basename, pc.Args, cwd)) {
					return hookio.RuleResult{
						Decision: hookio.Approve,
						Reason:   "approved verb-scoped tool: " + basename + " " + sub,
						Module:   r.Name(),
					}, nil
				}
				// Verb matched but its target is not (yet) vetted: fall through
				// rather than Approve. This leaf still isn't Rejected — a later
				// rule, or the chain's own abstain floor, decides from here.
			}
		}
		if r.approvedScripts[basename] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved project script: " + basename,
				Module:   r.Name(),
			}, nil
		}
		// Directory-prefix approval (BuildtoolsConfig.ApprovedScriptDirs): every
		// script under a configured directory is approved regardless of
		// basename or trailing args — for a skill/tool whose helper scripts have
		// unbounded basenames but a fixed directory (e.g.
		// ".claude/skills/silver-bullet/scripts/").
		if r.scriptInApprovedDir(pc.Executable, cwd) {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "approved script directory: " + basename,
				Module:   r.Name(),
			}, nil
		}
		// bash/sh <script> — check if the script arg is an approved script/tool,
		// or lives under an approved script directory.
		if (basename == "bash" || basename == "sh") && len(pc.Args) > 0 {
			scriptBase := filepath.Base(pc.Args[0])
			if r.approvedScripts[scriptBase] || r.approvedTools[scriptBase] || r.scriptInApprovedDir(pc.Args[0], cwd) {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "approved project script via " + basename + ": " + scriptBase,
					Module:   r.Name(),
				}, nil
			}
		}
	}
	return hookio.NotApplicable()
}

// scriptInApprovedDir reports whether executable, normalized relative to the
// project root via cmdparse.NormalizeExecutable (the same mechanism the
// monorepo rule uses), falls under one of the consumer-configured
// approvedScriptDirs. An absolute path under the project root and the
// equivalent relative path therefore match identically, so a script may be
// invoked with or without a leading "<worktree>/"-style prefix. An empty
// approvedScriptDirs (the default) or a nil path evaluator (no project-root
// context available) makes this always false.
func (r *Rule) scriptInApprovedDir(executable, cwd string) bool {
	if len(r.approvedScriptDirs) == 0 || r.pe == nil {
		return false
	}
	norm := filepath.ToSlash(cmdparse.NormalizeExecutable(executable, r.pe.ProjectRoot(), cwd))
	for _, dir := range r.approvedScriptDirs {
		if strings.HasPrefix(norm, dir) {
			return true
		}
	}
	return false
}

// baseVerbFlags is the BUILT-IN pre-verb flag allowlist behind the base-generic
// verb-scoped approvals (`devbox search`, `cue vet`, `jar xf`). Every tool named
// here resolves its verb under STRICT rules, so a dash token that is not listed
// resolves NO verb and the command Abstains. The predecessor of this table was a
// resolver that skipped every dash token unconditionally, with no allowlist.
//
// Why a BUILT-IN table is defensible here when a built-in valueFlags table was
// refused (bead tc-xjoe): the two fields fail in OPPOSITE directions. Declaring a
// flag value-taking when it is not over-skips tokens and can manufacture a wrong
// Approve, so that table had to stay consumer-authored. An ALLOW list can only
// ever be too SHORT, and an omitted flag costs exactly one prompt — never a wrong
// Approve. A base allowlist that is merely incomplete is therefore safe to ship.
//
// Every entry is a flag the tool accepts BEFORE its verb AND that alters only
// OUTPUT, never execution. Enumeration recorded 2026-08-09 (bead tc-457w):
//
//   - devbox 0.17.5, from `devbox --help` and the "Global Flags" block of
//     `devbox search --help`. The root command's only persistent flags are
//     `-q/--quiet` and `-h/--help`; both are boolean and log-suppressing. Nothing
//     names an interpreter, a config file, an env/dotenv file or a directory, and
//     devbox itself rejects anything else in that position ("Error: unknown flag:
//     --config"). NOT exploitable. Listed anyway, so the path fails closed if
//     devbox later grows such a flag. `--show-all` is deliberately absent: it
//     belongs to `search`, and written pre-verb cobra consumes the verb token as
//     its value (`devbox --show-all search cowsay` reports `unknown command
//     "cowsay"`), so allowing it would approve a spelling that never searches.
//   - cue 0.16.1, from the "Global Flags" block of `cue vet --help` and
//     `cue mod --help`. The root persistent flags are `-E/--all-errors`,
//     `-i/--ignore`, `-s/--simplify` and `-h/--help`, all boolean and output-only.
//     Cobra also honors `vet`'s own flags ahead of the verb (`cue -t xv=zzz vet
//     a.cue` evaluates), but those only select or inject DATA: CUE evaluation
//     cannot run an external command, and the one subcommand that can (`cue cmd`,
//     via tool/exec) is unreachable while `vet` holds the verb slot. The module
//     registry is chosen by $CUE_REGISTRY, not by a flag. NOT exploitable; those
//     vet-local flags are simply left off, which costs a prompt and nothing else.
//   - jar 21.0.12, from `jar --help` and `jar --help:compat`. EXPLOITABLE, which
//     is why its list is EMPTY. `xf` is not a subcommand at all — it is the legacy
//     operation-mode operand, and jar reads it as one only when it is the FIRST
//     argument. Put any dash token in front and jar switches to GNU-style parsing,
//     where `xf` degrades to a positional file operand and the operation MODE
//     comes from the flags instead. Measured: `jar -v xf a.jar` fails outright
//     ("One of options -{ctxuid} or --validate must be specified"), but
//     `jar --create --file=<path> xf` SUCCEEDS and overwrites <path> with a new
//     archive — which the old dash-skipping resolver approved as "jar xf
//     (extraction)". An empty list confines the approval to `jar xf ...` with `xf`
//     first, which is also the only spelling jar itself honors.
//
// This table is deliberately NOT merged with BuildtoolsConfig.AllowedFlags. A
// consumer needing a wider pre-verb surface declares its own verbScopedApprovals
// entry for the tool; that path is evaluated separately and carries its own
// consumer-authored policy, so the base approvals cannot be widened by config.
var baseVerbFlags = map[string][]string{
	"devbox": {"-q", "--quiet", "-h", "--help"},
	"cue":    {"-E", "--all-errors", "-i", "--ignore", "-s", "--simplify", "-h", "--help"},
	"jar":    {},
}

// baseFlagPolicies compiles baseVerbFlags through the same parseFlagName
// validator the consumer field uses, so `-`, `--`, and glued or arity spellings
// can never enter the base allowlist either.
var baseFlagPolicies = compileBaseFlagPolicies()

func compileBaseFlagPolicies() map[string]flagPolicy {
	policies := make(map[string]flagPolicy, len(baseVerbFlags))
	for tool, flags := range baseVerbFlags {
		set := make(map[string]bool, len(flags))
		for _, f := range flags {
			if name, ok := parseFlagName(f); ok {
				set[name] = true
			}
		}
		policies[tool] = flagPolicy{allowed: set, strict: true}
	}
	return policies
}

// baseVerbIs reports whether args resolve, under tool's built-in strict policy,
// to exactly verb. A tool with no entry in baseFlagPolicies resolves nothing:
// the zero flagPolicy is NOT strict, so falling through to it would restore the
// unconditional dash-skipping this function exists to remove.
func baseVerbIs(args []string, tool, verb string) bool {
	policy, ok := baseFlagPolicies[tool]
	if !ok {
		return false
	}
	return firstSubcommand(args, policy) == verb
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
// p.strict closes that hole. When the policy is strict — because the consumer
// declared an allowedFlags entry for the tool, or because the tool is one of the
// base-generic verbs compiled into baseFlagPolicies — a dash-token is skipped ONLY
// if it is a declared value flag, or a declared allowed flag spelled BARE;
// anything else ends resolution with "".
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
		// bareAllowed is the ONE shape a non-value-flag dash-token may take under a
		// strict policy: a declared allowed flag, spelled BARE. An allowed flag is
		// boolean by declaration, so `--allowed=value` contradicts the declaration —
		// which is exactly how a mis-declared dangerous flag (`--shell` listed as
		// allowed) would otherwise smuggle its value past the verb slot. The
		// condition is NAMED rather than inlined as `!(p.allowed[name] && !glued)`
		// so the guard still reads in the POSITIVE form the rule is stated in
		// (staticcheck QF1001 rejects the inline negated conjunction); the extra map
		// read when the policy is not strict is side-effect-free.
		bareAllowed := p.allowed[name] && !glued
		if p.strict && !ok && !bareAllowed {
			// Neither a declared value flag nor a bare declared allowed flag: verb
			// resolution ends here, and "" can never approve.
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

// --- tc-mgb6: target-aware deploy/terraform verb scoping ---

// targetSensitiveVerbs names, per tool, the verb spellings that MUST NOT
// approve from an unvetted justfile/target — tc-mgb6's operator ruling
// ("make it target-aware"). A homelab-style consumer's rules.json declares
// "deploy"/"terraform" (and these siblings) as bare VerbScopedApprovals
// entries with no Dirs; the tc-mgb6 defect was exactly that bare declaration
// auto-approving EVERY justfile's "deploy"/"terraform" recipe alike,
// including infrastructure/machines/monorepod's own Proxmox-host-level
// `terraform apply`, identically to a routine k3s cluster app deploy
// (infrastructure/k3s/kinfra/justfile).
//
// Listing a verb here does not by itself narrow anything for a consumer who
// has already vetted a target — VerbScopedApproval.Dirs is what re-widens a
// specific target once vetted; see its doc. It only changes what an ENTRY
// WITH NO Dirs AT ALL means for these particular spellings: never approve,
// rather than approve everywhere (verbApprovesTarget).
//
// Keyed by tool because these verb spellings are a `just` convention here,
// not a universal one — a different tool's "deploy" verb is unaffected unless
// a future consumer's entry for it is added here too. The exact set is the
// one enumerated in tc-mgb6's root-cause paragraph: "deploy" and "terraform"
// plus their named siblings "deploy-manual", "deploy-remote", "deploy-self",
// "deploy-swarm", "terraform-infra", "undeploy".
var targetSensitiveVerbs = map[string]map[string]bool{
	"just": {
		"deploy": true, "deploy-manual": true, "deploy-remote": true,
		"deploy-self": true, "deploy-swarm": true, "undeploy": true,
		"terraform": true, "terraform-infra": true,
	},
}

// verbApprovesTarget decides, for an ALREADY-MATCHED (tool,verb) verb-scoped
// approval, whether targetDir (see resolveTargetDir) is one this entry may
// approve from.
//
// No Dirs declared for (tool,verb): a target-sensitive verb (targetSensitiveVerbs)
// has NOTHING vetted, so it can never approve here — the fail-safe direction
// tc-mgb6 requires. Any other verb is unrestricted, exactly as this rule
// behaved before Dirs existed. Dirs declared: targetDir must fall under one
// of them regardless of sensitivity — a consumer MAY opt an ordinary verb
// into directory scoping too, harmlessly.
func (r *Rule) verbApprovesTarget(tool, verb, targetDir string) bool {
	dirs := r.verbScopedDirs[tool][verb]
	if len(dirs) == 0 {
		return !targetSensitiveVerbs[tool][verb]
	}
	return underAnyPrefix(targetDir, dirs)
}

// underAnyPrefix reports whether dir (already normalized/slash-terminated by
// normalizeJustDir) has one of prefixes (already normalized by
// normalizeScriptDirs) as a prefix.
func underAnyPrefix(dir string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(dir, p) {
			return true
		}
	}
	return false
}

// justTargetDir scans a `just` invocation's argv for the two flags whose
// value identifies the justfile to run (-f/--justfile) or the working
// directory to run it from (-d/--working-directory) — the same two flags
// BuildtoolsConfig.ValueFlags' doc names as safe to declare for value-flag
// skipping (tc-xjoe). This reads the SAME pair for what they point AT rather
// than skipping over them, which is what tc-mgb6's target-aware scoping
// needs. isFile reports which of the two matched (true for -f/--justfile,
// whose value is a FILE and must still have its parent directory taken by the
// caller); found reports whether either was present at all.
func justTargetDir(args []string) (value string, isFile, found bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" || a[0] != '-' {
			continue
		}
		name := a
		v, haveValue := "", false
		// Glued form (-f=path / --justfile=path): reuse cmdparse.GluedFlagValue
		// rather than a bespoke split, so this shares the same quote-unwrap and
		// malformed-value handling as every other glued-flag reader in this
		// codebase (pg2-52eod). A malformed value is treated as NOT found — the
		// caller then falls back to cwd, which is the fail-safe side (almost
		// certainly still won't match a vetted Dirs prefix).
		if val, ok, malformed := cmdparse.GluedFlagValue(a); ok && !malformed {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				name = a[:eq]
			}
			v, haveValue = val, true
		}
		switch name {
		case "-f", "--justfile":
			if !haveValue {
				if i+1 >= len(args) {
					continue
				}
				v = args[i+1]
			}
			return v, true, true
		case "-d", "--working-directory":
			if !haveValue {
				if i+1 >= len(args) {
					continue
				}
				v = args[i+1]
			}
			return v, false, true
		}
	}
	return "", false, false
}

// resolveTargetDir is the directory a `just` invocation's matched verb will
// actually run from: -f/--justfile's value's parent directory, or
// -d/--working-directory's value directly, falling back to cwd when neither
// is present (which is what `just` itself does) or when tool is not "just" at
// all (the -f/-d convention is just-specific; another tool's Dirs match
// against its own cwd only).
func (r *Rule) resolveTargetDir(tool string, args []string, cwd string) string {
	rawDir := cwd
	if tool == "just" {
		if v, isFile, found := justTargetDir(args); found {
			if isFile {
				rawDir = filepath.Dir(v)
			} else {
				rawDir = v
			}
		}
	}
	projectRoot := ""
	if r.pe != nil {
		projectRoot = r.pe.ProjectRoot()
	}
	return normalizeJustDir(rawDir, projectRoot, cwd)
}

// normalizeJustDir resolves dir (relative to cwd when not already absolute)
// to a project-root-relative, slash-separated, slash-TERMINATED path — the
// same shape normalizeScriptDirs puts VerbScopedApproval.Dirs entries in, so
// underAnyPrefix can compare them directly. An absolute dir outside
// projectRoot, or an empty projectRoot (no path-evaluator context), is
// returned as a plain slash-separated path instead: it cannot start with any
// project-root-relative prefix, which is the fail-safe (Abstain) direction —
// mirroring cmdparse.NormalizeExecutable's own project-root containment check
// (that helper is not reused directly here because its "no '/' at all"
// special case is an executable-PATH-search convention that does not apply to
// a directory value).
func normalizeJustDir(dir, projectRoot, cwd string) string {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	dir = filepath.Clean(dir)
	projectRoot = filepath.Clean(projectRoot)
	if projectRoot != "" && projectRoot != "." && (dir == projectRoot || strings.HasPrefix(dir+"/", projectRoot+"/")) {
		if rel, err := filepath.Rel(projectRoot, dir); err == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel) + "/"
		}
	}
	return filepath.ToSlash(dir) + "/"
}
