package embeddedspecs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
// any stale files first so a future DefaultRegistry() rename or removal
// does not leave an orphaned data/*.json behind (the Repository would
// otherwise still load and merge it as a phantom entry). Run this repo's
// formatter/pre-commit hooks over dir afterward before committing, the same
// convention cmd/legacyextract's own sibling generator documents.
func WriteSpecs(dir string, specs map[string]specfmt.Spec) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("embeddedspecs: removing stale %s: %w", dir, err)
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
