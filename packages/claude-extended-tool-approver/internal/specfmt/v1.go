package specfmt

// FormatVersion is the only version this package currently understands. A
// Spec whose Version field does not equal this is rejected by
// (*Repository).Load (see repository.go) — a future v2 gets its own sibling
// types and a version switch there, never a silent reinterpretation of this
// file's shapes under a new tag.
const FormatVersion = "v1"

// SpecKind names what one Spec document describes. Only KindCommand is
// populated by this packet (Phase 1 packet 1.1); KindPath and KindTarget are
// reserved names for later phases (P8's target-spec fields, path-spec
// zones/secret declarations) so the envelope below never has to change
// shape to accommodate them — see doc.go's "What this packet mints".
type SpecKind string

const (
	// KindCommand names a COMMAND spec — the only kind this packet
	// populates fields for (Spec.Command).
	KindCommand SpecKind = "command"
	// KindPath names a PATH spec (later phase; reserved).
	KindPath SpecKind = "path"
	// KindTarget names a HOST/TARGET spec (later phase; reserved, P8/P14).
	KindTarget SpecKind = "target"
)

// Citation records WHERE a spec author verified one fact from: a --help
// excerpt, a man-page section/line, or a source-line reference into the
// tool's own source. Every fact-bearing element in this format carries one
// (CommandSpecV1.Citations for the command's own top-level facts;
// FlagSpecV1.Citation, PositionalSpecV1.Citation and ImplicitEffectV1.Citation
// for theirs) — Validate (validate.go) rejects a spec missing one. Source is
// free text; this package does not further parse or classify it (a linter,
// packet 1.3's scope, MAY choose to).
type Citation struct {
	Source string `json:"source"`
}

// empty reports whether the citation carries no source text — the condition
// Validate treats as "missing".
func (c Citation) empty() bool { return c.Source == "" }

// OperandRoleV1 is the wire form of cmddesc.OperandRole. Kind is the
// deterministic name cmddesc.RoleKind.String() already produces (see
// convert.go's roleKindByName, built FROM that method so the two can never
// drift apart); Dialect and Operation carry the same meaning as the Go type
// (Dialect only for a KindProgram role, Operation only for a KindRemote
// role).
type OperandRoleV1 struct {
	Kind      string `json:"kind"`
	Dialect   string `json:"dialect,omitempty"`
	Operation string `json:"operation,omitempty"`
}

// EffectTransformV1 is the wire form of cmddesc.EffectTransform. Kind is
// cmddesc.TransformKind.String()'s spelling.
type EffectTransformV1 struct {
	Kind string `json:"kind"`
}

// FlagSpecV1 is the wire form of cmddesc.FlagSpec, plus its own mandatory
// Citation (the fact being cited is "this flag spelling takes this
// arity/role/transform" — typically a --help line or man-page paragraph for
// the flag).
type FlagSpecV1 struct {
	Arity     string            `json:"arity"`
	Operand   OperandRoleV1     `json:"operand,omitempty"`
	Operands  []OperandRoleV1   `json:"operands,omitempty"`
	Transform EffectTransformV1 `json:"transform,omitempty"`
	Citation  Citation          `json:"citation"`
}

// RestOverrideV1 is the wire form of cmddesc.RestOverride.
type RestOverrideV1 struct {
	Flags []string      `json:"flags,omitempty"`
	Role  OperandRoleV1 `json:"role,omitempty"`
}

// PositionalSpecV1 is the wire form of cmddesc.PositionalSpec, plus its own
// mandatory Citation (the fact being cited is the command's whole positional
// layout — typically the --help synopsis line or man-page SYNOPSIS).
type PositionalSpecV1 struct {
	Leading                []OperandRoleV1 `json:"leading,omitempty"`
	LeadingSkippedByFlags  []string        `json:"leadingSkippedByFlags,omitempty"`
	LeadingOptional        bool            `json:"leadingOptional,omitempty"`
	Rest                   OperandRoleV1   `json:"rest,omitempty"`
	RestOverride           RestOverrideV1  `json:"restOverride,omitempty"`
	MinRest                int             `json:"minRest,omitempty"`
	Trailing               []OperandRoleV1 `json:"trailing,omitempty"`
	TrailingSkippedByFlags []string        `json:"trailingSkippedByFlags,omitempty"`
	StdinToken             string          `json:"stdinToken,omitempty"`
	Citation               Citation        `json:"citation"`
}

// ImplicitEffectV1 is the wire form of cmddesc.ImplicitEffect, plus its own
// mandatory Citation (the fact being cited is why this effect fires without
// consuming an operand — e.g. the man-page paragraph documenting `git
// status`'s implicit working-tree read).
type ImplicitEffectV1 struct {
	Role                  OperandRoleV1 `json:"role"`
	Target                string        `json:"target,omitempty"`
	Dynamic               bool          `json:"dynamic,omitempty"`
	WhenNoPositionals     bool          `json:"whenNoPositionals,omitempty"`
	WhenNoRestPositionals bool          `json:"whenNoRestPositionals,omitempty"`
	WhenFlags             []string      `json:"whenFlags,omitempty"`
	RemoteFamily          string        `json:"remoteFamily,omitempty"`
	Citation              Citation      `json:"citation"`
}

// Citation keys used in CommandSpecV1.Citations — one per top-level scalar
// fact. citationKeyInterpreter is only required when Interpreter is
// non-empty (the generic, empty-string interpreter is not itself a fact
// needing a citation).
const (
	citationKeyProvenance  = "provenance"
	citationKeyStdin       = "stdin"
	citationKeyStdout      = "stdout"
	citationKeyUnknownFlag = "unknownFlag"
	citationKeyInterpreter = "interpreter"
)

// CommandSpecV1 is the v1 on-disk serialization of cmddesc.CommandSchema —
// every field of that type has a 1:1 counterpart here (see convert.go's
// ToSchema/FromSchema), and Citations carries the mandatory per-fact
// citation for this schema's own top-level scalar facts (keyed by the
// citationKey* constants above); Positionals, each Flags entry, and each
// ImplicitEffects entry carry their OWN Citation field instead, since they
// are already their own addressable elements.
type CommandSpecV1 struct {
	Name                  string                   `json:"name"`
	Provenance            string                   `json:"provenance"`
	Citations             map[string]Citation      `json:"citations"`
	Flags                 map[string]FlagSpecV1    `json:"flags,omitempty"`
	Positionals           PositionalSpecV1         `json:"positionals"`
	ImplicitEffects       []ImplicitEffectV1       `json:"implicitEffects,omitempty"`
	Stdin                 string                   `json:"stdin"`
	Stdout                string                   `json:"stdout"`
	UnknownFlag           string                   `json:"unknownFlag"`
	EndOfOptions          bool                     `json:"endOfOptions,omitempty"`
	PositionalsEndOptions bool                     `json:"positionalsEndOptions,omitempty"`
	Interpreter           string                   `json:"interpreter,omitempty"`
	Subcommands           map[string]CommandSpecV1 `json:"subcommands,omitempty"`
	DefaultSubcommand     string                   `json:"defaultSubcommand,omitempty"`
	VerbFamily            string                   `json:"verbFamily,omitempty"`
	DefaultVerb           string                   `json:"defaultVerb,omitempty"`
}

// Spec is the top-level, SPEC-KIND-AGNOSTIC on-disk document: one file is
// one Spec. Version MUST equal FormatVersion. Name is the spec's identity
// within its Kind (a command name for KindCommand) — the Repository merges
// layers keyed by (Kind, Name). Overrides is the author's explicit
// declaration that THIS spec is meant to replace an earlier layer's spec of
// the same (Kind, Name); the Repository records a Conflict (see
// repository.go) when a later layer replaces an earlier one without it.
//
// Only Command is populated by this packet; a later phase adds a Path/
// Target field alongside it without touching this struct's existing fields
// or any Repository/merge code, since both key only on Kind+Name.
type Spec struct {
	Version   string         `json:"version"`
	Kind      SpecKind       `json:"kind"`
	Name      string         `json:"name"`
	Overrides bool           `json:"overrides,omitempty"`
	Command   *CommandSpecV1 `json:"command,omitempty"`
}
