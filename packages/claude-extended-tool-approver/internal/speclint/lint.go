package speclint

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

// Severity is a Finding's disposition: WARN never fails the linter's exit
// code, HARD does.
type Severity string

const (
	SeverityWarn Severity = "warn"
	SeverityHard Severity = "hard"
)

// Check names one of this package's independently-reportable checks (see
// doc.go).
type Check string

const (
	CheckCitationPresence  Check = "citation-presence"
	CheckDangerFlagRole    Check = "danger-flag-role"
	CheckUnknownFlagInert  Check = "unknown-flag-inert-justification"
	CheckOverridesConflict Check = "overrides-conflict"
	CheckInvalidSpec       Check = "invalid-spec"
)

// Finding is one independently-reportable lint result.
type Finding struct {
	Severity Severity
	Check    Check
	// Spec identifies the command this finding is about — the top-level
	// command name, with "/<subcommand>" appended for a nested command,
	// matching specfmt/validate.go's own "specName+\"/\"+name" convention.
	Spec string
	// Field is the specific fact within Spec this finding is about (a
	// citation key, a flag spelling, "positionals", etc.) — empty for a
	// finding that is about the whole spec (overrides-conflict,
	// invalid-spec).
	Field string
	// Detail is a human-readable explanation.
	Detail string
}

func (f Finding) String() string {
	loc := f.Spec
	if f.Field != "" {
		loc = fmt.Sprintf("%s: %s", f.Spec, f.Field)
	}
	return fmt.Sprintf("[%s] %s: %s: %s", f.Severity, f.Check, loc, f.Detail)
}

// registryGoThinMarkers is the interim citation shape packet 1.2's
// generator (embeddedspecs/generate.go) stamps on every fact it marshals
// from cmddesc.DefaultRegistry() — "internal/cmddesc/registry.go:
// cmddesc.DefaultRegistry()[\"<name>\"]". A citation matching it points at
// where the FIELD LIVES, not at the --help/man/source-line evidence a real
// citation records, so it counts as "thin" per the packet's own framing of
// this interim state.
const (
	thinMarkerFile     = "registry.go"
	thinMarkerFunction = "DefaultRegistry()"
)

// isThinCitation reports whether c is packet 1.2's interim
// generated-from-registry.go placeholder rather than a real fact citation.
// c.Source == "" (a citation specfmt.Validate has already rejected outright
// — see LintInvalid) is NOT considered thin by this function; it is a
// distinct, more severe condition this package never sees reach a
// specfmt.MergedSet.Commands entry at all.
func isThinCitation(c specfmt.Citation) bool {
	return c.Source != "" && strings.Contains(c.Source, thinMarkerFile) && strings.Contains(c.Source, thinMarkerFunction)
}

// citationFinding returns a Finding for a thin citation at field within
// specName, or nil if c is not thin. Always HARD, regardless of builtin —
// docket tc-o14i5.4's Phase 3 packet 1 (tc-o14i5.4.3) back-filled real
// citations for all 46 embedded built-in specs, so the WARN staging this
// function used to apply to the embedded/builtin layer (a documented
// interim allowance for packet 1.2's mechanically-marshalled, not-yet-cited
// specs) is resolved: a thin citation is now unconditionally a real-content
// gap, never an expected staging artifact.
func citationFinding(specName, field string, c specfmt.Citation, builtin bool) *Finding {
	if !isThinCitation(c) {
		return nil
	}
	return &Finding{
		Severity: SeverityHard,
		Check:    CheckCitationPresence,
		Spec:     specName,
		Field:    field,
		Detail:   fmt.Sprintf("thin citation (%q) — interim, generated-from-registry.go provenance, not a real fact citation", c.Source),
	}
}

// dangerPatterns are the P13 danger-shaped flag name patterns (design:
// Phase 1 — Specs as data (L), item 3; P13). Patterns use filepath.Match
// glob syntax ("*" matches any run of characters — flag spellings never
// contain '/', so filepath.Match's path-separator handling never applies).
// "@file values" from the design list is NOT a name pattern — see doc.go;
// it is handled generically by the same "is this role an effect role" test
// applied to every value-taking danger flag (a role of
// cmddesc.KindDataOrAtFile already counts as an effect role below).
var dangerPatterns = []string{
	"-o",
	"--output*",
	"--exec*",
	"-c",
	"--config*",
	"--command",
	"-e",
	"--*-program",
	"--*-hook*",
	"--receive-pack",
	"--upload-pack",
	"--prune",
	"--mirror",
	"--all",
	"--delete",
	"-f",
	"--force*",
	"-r",
	"-R",
	"--recursive",
}

// isDangerShaped reports whether flagName matches one of dangerPatterns.
func isDangerShaped(flagName string) bool {
	for _, p := range dangerPatterns {
		if ok, _ := filepath.Match(p, flagName); ok {
			return true
		}
	}
	return false
}

// dangerFlagJustificationKey names the OPTIONAL per-flag entry a spec author
// MAY add to CommandSpecV1.Citations to justify a specific danger-shaped
// flag's non-effect role — bug tc-6v2dm: dangerPatterns matches on bare
// SPELLING only, with no awareness of which command a flag belongs to, so a
// single-letter spelling like "-o"/"-c"/"-e"/"-f"/"-r"/"-R" (and even a
// longer one like "--output") routinely collides with a flag that means
// something else entirely in a given command (xargs -e is an EOF-string
// delimiter, not "exec"; kubectl -o/--output is an output FORMAT string, not
// a file write). specfmt.Validate places no constraint on which keys a spec
// author puts in Citations beyond the handful it itself requires (see
// validateCommand), so this is a plain additive convention, not a wire-
// format change: "dangerFlagInert:" + the exact flag spelling.
//
// This mirrors unknownFlagInertFindings' own escape hatch (a real, non-thin
// citation is trusted) but is DELIBERATELY separate from that flag's own
// mandatory Citation field: that field only attests "this is the flag's
// arity/role", not "I reviewed this specific role against the danger-shaped
// pattern it happens to match and confirm it carries no effect" — see
// dangerFlagFindings' own doc comment for why conflating the two would let
// an uncritiqued citation silently launder a genuine mis-model.
//
// Unlike UnknownFlagInert (doc.go item 3, unconditionally forbidden in a
// skill-generated spec per P13), this escape hatch is NOT restricted by
// IsSkillGenerated: UnknownFlagInert blanket-trusts EVERY unknown flag a
// command might ever be given, which a skill could hallucinate broadly,
// whereas this hatch only trusts ONE already-cited, already-named flag's
// role assignment — exactly the real --help/man-evidence citation
// methodology ceta-spec-gen's own SKILL.md (tc-o14i5.4.1) already requires,
// which is precisely what this check should be trusting.
func dangerFlagJustificationKey(flagName string) string {
	return "dangerFlagInert:" + flagName
}

// dangerFlagJustified reports whether c's Citations carries a real
// (non-thin) justification for flagName's danger-shaped role — see
// dangerFlagJustificationKey.
func dangerFlagJustified(c specfmt.CommandSpecV1, flagName string) bool {
	cite, ok := c.Citations[dangerFlagJustificationKey(flagName)]
	return ok && !isThinCitation(cite)
}

// Wire spellings mirrored from specfmt/convert.go's unexported constants —
// see that file's own doc comment: "this package names its own wire
// spellings ... documented here as the single source of truth for both
// directions." specfmt exports no Go constants for them, so a consumer
// outside that package names its own copy; a drift here would be caught by
// a real embedded spec exercising an arity this package's tests do not
// recognize.
const (
	wireArityNone        = "none"
	wireUnknownFlagInert = "inert"
	wireRoleLiteral      = "literal"
	wireRoleUnmodeled    = "unmodeled"
)

// isEffectRole reports whether kind denotes a role the generic interpreter
// (internal/cmddesc) actually treats as producing an effect, as opposed to
// KindLiteral (deliberately inert) or KindUnmodeled (deliberately left
// unclassified, which makes the interpretation fail closed rather than
// approve — see cmddesc.KindUnmodeled's own doc comment). An empty Kind
// (the zero value some FlagSpecV1.Operand entries carry when a flag genuinely
// has no operand, e.g. ArityNone) is likewise not an effect role.
func isEffectRole(kind string) bool {
	switch kind {
	case "", wireRoleLiteral, wireRoleUnmodeled:
		return false
	default:
		return true
	}
}

// flagParticipatesElsewhere reports whether flagName conditions any part of
// c's OWN positional layout or implicit effects (RestOverride.Flags,
// LeadingSkippedByFlags, TrailingSkippedByFlags, or any ImplicitEffect's
// WhenFlags) — the ways a boolean (ArityNone) flag can carry an effect
// without an operand role of its own.
func flagParticipatesElsewhere(flagName string, c specfmt.CommandSpecV1) bool {
	contains := func(names []string) bool {
		for _, n := range names {
			if n == flagName {
				return true
			}
		}
		return false
	}
	if contains(c.Positionals.RestOverride.Flags) {
		return true
	}
	if contains(c.Positionals.LeadingSkippedByFlags) {
		return true
	}
	if contains(c.Positionals.TrailingSkippedByFlags) {
		return true
	}
	for _, e := range c.ImplicitEffects {
		if contains(e.WhenFlags) {
			return true
		}
	}
	return false
}

// dangerFlagFindings runs check 2 (danger-shaped-flag role) over c's own
// flags (not its subcommands' — LintCommand recurses separately). Skipped
// entirely by the caller when builtin is true (doc.go's BUILTIN SCOPING).
//
// A flag matching dangerFlagJustified (tc-6v2dm) is skipped outright,
// regardless of arity/role — the spec author has already recorded real
// evidence that THIS flag's role is correctly, deliberately non-effect
// despite its dangerous-looking spelling, which is exactly the case
// dangerPatterns' pure name-glob match cannot itself distinguish.
func dangerFlagFindings(specName string, c specfmt.CommandSpecV1) []Finding {
	var findings []Finding
	// Deterministic order.
	names := make([]string, 0, len(c.Flags))
	for name := range c.Flags {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !isDangerShaped(name) {
			continue
		}
		if dangerFlagJustified(c, name) {
			continue
		}
		f := c.Flags[name]
		if f.Arity == wireArityNone {
			if f.Transform.Kind != "none" && f.Transform.Kind != "" {
				continue
			}
			if flagParticipatesElsewhere(name, c) {
				continue
			}
			findings = append(findings, Finding{
				Severity: SeverityHard,
				Check:    CheckDangerFlagRole,
				Spec:     specName,
				Field:    "flag " + name,
				Detail:   "danger-shaped flag name has no transform and does not condition any positional/implicit-effect elsewhere — role is not an effect role",
			})
			continue
		}
		// Value-taking flag: every operand slot must be an effect role.
		roles := f.Operands
		if len(roles) == 0 {
			roles = []specfmt.OperandRoleV1{f.Operand}
		}
		for _, r := range roles {
			if !isEffectRole(r.Kind) {
				findings = append(findings, Finding{
					Severity: SeverityHard,
					Check:    CheckDangerFlagRole,
					Spec:     specName,
					Field:    "flag " + name,
					Detail:   fmt.Sprintf("danger-shaped flag's operand role %q is not an effect role", r.Kind),
				})
				break
			}
		}
	}
	return findings
}

// IsSkillGenerated reports whether specName was produced by a spec-
// generation skill (P13: UnknownFlagInert is unconditionally forbidden,
// no justification accepted, in a skill-generated spec). Phase 1 has no
// skill-generated specs (that is Phase 3's scope) — this is a documented
// no-op stub, always false, per the packet's own instruction to wire this
// branch as "a no-op/false condition for now" rather than leave it silently
// unimplemented.
func IsSkillGenerated(specName string) bool {
	return false
}

// unknownFlagInertFindings runs check 3 (UnknownFlagInert justification)
// over c itself (not its subcommands). Skipped entirely by the caller when
// builtin is true (doc.go's BUILTIN SCOPING).
func unknownFlagInertFindings(specName string, c specfmt.CommandSpecV1) []Finding {
	if c.UnknownFlag != wireUnknownFlagInert {
		return nil
	}
	if IsSkillGenerated(specName) {
		return []Finding{{
			Severity: SeverityHard,
			Check:    CheckUnknownFlagInert,
			Spec:     specName,
			Detail:   "UnknownFlagInert is unconditionally forbidden in skill-generated specs",
		}}
	}
	cite, ok := c.Citations["unknownFlag"]
	if !ok || isThinCitation(cite) {
		return []Finding{{
			Severity: SeverityHard,
			Check:    CheckUnknownFlagInert,
			Spec:     specName,
			Field:    "unknownFlag",
			Detail:   "hand-written UnknownFlagInert use needs a real (non-thin) justification via its citation",
		}}
	}
	return nil
}

// LintCommand runs every per-fact check (1, and — when !builtin — 2 and 3)
// over c and its subcommands, recursively. specName is the display name for
// c itself (the top-level command's Name, or "<parent>/<sub>" for a
// subcommand — mirroring specfmt/validate.go's own convention).
func LintCommand(specName string, c specfmt.CommandSpecV1, builtin bool) []Finding {
	var findings []Finding

	addCitation := func(field string, cite specfmt.Citation) {
		if f := citationFinding(specName, field, cite, builtin); f != nil {
			findings = append(findings, *f)
		}
	}

	citationKeys := make([]string, 0, len(c.Citations))
	for k := range c.Citations {
		citationKeys = append(citationKeys, k)
	}
	sort.Strings(citationKeys)
	for _, k := range citationKeys {
		addCitation(k, c.Citations[k])
	}
	addCitation("positionals", c.Positionals.Citation)

	flagNames := make([]string, 0, len(c.Flags))
	for name := range c.Flags {
		flagNames = append(flagNames, name)
	}
	sort.Strings(flagNames)
	for _, name := range flagNames {
		addCitation("flag "+name, c.Flags[name].Citation)
	}

	for i, e := range c.ImplicitEffects {
		addCitation(fmt.Sprintf("implicitEffects[%d]", i), e.Citation)
	}

	if !builtin {
		findings = append(findings, dangerFlagFindings(specName, c)...)
		findings = append(findings, unknownFlagInertFindings(specName, c)...)
	}

	subNames := make([]string, 0, len(c.Subcommands))
	for name := range c.Subcommands {
		subNames = append(subNames, name)
	}
	sort.Strings(subNames)
	for _, name := range subNames {
		findings = append(findings, LintCommand(specName+"/"+name, c.Subcommands[name], builtin)...)
	}

	return findings
}

// LintConflicts runs check 4 (overrides-conflict) over conflicts — every
// entry is HARD regardless of builtin, per specfmt/repository.go's own doc
// comment: "packet 1.3's linter is what turns a Conflict into a hard
// failure."
func LintConflicts(conflicts []specfmt.Conflict) []Finding {
	findings := make([]Finding, 0, len(conflicts))
	for _, c := range conflicts {
		findings = append(findings, Finding{
			Severity: SeverityHard,
			Check:    CheckOverridesConflict,
			Spec:     string(c.Kind) + "/" + c.Name,
			Detail: fmt.Sprintf("%s layer spec (%s) replaces %s layer spec (%s) without declaring \"overrides\"",
				c.WinningLayer, c.WinningPath, c.LosingLayer, c.LosingPath),
		})
	}
	return findings
}

// LintInvalid surfaces every specfmt.InvalidSpec (a spec specfmt.Validate
// rejected — including, per that package's own unconditional rule, a
// literally empty per-fact citation) as a WARN Finding. See doc.go's
// "Division of labor with specfmt.Validate."
//
// This is WARN, not HARD, deliberately: the packet's own Contract frames
// this whole check as optional ("this packet's linter MAY surface the
// loader's own rejection as a lint finding for a better error message, but
// the authoritative enforcement point is packet 1.1's loader, not this
// linter"). specfmt.Repository.Load has ALREADY performed the actual,
// authoritative enforcement by excluding the spec from MergedSet.Commands
// entirely — this Finding is a diagnostic breadcrumb explaining WHY a
// command silently vanished, not a second gate. Concretely, two of the 45
// embedded built-ins (bash, sh) are invalid TODAY via a pre-existing,
// deliberate, already-documented gap unrelated to this packet: bashSchema's
// Program("shell-file") role names a dialect cmddesc/registry.go
// intentionally never registers for real (see
// embeddedspecs/dialects_test.go's own doc comment — a test-only
// registration exists solely so packet 1.2's own TestRoundTrip can reach
// its "0 diffs" bar; packet 1.2's writeup explicitly says this collision
// with specfmt.Validate's fail-closed dialect check "was verified,
// empirically... not assumed" and is not reconciled). Making that a HARD
// finding here would fail this packet's own "flake check passes against
// packet 1.2's embedded specs today" validation bar over a gap this packet
// neither introduced nor is scoped to fix (internal/specfmt and
// cmddesc/registry.go are both out of scope — see this package's doc.go).
func LintInvalid(invalid []specfmt.InvalidSpec) []Finding {
	findings := make([]Finding, 0, len(invalid))
	for _, inv := range invalid {
		findings = append(findings, Finding{
			Severity: SeverityWarn,
			Check:    CheckInvalidSpec,
			Spec:     fmt.Sprintf("%s:%s", inv.Layer, inv.Path),
			Detail:   inv.Err.Error(),
		})
	}
	return findings
}

// Report is the full result of linting a Repository or a set of spec
// files.
type Report struct {
	Findings []Finding
}

// HasHard reports whether r contains at least one SeverityHard finding.
func (r *Report) HasHard() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityHard {
			return true
		}
	}
	return false
}

// LintRepository loads repo and runs every check over the result. builtin
// applies uniformly to every spec repo yields — see doc.go's BUILTIN
// SCOPING for why a single bool is the right granularity for this
// package's primary (CLI) caller, which only ever lints a Repository
// consisting entirely of one kind of layer at a time.
func LintRepository(repo *specfmt.Repository, builtin bool) (*Report, error) {
	merged, err := repo.Load()
	if err != nil {
		return nil, err
	}

	var findings []Finding
	names := make([]string, 0, len(merged.Commands))
	for name := range merged.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		findings = append(findings, LintCommand(name, merged.Commands[name], builtin)...)
	}
	findings = append(findings, LintConflicts(merged.Conflicts)...)
	findings = append(findings, LintInvalid(merged.Invalid)...)

	return &Report{Findings: findings}, nil
}

// LintSpec runs every check over a single, already-parsed specfmt.Spec —
// the path used for a bare spec-file argument (outside any Repository
// layer, so builtin is always false: see doc.go's BUILTIN SCOPING). Non-
// KindCommand specs (KindPath/KindTarget) produce no findings; this
// packet's checks are all command-spec checks.
func LintSpec(path string, s specfmt.Spec) []Finding {
	if s.Kind != specfmt.KindCommand || s.Command == nil {
		return nil
	}
	return LintCommand(s.Name, *s.Command, false)
}
