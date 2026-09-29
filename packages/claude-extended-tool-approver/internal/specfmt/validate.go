package specfmt

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
)

// Validate checks one Spec for structural well-formedness and for the
// fail-closed rejections this packet's contract requires: an unknown
// Interpreter, program Dialect, VerbFamily, or RemoteFamily (P13/P3), and a
// missing per-fact Citation (P13's "no dropped facts" — a fact with no
// citation is treated the same as a dropped fact). It returns every problem
// found, joined with errors.Join, rather than stopping at the first — a
// spec author (or packet 1.3's linter) fixing a file wants the whole list at
// once, not one round trip per mistake.
//
// Validate does NOT reject an unrecognised SpecKind by itself: KindPath is a
// reserved name with no populated fields yet (see v1.go), so a Spec naming
// it with no payload is well-formed but empty. It DOES reject Kind ==
// KindCommand with a nil Command, Kind == KindTarget with a nil Target
// (payload required once a kind is claimed), and any Kind this package has
// never heard of at all.
func Validate(s Spec) error {
	var errs []error

	if s.Version != FormatVersion {
		errs = append(errs, fmt.Errorf("unsupported spec version %q (want %q)", s.Version, FormatVersion))
	}
	if s.Name == "" {
		errs = append(errs, errors.New("spec name is empty"))
	}

	switch s.Kind {
	case KindCommand:
		if s.Command == nil {
			errs = append(errs, fmt.Errorf("spec %q: kind %q requires a command payload", s.Name, s.Kind))
		} else {
			errs = append(errs, validateCommand(s.Name, *s.Command)...)
		}
	case KindTarget:
		if s.Target == nil {
			errs = append(errs, fmt.Errorf("spec %q: kind %q requires a target payload", s.Name, s.Kind))
		} else {
			errs = append(errs, validateTarget(s.Name, *s.Target)...)
		}
	case KindPath:
		// Reserved, no payload defined yet in this packet's scope.
	default:
		errs = append(errs, fmt.Errorf("spec %q: unknown spec kind %q", s.Name, s.Kind))
	}

	return errors.Join(errs...)
}

// validateTarget checks one KindTarget spec's payload (P8, docket
// tc-o14i5.3, packet tc-o14i5.3.4): TargetKind must be registered
// (IsKnownTargetKind — P3's "unknown => never approve" reach into spec
// authoring itself: an unrecognised target kind is a malformed spec, not
// silently merged), Class must be one of the two recognised values, and
// Citation is mandatory (TargetSpecV1's own doc comment).
func validateTarget(specName string, t TargetSpecV1) []error {
	var errs []error
	if !IsKnownTargetKind(t.TargetKind) {
		errs = append(errs, fmt.Errorf("spec %q target: unknown target kind %q", specName, t.TargetKind))
	}
	if t.Class != TargetClassProduction && t.Class != TargetClassTrustedDev {
		errs = append(errs, fmt.Errorf("spec %q target: unknown class %q (want %q or %q)", specName, t.Class, TargetClassProduction, TargetClassTrustedDev))
	}
	if t.Citation.empty() {
		errs = append(errs, fmt.Errorf("spec %q target: missing citation", specName))
	}
	return errs
}

func validateCommand(specName string, c CommandSpecV1) []error {
	var errs []error
	note := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("spec %q command %q: %s", specName, c.Name, fmt.Sprintf(format, args...)))
	}

	requireCitation := func(key string) {
		if c.Citations[key].empty() {
			note("missing citation for %q", key)
		}
	}
	requireCitation(citationKeyProvenance)
	requireCitation(citationKeyStdin)
	requireCitation(citationKeyStdout)
	requireCitation(citationKeyUnknownFlag)
	if c.Interpreter != "" {
		requireCitation(citationKeyInterpreter)
		if _, ok := cmddesc.LookupInterpreter(c.Interpreter); !ok {
			note("unknown interpreter %q", c.Interpreter)
		}
	}
	if c.VerbFamily != "" && !IsKnownVerbFamily(c.VerbFamily) {
		note("unknown verb family %q", c.VerbFamily)
	}
	if c.Positionals.Citation.empty() {
		note("positionals: missing citation")
	}
	validateRoleDialect(note, "positionals.leading", c.Positionals.Leading)
	validateRoleDialect(note, "positionals.rest", []OperandRoleV1{c.Positionals.Rest})
	validateRoleDialect(note, "positionals.trailing", c.Positionals.Trailing)
	validateRoleDialect(note, "positionals.restOverride", []OperandRoleV1{c.Positionals.RestOverride.Role})

	for name, f := range c.Flags {
		if f.Citation.empty() {
			note("flag %q: missing citation", name)
		}
		validateRoleDialect(note, fmt.Sprintf("flag %q operand", name), []OperandRoleV1{f.Operand})
		validateRoleDialect(note, fmt.Sprintf("flag %q operands", name), f.Operands)
	}

	for i, e := range c.ImplicitEffects {
		if e.Citation.empty() {
			note("implicitEffects[%d]: missing citation", i)
		}
		if e.RemoteFamily != "" && !IsKnownRemoteFamily(e.RemoteFamily) {
			note("implicitEffects[%d]: unknown remote family %q", i, e.RemoteFamily)
		}
		validateRoleDialect(note, fmt.Sprintf("implicitEffects[%d].role", i), []OperandRoleV1{e.Role})
	}

	for name, sub := range c.Subcommands {
		errs = append(errs, validateCommand(specName+"/"+name, sub)...)
	}

	return errs
}

// validateRoleDialect checks the Dialect named by any KindProgram role in
// roles against cmddesc.LookupDialect, appending a note for an unknown one.
// note is the caller's closure that already knows the enclosing spec/command
// name; label identifies which field within it this role came from.
func validateRoleDialect(note func(format string, args ...any), label string, roles []OperandRoleV1) {
	for _, r := range roles {
		if r.Kind != "program" || r.Dialect == "" {
			continue
		}
		if _, ok := cmddesc.LookupDialect(r.Dialect); !ok {
			note("%s: unknown dialect %q", label, r.Dialect)
		}
	}
}
