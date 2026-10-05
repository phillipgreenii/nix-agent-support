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
// role, Set only for a KindAllowedLiteral role — a NAMED reference to a
// closed literal set compiled into cmddesc, which the loader rejects when
// unknown, exactly like Dialect/Interpreter, P13).
type OperandRoleV1 struct {
	Kind      string `json:"kind"`
	Dialect   string `json:"dialect,omitempty"`
	Operation string `json:"operation,omitempty"`
	Set       string `json:"set,omitempty"`
}

// EffectTransformV1 is the wire form of cmddesc.EffectTransform. Kind is
// cmddesc.TransformKind.String()'s spelling.
type EffectTransformV1 struct {
	Kind string `json:"kind"`
	// From/To carry a "retarget-remote" transform's Operation spellings
	// (cmddesc.EffectTransform.From/To); empty for every other kind.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
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
	// LiveOperandNextToOperator is the wire form of
	// cmddesc.PositionalSpec.LiveOperandNextToOperator (pg2-5ctay).
	LiveOperandNextToOperator bool     `json:"liveOperandNextToOperator,omitempty"`
	Citation                  Citation `json:"citation"`
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
	// RemotePersistent is the wire form of cmddesc.ImplicitEffect.RemotePersistent
	// (P8, docket tc-o14i5.3, packet tc-o14i5.3.4) — see that field's own doc
	// comment.
	RemotePersistent bool     `json:"remotePersistent,omitempty"`
	Citation         Citation `json:"citation"`
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
// within its Kind (a command name for KindCommand; a target name — "kinfra",
// "synfra.twistcone.us" — for KindTarget) — the Repository merges layers
// keyed by (Kind, Name), plus TargetKind for a KindTarget spec (see
// TargetSpecV1's own doc comment for why Name alone is not enough there).
// Overrides is the author's explicit declaration that THIS spec is meant to
// replace an earlier layer's spec of the same key; the Repository records a
// Conflict (see repository.go) when a later layer replaces an earlier one
// without it.
//
// Command is populated for KindCommand (Phase 1, packet 1.1); Target is
// populated for KindTarget (Phase 2, packet tc-o14i5.3.4, P8). KindPath
// remains reserved, no payload defined yet. Adding Target did not touch this
// struct's existing fields or any Repository/merge code path Command already
// used — see repository.go's own doc comments for exactly what was added.
type Spec struct {
	Version   string         `json:"version"`
	Kind      SpecKind       `json:"kind"`
	Name      string         `json:"name"`
	Overrides bool           `json:"overrides,omitempty"`
	Command   *CommandSpecV1 `json:"command,omitempty"`
	Target    *TargetSpecV1  `json:"target,omitempty"`
}

// TargetKind names the NAMESPACE a KindTarget spec's Name is drawn from —
// which kind of target ("docker context", "kube context", ...) the name
// identifies. It is a SEPARATE axis from Name because different target
// kinds can legitimately share a name (a docker context and an ssh host
// both named "prod" are two different real-world things), so the Repository
// merges/looks up KindTarget specs keyed by (TargetKind, Name), not Name
// alone — see repository.go's key.SubKind field and MergedSet.Targets.
//
// Docket tc-o14i5.3, packet tc-o14i5.3.4 (P8): "each entry names a target
// (docker context/host, kube context/server, vault address, ssh host, git
// remote) and its class". Git remote is included in the design vocabulary
// but is explicitly this packet's sibling's concern (K9 git-protection,
// K10 push-policy) to CONSUME, not this packet's to wire into any policy —
// see this packet's own "Out of scope" section.
type TargetKind string

const (
	// TargetKindDockerContext names a `docker context` entry (the CLI's own
	// multi-daemon-endpoint concept, `docker context ls`/`--context NAME`).
	TargetKindDockerContext TargetKind = "docker-context"
	// TargetKindDockerHost names a raw Docker daemon endpoint given via
	// `-H`/`--host`/`DOCKER_HOST` (a host[:port] or a full URL) rather than
	// a named context.
	TargetKindDockerHost TargetKind = "docker-host"
	// TargetKindKubeContext names a kubeconfig context (`kubectl --context
	// NAME`), the same identity evalcontract.Request.KubeContexts already
	// keys its OWN, pre-existing per-context policy on (see P6's rollback
	// note on this package's doc comment and effectpolicy.KubeContextPolicy).
	TargetKindKubeContext TargetKind = "kube-context"
	// TargetKindKubeServer names a raw Kubernetes API server URL given via
	// `--server`/`-s` directly, bypassing context-name resolution entirely.
	TargetKindKubeServer TargetKind = "kube-server"
	// TargetKindVaultAddress names a HashiCorp Vault server address
	// (`VAULT_ADDR`/`vault -address=...`).
	TargetKindVaultAddress TargetKind = "vault-address"
	// TargetKindSSHHost names an ssh/scp destination host, the same identity
	// cmddesc.Effect.Remote/Host already carries for ssh's own EffectNet and
	// remote-scope EffectPath stamping (see effect.go's Effect.Remote doc
	// comment).
	TargetKindSSHHost TargetKind = "ssh-host"
	// TargetKindGitRemote names a git remote (the same identity
	// cmddesc.Effect.Resource carries for a git EffectRemote). Reserved for
	// K9/K10's own consumption — see this package's TargetKind doc comment.
	TargetKindGitRemote TargetKind = "git-remote"
)

// TargetClass is a target's operator-declared trust class (P8). Mutation of
// a TargetClassProduction target is REJECTed; an unlisted target (no
// TargetSpecV1 entry at all) is ABSTAINed; TargetClassTrustedDev is judged
// by the mutating effect itself (this package records no verdict for it —
// see effectpolicy.TargetSpecPolicy, the one place that turns a TargetClass
// into a Reject/Abstain/defer verdict; P14 forbids a spec value producing a
// verdict directly).
type TargetClass string

const (
	// TargetClassTrustedDev is a target the operator trusts for ordinary
	// development traffic; its mutations are judged by whatever
	// effect-level policy already applies (defer, not a blanket approve).
	TargetClassTrustedDev TargetClass = "trusted-dev"
	// TargetClassProduction is a target whose mutations are always rejected
	// outright, regardless of what any other policy would otherwise say.
	TargetClassProduction TargetClass = "production"
)

// TargetKey identifies one merged target-spec entry: TargetKind namespaces
// Name (see TargetKind's own doc comment for why Name alone collides across
// kinds). It is the map key of MergedSet.Targets and the exported shape
// internal/targetspec's Lookup is built from.
type TargetKey struct {
	Kind TargetKind
	Name string
}

// TargetSpecV1 is the v1 on-disk payload of a KindTarget Spec (P8). Citation
// records the operator's own provenance for WHY this target is classified
// this way (an infrastructure inventory entry, an ADR, a runbook) — the same
// "every fact-bearing element carries one" convention CommandSpecV1 already
// follows (see Citation's own doc comment), applied here to the FACT "this
// target belongs to this trust class" rather than a command's documented
// behavior.
type TargetSpecV1 struct {
	TargetKind TargetKind  `json:"targetKind"`
	Class      TargetClass `json:"class"`
	Citation   Citation    `json:"citation"`
}
