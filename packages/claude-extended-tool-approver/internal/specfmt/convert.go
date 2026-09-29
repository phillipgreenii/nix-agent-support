package specfmt

import (
	"fmt"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
)

// roleKindByName is the reverse of cmddesc.RoleKind.String(), built BY
// CALLING that method over every known constant so the two directions can
// never drift apart (there is exactly one place, cmddesc/schema.go, that
// spells "path-read" etc.). KindLiteral..KindKeyMaterial are contiguous
// iota values in cmddesc (verified 2026-09-29 against schema.go), so the
// range loop below visits every one of them.
var roleKindByName = func() map[string]cmddesc.RoleKind {
	m := make(map[string]cmddesc.RoleKind)
	for k := cmddesc.KindLiteral; k <= cmddesc.KindKeyMaterial; k++ {
		m[k.String()] = k
	}
	return m
}()

func roleKindFromString(s string) (cmddesc.RoleKind, bool) {
	k, ok := roleKindByName[s]
	return k, ok
}

// transformKindByName is roleKindByName's TransformKind analogue — same
// rationale, built from cmddesc.TransformKind.String().
// TransformNone..TransformDeleteRef are contiguous iota values in cmddesc.
var transformKindByName = func() map[string]cmddesc.TransformKind {
	m := make(map[string]cmddesc.TransformKind)
	for k := cmddesc.TransformNone; k <= cmddesc.TransformDeleteRef; k++ {
		m[k.String()] = k
	}
	return m
}()

func transformKindFromString(s string) (cmddesc.TransformKind, bool) {
	k, ok := transformKindByName[s]
	return k, ok
}

// Arity, StdinSpec, StdoutKind and UnknownFlagPolicy have no String() method
// in cmddesc, so this package names its own wire spellings for them —
// documented here as the single source of truth for both directions.
const (
	arityNone          = "none"
	arityOne           = "one"
	arityOptionalGlued = "optional-glued"
	arityN             = "n"
	stdinNever         = "never"
	stdinAlways        = "always"
	stdinWhenNoPath    = "when-no-path-operands"
	stdoutNone         = "none"
	stdoutContent      = "content"
	stdoutMetadata     = "metadata"
	unknownFlagInsuf   = "insufficient"
	unknownFlagInert   = "inert"
)

func arityToString(a cmddesc.Arity) (string, error) {
	switch a {
	case cmddesc.ArityNone:
		return arityNone, nil
	case cmddesc.ArityOne:
		return arityOne, nil
	case cmddesc.ArityOptionalGlued:
		return arityOptionalGlued, nil
	case cmddesc.ArityN:
		return arityN, nil
	default:
		return "", fmt.Errorf("specfmt: unknown cmddesc.Arity %d", a)
	}
}

func arityFromString(s string) (cmddesc.Arity, error) {
	switch s {
	case arityNone:
		return cmddesc.ArityNone, nil
	case arityOne:
		return cmddesc.ArityOne, nil
	case arityOptionalGlued:
		return cmddesc.ArityOptionalGlued, nil
	case arityN:
		return cmddesc.ArityN, nil
	default:
		return 0, fmt.Errorf("specfmt: unknown arity spelling %q", s)
	}
}

func stdinToString(s cmddesc.StdinSpec) (string, error) {
	switch s {
	case cmddesc.StdinNever:
		return stdinNever, nil
	case cmddesc.StdinAlways:
		return stdinAlways, nil
	case cmddesc.StdinWhenNoPathOperands:
		return stdinWhenNoPath, nil
	default:
		return "", fmt.Errorf("specfmt: unknown cmddesc.StdinSpec %d", s)
	}
}

func stdinFromString(v string) (cmddesc.StdinSpec, error) {
	switch v {
	case stdinNever:
		return cmddesc.StdinNever, nil
	case stdinAlways:
		return cmddesc.StdinAlways, nil
	case stdinWhenNoPath:
		return cmddesc.StdinWhenNoPathOperands, nil
	default:
		return 0, fmt.Errorf("specfmt: unknown stdin spelling %q", v)
	}
}

func stdoutToString(s cmddesc.StdoutKind) (string, error) {
	switch s {
	case cmddesc.StdoutNone:
		return stdoutNone, nil
	case cmddesc.StdoutContent:
		return stdoutContent, nil
	case cmddesc.StdoutMetadata:
		return stdoutMetadata, nil
	default:
		return "", fmt.Errorf("specfmt: unknown cmddesc.StdoutKind %d", s)
	}
}

func stdoutFromString(v string) (cmddesc.StdoutKind, error) {
	switch v {
	case stdoutNone:
		return cmddesc.StdoutNone, nil
	case stdoutContent:
		return cmddesc.StdoutContent, nil
	case stdoutMetadata:
		return cmddesc.StdoutMetadata, nil
	default:
		return 0, fmt.Errorf("specfmt: unknown stdout spelling %q", v)
	}
}

func unknownFlagToString(p cmddesc.UnknownFlagPolicy) (string, error) {
	switch p {
	case cmddesc.UnknownFlagInsufficient:
		return unknownFlagInsuf, nil
	case cmddesc.UnknownFlagInert:
		return unknownFlagInert, nil
	default:
		return "", fmt.Errorf("specfmt: unknown cmddesc.UnknownFlagPolicy %d", p)
	}
}

func unknownFlagFromString(v string) (cmddesc.UnknownFlagPolicy, error) {
	switch v {
	case unknownFlagInsuf:
		return cmddesc.UnknownFlagInsufficient, nil
	case unknownFlagInert:
		return cmddesc.UnknownFlagInert, nil
	default:
		return 0, fmt.Errorf("specfmt: unknown unknownFlag spelling %q", v)
	}
}

func roleToV1(r cmddesc.OperandRole) OperandRoleV1 {
	return OperandRoleV1{Kind: r.Kind.String(), Dialect: r.Dialect, Operation: r.Operation}
}

func roleFromV1(r OperandRoleV1) (cmddesc.OperandRole, error) {
	k, ok := roleKindFromString(r.Kind)
	if !ok {
		return cmddesc.OperandRole{}, fmt.Errorf("specfmt: unknown role kind %q", r.Kind)
	}
	return cmddesc.OperandRole{Kind: k, Dialect: r.Dialect, Operation: r.Operation}, nil
}

func rolesToV1(rs []cmddesc.OperandRole) []OperandRoleV1 {
	if rs == nil {
		return nil
	}
	out := make([]OperandRoleV1, len(rs))
	for i, r := range rs {
		out[i] = roleToV1(r)
	}
	return out
}

func rolesFromV1(rs []OperandRoleV1) ([]cmddesc.OperandRole, error) {
	if rs == nil {
		return nil, nil
	}
	out := make([]cmddesc.OperandRole, len(rs))
	for i, r := range rs {
		role, err := roleFromV1(r)
		if err != nil {
			return nil, err
		}
		out[i] = role
	}
	return out, nil
}

func flagToV1(f cmddesc.FlagSpec, citation Citation) (FlagSpecV1, error) {
	arity, err := arityToString(f.Arity)
	if err != nil {
		return FlagSpecV1{}, err
	}
	return FlagSpecV1{
		Arity:     arity,
		Operand:   roleToV1(f.Operand),
		Operands:  rolesToV1(f.Operands),
		Transform: EffectTransformV1{Kind: f.Transform.Kind.String()},
		Citation:  citation,
	}, nil
}

func flagFromV1(f FlagSpecV1) (cmddesc.FlagSpec, error) {
	arity, err := arityFromString(f.Arity)
	if err != nil {
		return cmddesc.FlagSpec{}, err
	}
	operand, err := roleFromV1(f.Operand)
	if err != nil {
		return cmddesc.FlagSpec{}, err
	}
	operands, err := rolesFromV1(f.Operands)
	if err != nil {
		return cmddesc.FlagSpec{}, err
	}
	tk, ok := transformKindFromString(f.Transform.Kind)
	if !ok {
		return cmddesc.FlagSpec{}, fmt.Errorf("specfmt: unknown transform kind %q", f.Transform.Kind)
	}
	return cmddesc.FlagSpec{
		Arity:     arity,
		Operand:   operand,
		Operands:  operands,
		Transform: cmddesc.EffectTransform{Kind: tk},
	}, nil
}

func positionalsToV1(p cmddesc.PositionalSpec, citation Citation) PositionalSpecV1 {
	return PositionalSpecV1{
		Leading:               rolesToV1(p.Leading),
		LeadingSkippedByFlags: p.LeadingSkippedByFlags,
		LeadingOptional:       p.LeadingOptional,
		Rest:                  roleToV1(p.Rest),
		RestOverride: RestOverrideV1{
			Flags: p.RestOverride.Flags,
			Role:  roleToV1(p.RestOverride.Role),
		},
		MinRest:                p.MinRest,
		Trailing:               rolesToV1(p.Trailing),
		TrailingSkippedByFlags: p.TrailingSkippedByFlags,
		StdinToken:             p.StdinToken,
		Citation:               citation,
	}
}

func positionalsFromV1(p PositionalSpecV1) (cmddesc.PositionalSpec, error) {
	leading, err := rolesFromV1(p.Leading)
	if err != nil {
		return cmddesc.PositionalSpec{}, err
	}
	rest, err := roleFromV1(p.Rest)
	if err != nil {
		return cmddesc.PositionalSpec{}, err
	}
	overrideRole, err := roleFromV1(p.RestOverride.Role)
	if err != nil {
		return cmddesc.PositionalSpec{}, err
	}
	trailing, err := rolesFromV1(p.Trailing)
	if err != nil {
		return cmddesc.PositionalSpec{}, err
	}
	return cmddesc.PositionalSpec{
		Leading:               leading,
		LeadingSkippedByFlags: p.LeadingSkippedByFlags,
		LeadingOptional:       p.LeadingOptional,
		Rest:                  rest,
		RestOverride: cmddesc.RestOverride{
			Flags: p.RestOverride.Flags,
			Role:  overrideRole,
		},
		MinRest:                p.MinRest,
		Trailing:               trailing,
		TrailingSkippedByFlags: p.TrailingSkippedByFlags,
		StdinToken:             p.StdinToken,
	}, nil
}

func implicitToV1(e cmddesc.ImplicitEffect, citation Citation) ImplicitEffectV1 {
	return ImplicitEffectV1{
		Role:                  roleToV1(e.Role),
		Target:                e.Target,
		Dynamic:               e.Dynamic,
		WhenNoPositionals:     e.WhenNoPositionals,
		WhenNoRestPositionals: e.WhenNoRestPositionals,
		WhenFlags:             e.WhenFlags,
		RemoteFamily:          e.RemoteFamily,
		Citation:              citation,
	}
}

func implicitFromV1(e ImplicitEffectV1) (cmddesc.ImplicitEffect, error) {
	role, err := roleFromV1(e.Role)
	if err != nil {
		return cmddesc.ImplicitEffect{}, err
	}
	return cmddesc.ImplicitEffect{
		Role:                  role,
		Target:                e.Target,
		Dynamic:               e.Dynamic,
		WhenNoPositionals:     e.WhenNoPositionals,
		WhenNoRestPositionals: e.WhenNoRestPositionals,
		WhenFlags:             e.WhenFlags,
		RemoteFamily:          e.RemoteFamily,
	}, nil
}

// FromSchema converts a cmddesc.CommandSchema into its v1 wire form.
// citations supplies the mandatory per-fact citation for every fact this
// schema carries, keyed as follows: the citationKey* constants for the
// schema's own top-level scalar facts, "flag:<spelling>" for each Flags
// entry, "positionals" for the Positionals block, and
// "implicitEffect:<index>" for each ImplicitEffects entry (index in the
// slice's own order) — a caller marshalling a hand-authored schema (as
// opposed to round-tripping an already-cited CommandSpecV1) supplies these;
// a missing key produces an empty Citation, which Validate then reports.
// Subcommands are converted recursively; a subcommand's own citations are
// looked up under "subcommand:<name>:<key>".
func FromSchema(s cmddesc.CommandSchema, citations map[string]Citation) (CommandSpecV1, error) {
	if citations == nil {
		citations = map[string]Citation{}
	}
	return fromSchema(s, citations, "")
}

func fromSchema(s cmddesc.CommandSchema, citations map[string]Citation, prefix string) (CommandSpecV1, error) {
	stdinStr, err := stdinToString(s.Stdin)
	if err != nil {
		return CommandSpecV1{}, err
	}
	stdoutStr, err := stdoutToString(s.Stdout)
	if err != nil {
		return CommandSpecV1{}, err
	}
	unknownFlagStr, err := unknownFlagToString(s.UnknownFlag)
	if err != nil {
		return CommandSpecV1{}, err
	}

	out := CommandSpecV1{
		Name:       s.Name,
		Provenance: s.Provenance,
		Citations: map[string]Citation{
			citationKeyProvenance:  citations[prefix+citationKeyProvenance],
			citationKeyStdin:       citations[prefix+citationKeyStdin],
			citationKeyStdout:      citations[prefix+citationKeyStdout],
			citationKeyUnknownFlag: citations[prefix+citationKeyUnknownFlag],
		},
		Positionals:           positionalsToV1(s.Positionals, citations[prefix+"positionals"]),
		Stdin:                 stdinStr,
		Stdout:                stdoutStr,
		UnknownFlag:           unknownFlagStr,
		EndOfOptions:          s.EndOfOptions,
		PositionalsEndOptions: s.PositionalsEndOptions,
		Interpreter:           s.Interpreter,
		DefaultSubcommand:     s.DefaultSubcommand,
		VerbFamily:            s.VerbFamily,
		DefaultVerb:           s.DefaultVerb,
	}
	if s.Interpreter != "" {
		out.Citations[citationKeyInterpreter] = citations[prefix+citationKeyInterpreter]
	}

	if len(s.Flags) > 0 {
		out.Flags = make(map[string]FlagSpecV1, len(s.Flags))
		for name, f := range s.Flags {
			fv1, err := flagToV1(f, citations[prefix+"flag:"+name])
			if err != nil {
				return CommandSpecV1{}, fmt.Errorf("flag %q: %w", name, err)
			}
			out.Flags[name] = fv1
		}
	}

	if len(s.ImplicitEffects) > 0 {
		out.ImplicitEffects = make([]ImplicitEffectV1, len(s.ImplicitEffects))
		for i, e := range s.ImplicitEffects {
			out.ImplicitEffects[i] = implicitToV1(e, citations[fmt.Sprintf("%simplicitEffect:%d", prefix, i)])
		}
	}

	if len(s.Subcommands) > 0 {
		out.Subcommands = make(map[string]CommandSpecV1, len(s.Subcommands))
		for name, sub := range s.Subcommands {
			subV1, err := fromSchema(sub, citations, fmt.Sprintf("%ssubcommand:%s:", prefix, name))
			if err != nil {
				return CommandSpecV1{}, fmt.Errorf("subcommand %q: %w", name, err)
			}
			out.Subcommands[name] = subV1
		}
	}

	return out, nil
}

// ToSchema converts a v1 wire CommandSpecV1 back into a cmddesc.CommandSchema.
// It does not itself reject an unknown Interpreter/Dialect/VerbFamily/
// RemoteFamily — that is Validate's job (validate.go), run by the Repository
// before ToSchema is ever called on a loaded spec — but it DOES fail on a
// structurally invalid value (an unrecognised role/arity/transform/stdin/
// stdout/unknownFlag spelling), since those have no meaning to recover a
// CommandSchema from at all.
func ToSchema(c CommandSpecV1) (cmddesc.CommandSchema, error) {
	positionals, err := positionalsFromV1(c.Positionals)
	if err != nil {
		return cmddesc.CommandSchema{}, fmt.Errorf("positionals: %w", err)
	}
	stdin, err := stdinFromString(c.Stdin)
	if err != nil {
		return cmddesc.CommandSchema{}, err
	}
	stdout, err := stdoutFromString(c.Stdout)
	if err != nil {
		return cmddesc.CommandSchema{}, err
	}
	unknownFlag, err := unknownFlagFromString(c.UnknownFlag)
	if err != nil {
		return cmddesc.CommandSchema{}, err
	}

	out := cmddesc.CommandSchema{
		Name:                  c.Name,
		Provenance:            c.Provenance,
		Positionals:           positionals,
		Stdin:                 stdin,
		Stdout:                stdout,
		UnknownFlag:           unknownFlag,
		EndOfOptions:          c.EndOfOptions,
		PositionalsEndOptions: c.PositionalsEndOptions,
		Interpreter:           c.Interpreter,
		DefaultSubcommand:     c.DefaultSubcommand,
		VerbFamily:            c.VerbFamily,
		DefaultVerb:           c.DefaultVerb,
	}

	if len(c.Flags) > 0 {
		out.Flags = make(map[string]cmddesc.FlagSpec, len(c.Flags))
		for name, f := range c.Flags {
			flag, err := flagFromV1(f)
			if err != nil {
				return cmddesc.CommandSchema{}, fmt.Errorf("flag %q: %w", name, err)
			}
			out.Flags[name] = flag
		}
	}

	if len(c.ImplicitEffects) > 0 {
		out.ImplicitEffects = make([]cmddesc.ImplicitEffect, len(c.ImplicitEffects))
		for i, e := range c.ImplicitEffects {
			eff, err := implicitFromV1(e)
			if err != nil {
				return cmddesc.CommandSchema{}, fmt.Errorf("implicitEffects[%d]: %w", i, err)
			}
			out.ImplicitEffects[i] = eff
		}
	}

	if len(c.Subcommands) > 0 {
		out.Subcommands = make(map[string]cmddesc.CommandSchema, len(c.Subcommands))
		for name, sub := range c.Subcommands {
			subSchema, err := ToSchema(sub)
			if err != nil {
				return cmddesc.CommandSchema{}, fmt.Errorf("subcommand %q: %w", name, err)
			}
			out.Subcommands[name] = subSchema
		}
	}

	return out, nil
}
