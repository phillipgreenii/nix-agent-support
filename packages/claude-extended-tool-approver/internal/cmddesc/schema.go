// Package cmddesc is the SCHEMA layer of the effect-graph spike: a
// CommandSchema is a plain data VALUE describing how one command's flags and
// operands map to typed Effects, a Registry holds those values keyed by
// basename, and an Interpreter turns a parsed leaf plus a schema into an
// Interpretation. Nothing in this package branches on a command name — the
// generic interpreter reads only the schema, and a new command is only a new
// registry entry.
//
// Known import cycle to fix later: this package imports internal/cmdparse for
// ParsedCommand, and cmdparse embeds hookio.Redirection, so hookio is reached
// TRANSITIVELY. No package in the spike imports internal/hookio directly (a
// guard test in internal/effectpolicy enforces that).
package cmddesc

import "fmt"

// RoleKind enumerates what an operand position MEANS to the command: a
// literal (inert) token, a path with a given access class, a program to be
// executed, or a free-text message.
type RoleKind int

const (
	// KindLiteral is an inert operand (a count, a pattern, a format string).
	KindLiteral RoleKind = iota
	// KindPathRead is a path the command reads.
	KindPathRead
	// KindPathCreate is a path the command creates (fails or overwrites if present
	// depending on the tool; the schema author picks Create vs Truncate).
	KindPathCreate
	// KindPathModify is a path the command modifies in place (append, edit).
	KindPathModify
	// KindPathDelete is a path the command removes.
	KindPathDelete
	// KindPathTruncate is a path the command truncates and rewrites.
	KindPathTruncate
	// KindProgram is a program text the command executes under Dialect.
	KindProgram
	// KindMessage is free text the command emits or records (a commit message).
	KindMessage
)

// String returns the deterministic role name used in labels and reasons.
func (k RoleKind) String() string {
	switch k {
	case KindLiteral:
		return "literal"
	case KindPathRead:
		return "path-read"
	case KindPathCreate:
		return "path-create"
	case KindPathModify:
		return "path-modify"
	case KindPathDelete:
		return "path-delete"
	case KindPathTruncate:
		return "path-truncate"
	case KindProgram:
		return "program"
	case KindMessage:
		return "message"
	default:
		return "role-invalid"
	}
}

// OperandRole is the schema's description of one operand slot. Program roles
// carry the Dialect the program text is written in; every other kind leaves it
// empty.
type OperandRole struct {
	Kind    RoleKind
	Dialect string
}

// Schema-author shorthands for the common roles. Program(dialect) builds the
// only role that needs a parameter.
var (
	Literal      = OperandRole{Kind: KindLiteral}
	PathRead     = OperandRole{Kind: KindPathRead}
	PathCreate   = OperandRole{Kind: KindPathCreate}
	PathModify   = OperandRole{Kind: KindPathModify}
	PathDelete   = OperandRole{Kind: KindPathDelete}
	PathTruncate = OperandRole{Kind: KindPathTruncate}
	Message      = OperandRole{Kind: KindMessage}
)

// Program returns the operand role for program text in the named dialect.
func Program(dialect string) OperandRole {
	return OperandRole{Kind: KindProgram, Dialect: dialect}
}

// IsPath reports whether the role denotes a filesystem path of any access class.
func (r OperandRole) IsPath() bool {
	switch r.Kind {
	case KindPathRead, KindPathCreate, KindPathModify, KindPathDelete, KindPathTruncate:
		return true
	default:
		return false
	}
}

// pathAccess maps a path role to the effect-level access class. Only valid
// when IsPath() is true.
func (r OperandRole) pathAccess() PathAccess {
	switch r.Kind {
	case KindPathCreate:
		return AccessCreate
	case KindPathModify:
		return AccessModify
	case KindPathDelete:
		return AccessDelete
	case KindPathTruncate:
		return AccessTruncate
	default:
		return AccessRead
	}
}

// TransformKind enumerates the rewrites a flag can apply to the effects the
// interpreter has collected. The generic interpreter applies each kind by
// EFFECT SHAPE only (access class, whether the effect came from a positional
// operand); it never consults a command name.
type TransformKind int

const (
	// TransformNone is the identity transform: the flag changes no effect.
	TransformNone TransformKind = iota
	// TransformDryRun removes every write-class path effect (create, modify,
	// delete, truncate); reads and non-path effects stay.
	TransformDryRun
	// TransformInPlace upgrades every read path effect that came from a
	// POSITIONAL operand to modify (sed -i edits its file operands). Reads from
	// flag operands (a `-f script`) stay reads.
	TransformInPlace
	// TransformNoClobber downgrades every truncate path effect to create (cp -n
	// never overwrites an existing destination).
	TransformNoClobber
)

// String returns the deterministic kind name.
func (k TransformKind) String() string {
	switch k {
	case TransformNone:
		return "none"
	case TransformDryRun:
		return "dry-run"
	case TransformInPlace:
		return "in-place"
	case TransformNoClobber:
		return "no-clobber"
	default:
		return "transform-invalid"
	}
}

// EffectTransform is an optional rewrite a flag applies to the effects the
// interpreter has collected. The interpreter applies every transform the
// invocation's flags name, generically, after operand collection, IN FLAG
// ORDER: `--dry-run --in-place` first drops the writes and then upgrades the
// positional reads to modifies (writes reappear), whereas `--in-place
// --dry-run` upgrades first and then drops them. A schema that models two
// interacting transforms on one command must therefore mirror the tool's own
// precedence in that order, and an unrecognised Kind makes the interpretation
// insufficient rather than silently passing effects through.
type EffectTransform struct {
	Kind TransformKind
}

// Arity says how many values a flag takes and how they may be spelled.
type Arity int

const (
	// ArityNone: a boolean or inert flag; a glued value is an error.
	ArityNone Arity = iota
	// ArityOne: exactly one value, either glued (`--lines=3`, `-n3`) or the
	// next token (`-n 3`).
	ArityOne
	// ArityOptionalGlued: a value is present ONLY when glued (`-i.bak`,
	// `--in-place=.bak`); a following separate token is NOT the value. This is
	// the GNU `[SUFFIX]` / `[=SUFFIX]` shape.
	ArityOptionalGlued
)

// FlagSpec is the schema entry for one flag spelling. Operand is the role of
// the flag's value when Arity gives it one. Transform is applied generically
// once the flag is seen.
type FlagSpec struct {
	Arity     Arity
	Operand   OperandRole
	Transform EffectTransform
}

// PositionalSpec expresses the positional layout: the FIRST len(Leading)
// positionals take the Leading roles, the LAST len(Trailing) take the Trailing
// roles, and everything between takes Rest. Both ends are conditional:
// Leading applies only if NONE of LeadingSkippedByFlags appeared (sed's first
// positional is the program unless -e/-f supplied one) and Trailing only if
// NONE of TrailingSkippedByFlags appeared (cp's last positional is the
// destination unless -t supplied it). Roles are resolved after the whole argv
// is scanned, so Trailing can see the end. Fewer positionals than the active
// Leading+Trailing need, or fewer Rest positionals than MinRest, make the
// interpretation insufficient (fail closed).
//
// StdinToken is the schema-level spelling of "read standard input instead of
// a file" (`-` for the coreutils family): a positional token equal to it in a
// path-role slot is a stdin effect, not a path effect. Empty means the
// command has no such token.
type PositionalSpec struct {
	Leading                []OperandRole
	LeadingSkippedByFlags  []string
	Rest                   OperandRole
	MinRest                int
	Trailing               []OperandRole
	TrailingSkippedByFlags []string
	StdinToken             string
}

// resolveRoles assigns a role to each of n positionals given the set of flag
// spellings that appeared. It reports false with a reason when the layout
// cannot be satisfied.
func (p PositionalSpec) resolveRoles(n int, flagsSeen map[string]bool) ([]OperandRole, string, bool) {
	leading := p.Leading
	if anySeen(p.LeadingSkippedByFlags, flagsSeen) {
		leading = nil
	}
	trailing := p.Trailing
	if anySeen(p.TrailingSkippedByFlags, flagsSeen) {
		trailing = nil
	}
	need := len(leading) + len(trailing) + p.MinRest
	if n < need {
		return nil, fmt.Sprintf("too few positionals: %d given, need at least %d", n, need), false
	}
	roles := make([]OperandRole, n)
	for i := range roles {
		switch {
		case i < len(leading):
			roles[i] = leading[i]
		case i >= n-len(trailing):
			roles[i] = trailing[i-(n-len(trailing))]
		default:
			roles[i] = p.Rest
		}
	}
	return roles, "", true
}

func anySeen(names []string, seen map[string]bool) bool {
	for _, n := range names {
		if seen[n] {
			return true
		}
	}
	return false
}

// StdinSpec says when the command consumes standard input.
type StdinSpec int

const (
	// StdinNever: the command never reads stdin.
	StdinNever StdinSpec = iota
	// StdinAlways: the command always reads stdin.
	StdinAlways
	// StdinWhenNoPathOperands: stdin is read only when no path operand was given
	// (the cat/head/grep convention). A StdinToken operand also reads it.
	StdinWhenNoPathOperands
)

// StdoutKind says what the command writes to standard output.
type StdoutKind int

const (
	// StdoutNone: nothing meaningful on stdout.
	StdoutNone StdoutKind = iota
	// StdoutContent: file/data CONTENT flows to stdout (cat, head).
	StdoutContent
	// StdoutMetadata: only names/sizes/status flow to stdout (ls, wc).
	StdoutMetadata
)

// UnknownFlagPolicy says how the interpreter treats a `-`-prefixed token the
// schema does not model.
type UnknownFlagPolicy int

const (
	// UnknownFlagInsufficient (the default zero value) makes the node
	// insufficient: an unmodeled flag might change the command's effects.
	UnknownFlagInsufficient UnknownFlagPolicy = iota
	// UnknownFlagInert treats unmodeled flags as arity-0 no-ops. A schema author
	// opts into this only for commands whose flags provably cannot add effects.
	UnknownFlagInert
)

// CommandSchema is the schema VALUE for one command. Provenance records the
// tool/version the entry was verified against. Flags is keyed by every
// spelling (`-n` and `--number` are separate keys). EndOfOptions says whether
// `--` ends flag parsing. Interpreter names a non-generic interpreter; empty
// means GenericInterpreter.
type CommandSchema struct {
	Name         string
	Provenance   string
	Flags        map[string]FlagSpec
	Positionals  PositionalSpec
	Stdin        StdinSpec
	Stdout       StdoutKind
	UnknownFlag  UnknownFlagPolicy
	EndOfOptions bool
	Interpreter  string
}
