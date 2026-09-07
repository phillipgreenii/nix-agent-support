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

// EffectTransform is an optional rewrite a flag applies to the effects the
// interpreter has collected (a dry-run flag removing writes, an in-place flag
// upgrading reads to modifies). The interpreter applies every transform the
// invocation's flags name, generically, after operand collection. Only
// TransformNone exists in this slice; an unrecognised value makes the
// interpretation insufficient rather than silently passing effects through.
type EffectTransform int

// TransformNone is the identity transform: the flag changes no effect.
const TransformNone EffectTransform = 0

// FlagSpec is the schema entry for one flag spelling. Arity 0 is a boolean or
// inert flag; Arity 1 consumes the next token (or an `=`-glued / short-glued
// value) as an operand of the given role. Transform is applied generically
// once the flag is seen.
type FlagSpec struct {
	Arity     int
	Operand   OperandRole
	Transform EffectTransform
}

// PositionalSpec expresses "positions 0..k have these roles, the rest have this
// role". StdinToken is the schema-level spelling of "read standard input
// instead of a file" (`-` for the coreutils family): a positional token equal
// to it in a path-role slot is a stdin effect, not a path effect. Empty means
// the command has no such token.
type PositionalSpec struct {
	Leading    []OperandRole
	Rest       OperandRole
	StdinToken string
}

// roleAt returns the role for positional index i.
func (p PositionalSpec) roleAt(i int) OperandRole {
	if i < len(p.Leading) {
		return p.Leading[i]
	}
	return p.Rest
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
