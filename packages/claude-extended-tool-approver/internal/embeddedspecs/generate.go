package embeddedspecs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

// sourceCitation is the interim, provenance-truthful citation every
// marshalled fact carries in this packet's own scope (Phase 1 packet 1.2,
// tc-o14i5.2.2). Phase 3 back-fills real per-fact help/man/source-line
// citations; this packet's own Contract text explicitly allows the interim
// value: "where a fact currently has no independently-verifiable
// help/man/source-line citation beyond 'this is what registry.go already
// encodes,' this packet MAY use that provenance itself ... as the interim
// citation value; it is not a placeholder/TBD marker."
func sourceCitation(commandName string) specfmt.Citation {
	return specfmt.Citation{Source: fmt.Sprintf("internal/cmddesc/registry.go: cmddesc.DefaultRegistry()[%q]", commandName)}
}

// isThinCitation mirrors internal/speclint's thin-citation test: the interim
// placeholder shape sourceCitation stamps. Kept as a local copy (speclint
// imports this package's neighbours, not the reverse) — the two MUST agree.
func isThinCitation(c specfmt.Citation) bool {
	return c.Source != "" && strings.Contains(c.Source, "registry.go") && strings.Contains(c.Source, "DefaultRegistry()")
}

// LoadSpecs reads every KindCommand spec JSON file directly under dir (the
// shape WriteSpecs writes), keyed by command name. Non-spec siblings such as
// help-hashes*.json decode to a zero-value Spec and are skipped. A missing
// dir is an empty result, not an error: the first generation has no prior.
func LoadSpecs(dir string) (map[string]specfmt.Spec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]specfmt.Spec{}, nil
		}
		return nil, fmt.Errorf("embeddedspecs: reading %s: %w", dir, err)
	}
	out := map[string]specfmt.Spec{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("embeddedspecs: read %s: %w", e.Name(), err)
		}
		var sp specfmt.Spec
		if err := json.Unmarshal(data, &sp); err != nil {
			return nil, fmt.Errorf("embeddedspecs: decode %s: %w", e.Name(), err)
		}
		if sp.Kind != specfmt.KindCommand || sp.Command == nil {
			continue
		}
		out[sp.Command.Name] = sp
	}
	return out, nil
}

// BuildSpecsPreserving is BuildSpecs plus citation preservation: every fact
// whose entry in prior already carries a REAL (non-thin) citation at the same
// address keeps it, so regenerating after a registry change re-marshals the
// schema facts without discarding the per-fact help/man/source citations a
// back-fill (tc-o14i5.4.3) or an author (the ceta-spec-gen skill) recorded. A
// fact with no prior real citation gets the interim thin one and is then a
// HARD lint finding until an author cites it — which is the point: a new fact
// cannot slip in uncited.
func BuildSpecsPreserving(reg cmddesc.Registry, prior map[string]specfmt.Spec) (map[string]specfmt.Spec, error) {
	out, err := BuildSpecs(reg)
	if err != nil {
		return nil, err
	}
	for name, sp := range out {
		old, ok := prior[name]
		if !ok || old.Command == nil || sp.Command == nil {
			continue
		}
		overlayCitations(sp.Command, *old.Command)
	}
	return out, nil
}

// overlayCitations copies src's real citations onto dst wherever the same
// address exists in both (command-level keys, flags by spelling, positionals,
// implicit effects matched by role/operation/target, subcommands recursively).
func overlayCitations(dst *specfmt.CommandSpecV1, src specfmt.CommandSpecV1) {
	// Every real command-level citation is kept — including keys the marshaller
	// itself never emits (speclint's `dangerFlagInert:<flag>` justifications
	// are hand-authored, tc-6v2dm) — so a regeneration can never drop one.
	if dst.Citations == nil && len(src.Citations) > 0 {
		dst.Citations = map[string]specfmt.Citation{}
	}
	for k, c := range src.Citations {
		if !isThinCitation(c) && c.Source != "" {
			dst.Citations[k] = c
		}
	}
	for name, f := range dst.Flags {
		if sf, ok := src.Flags[name]; ok && !isThinCitation(sf.Citation) && sf.Citation.Source != "" {
			f.Citation = sf.Citation
			dst.Flags[name] = f
		}
	}
	if !isThinCitation(src.Positionals.Citation) && src.Positionals.Citation.Source != "" {
		dst.Positionals.Citation = src.Positionals.Citation
	}
	for i := range dst.ImplicitEffects {
		for _, se := range src.ImplicitEffects {
			if se.Role == dst.ImplicitEffects[i].Role && se.Target == dst.ImplicitEffects[i].Target && !isThinCitation(se.Citation) && se.Citation.Source != "" {
				dst.ImplicitEffects[i].Citation = se.Citation
				break
			}
		}
	}
	for name, sub := range dst.Subcommands {
		if ssub, ok := src.Subcommands[name]; ok {
			overlayCitations(&sub, ssub)
			dst.Subcommands[name] = sub
		}
	}
}

// BuildSpecs marshals every entry of reg (keyed by registry name, so all 45
// of DefaultRegistry()'s names -- 42 schema values plus the 3 renamed(...)
// aliases sh/[/gawk -- each become their own spec, since renamed(...)
// returns a distinct CommandSchema value with only its Name field changed)
// into a specfmt.Spec of KindCommand, with every fact-bearing element's
// Citation populated per citationsFor.
func BuildSpecs(reg cmddesc.Registry) (map[string]specfmt.Spec, error) {
	names := reg.Names()
	out := make(map[string]specfmt.Spec, len(names))
	for _, name := range names {
		schema, ok := reg.Lookup(name)
		if !ok {
			return nil, fmt.Errorf("embeddedspecs: registry reported name %q via Names() but Lookup failed", name)
		}
		citations := citationsFor(schema, "", sourceCitation(name))
		v1, err := specfmt.FromSchema(schema, citations)
		if err != nil {
			return nil, fmt.Errorf("embeddedspecs: FromSchema(%q): %w", name, err)
		}
		out[name] = specfmt.Spec{
			Version: specfmt.FormatVersion,
			Kind:    specfmt.KindCommand,
			Name:    name,
			Command: &v1,
		}
	}
	return out, nil
}

// citationsFor walks schema the same way specfmt.FromSchema's own (exported,
// public-contract) doc comment documents its citation-key scheme: the
// citationKey* constants for the schema's own top-level scalar facts
// (spelled out literally below, since they are unexported in specfmt but
// their SPELLING is part of FromSchema's documented public contract, not an
// internal peek), "flag:<spelling>" per Flags entry, "positionals" for the
// Positionals block, "implicitEffect:<index>" per ImplicitEffects entry, and
// "subcommand:<name>:<key>" recursively. Every key gets the SAME interim
// citation (cite) -- see sourceCitation's doc comment for why a single
// shared value is this packet's own scope, not a per-fact one.
func citationsFor(schema cmddesc.CommandSchema, prefix string, cite specfmt.Citation) map[string]specfmt.Citation {
	out := map[string]specfmt.Citation{
		prefix + "provenance":  cite,
		prefix + "stdin":       cite,
		prefix + "stdout":      cite,
		prefix + "unknownFlag": cite,
		prefix + "positionals": cite,
	}
	if schema.Interpreter != "" {
		out[prefix+"interpreter"] = cite
	}
	for name := range schema.Flags {
		out[prefix+"flag:"+name] = cite
	}
	for i := range schema.ImplicitEffects {
		out[fmt.Sprintf("%simplicitEffect:%d", prefix, i)] = cite
	}
	for name, sub := range schema.Subcommands {
		for k, v := range citationsFor(sub, fmt.Sprintf("%ssubcommand:%s:", prefix, name), cite) {
			out[k] = v
		}
	}
	return out
}

// filenameFor returns a filesystem-safe filename (no directory) for a
// registry name. Every name but one is already a safe bare filename; "["
// (the test/[ alias, cmddesc/registry.go's renamed(testSchema, "[")) is
// spelled out instead of written as a literal "[.json" so a directory
// listing of data/ does not read as truncated/broken. The Repository's own
// merge key is Spec.Name (see specfmt/repository.go's key type), never the
// filename, so this mapping is purely cosmetic and carries no behavior.
func filenameFor(name string) string {
	if name == "[" {
		return "bracket-test.json"
	}
	return name + ".json"
}

// WriteSpecs (re)writes dir with one indented JSON file per spec, removing
// any STALE SPEC file first (a command spec file whose name is no longer in
// specs) so a future DefaultRegistry() rename or removal does not leave an
// orphaned data/*.json behind (the Repository would otherwise still load and
// merge it as a phantom entry). Non-spec siblings in dir — help-hashes.json
// and its per-GOOS overlays, written by `spec-drift-check --record` — are NEVER
// removed: they are committed data this generator does not own (an earlier
// revision removed the whole directory, which silently deleted them). Run this
// repo's formatter/pre-commit hooks over dir afterward before committing, the
// same convention cmd/legacyextract's own sibling generator documents.
func WriteSpecs(dir string, specs map[string]specfmt.Spec) error {
	existing, err := LoadSpecs(dir)
	if err != nil {
		return err
	}
	for name := range existing {
		if _, keep := specs[name]; keep {
			continue
		}
		stale := filepath.Join(dir, filenameFor(name))
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("embeddedspecs: removing stale %s: %w", stale, err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("embeddedspecs: creating %s: %w", dir, err)
	}
	for name, spec := range specs {
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return fmt.Errorf("embeddedspecs: marshal %q: %w", name, err)
		}
		data = append(data, '\n')
		path := filepath.Join(dir, filenameFor(name))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("embeddedspecs: write %s: %w", path, err)
		}
	}
	return nil
}
