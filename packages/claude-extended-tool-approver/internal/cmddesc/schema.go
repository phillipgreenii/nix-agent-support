// Package cmddesc is the SCHEMA layer of the effect-graph spike: a
// CommandSchema is a plain data VALUE describing how one command's flags and
// operands map to typed Effects, a Registry holds those values keyed by
// basename, and an Interpreter turns a parsed leaf plus a schema into an
// Interpretation. Nothing in this package branches on a command name — the
// generic interpreter reads only the schema, and a new command is only a new
// registry entry.
//
// Import cycle RESOLVED as of the effect-graph spike's slice 3ap (tc-8og1):
// this package imports internal/cmdparse for ParsedCommand. cmdparse used to
// also import internal/hookio for *hookio.HookInput (the parameter of
// LeavesOf/RootLeavesOf), which reached hookio TRANSITIVELY through this
// package's own cmdparse import — slice 3ap relocated LeavesOf/RootLeavesOf
// into hookio itself, removing that import, so cmdparse (and everything that
// imports it, this package included) no longer reaches internal/hookio at
// all, directly or transitively. No package in the spike imports
// internal/hookio directly (a guard test in internal/effectpolicy enforces
// that) — this package no longer reaches hookio for any TYPE it names either:
// ParsedCommand.Redirections is []internal/hooktypes.Redirection, a
// zero-dependency leaf package, as of the effect-graph spike's slice 3r.
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
	// KindDataOrAtFile is a literal UNLESS it starts with `@`: `@-` consumes
	// stdin and `@path` reads path. This is the generic "data or @file"
	// convention (curl's -d/-F use it) and the generic interpreter handles it.
	KindDataOrAtFile
	// KindRemote is an operand naming a RESOURCE that lives outside the local
	// filesystem, reached BY NAME (a git remote today; a k8s context or a
	// cloud bucket later). The role's Operation carries the default operation
	// the resource is subjected to (e.g. "push"); a transform may rewrite it
	// generically by effect shape (TransformForce, TransformDeleteRef).
	KindRemote
	// KindEnvAssign is an operand that ASSIGNS an environment variable: either
	// `NAME=VALUE` (a new value) or a bare `NAME` (marks an existing shell
	// variable exported — `export`'s own semantics for a bare name). Both
	// forms are modeled as an EffectEnv SET of NAME; this slice does not
	// distinguish them further (no policy yet judges EffectEnv by value).
	// `export -n NAME`'s UNexport semantics is the OPPOSITE of a set, so its
	// positional is deliberately NOT routed through this role at all — see
	// exportSchema's doc comment for why `-n` is left unmodeled instead.
	KindEnvAssign
	// KindUnmodeled marks a positional slot the schema author has
	// DELIBERATELY left without a role. Unlike an absent Positionals spec
	// (whose zero-value Rest defaults to KindLiteral, i.e. inert), a role of
	// this kind makes ANY operand that resolves to it insufficient: none of
	// the generic interpreter's operand() cases recognise it, so it falls
	// into the existing fail-closed default — a purely DATA addition, adding
	// no new code path. It exists for a command whose flag-only invocations
	// this slice understands but whose bare-positional form changes the
	// command's meaning in a way not modeled here (`git branch foo` creates a
	// ref; `git config NAME VALUE` writes) — see gitBranchSchema/
	// gitConfigSchema for the worked cases.
	KindUnmodeled
	// KindChdir is an operand naming the directory the SHELL changes into
	// (cd's operand, slice 3o). It emits two effects: a metadata PathRead of
	// the directory (the interpreter's ordinary path effect) and an
	// EffectChdir the graph builder consumes to re-base every LATER leaf in
	// the same list against the new working directory — see
	// effectgraph's builder for the scoping rules. `-` (the previous
	// directory) is a runtime value and is emitted Dynamic.
	KindChdir
	// KindExec is IMPLICIT-ONLY (slice 3x, tc-lc8f item 4e; tc-vn5z item 1):
	// it names no operand text at all — a schema declares it via
	// ImplicitEffect, never via a Positionals/FlagSpec Operand role, so
	// operand()'s switch has no case for it and would fail closed like any
	// other unmodeled role if one were ever routed through it. It marks "the
	// build tool is operating ON or WITHIN a trusted checkout" — running the
	// checkout's own code (go test/go generate) or writing to the tool's own
	// declared build cache (go build/vet/fmt/list/env/version/mod) — a
	// single fact TrustedCheckoutExec (internal/effectpolicy/policy.go)
	// judges by checking whether the invocation's CWD is inside a recognised
	// git/go workspace, never by command name. See registry_breadth.go's
	// goTestSchema/goBuildSchema for the worked case.
	KindExec
	// KindKeyMaterial is an operand naming a CREDENTIAL FILE referenced BY
	// PATH to authenticate a remote connection (ssh/scp's `-i FILE`; slice
	// 3aa, tc-lc8f item 4g; tc-vn5z item 4) — deliberately NOT KindPathRead:
	// the command does not read and disclose the file's CONTENT anywhere
	// this model can see (it is handed to the local ssh client's own TLS/
	// key-exchange machinery), so routing it through the ordinary read
	// effect would make NoReadOfSecretPath (internal/effectpolicy/policy.go)
	// Forbid every `ssh -i ~/.ssh/id_rsa host ...` invocation outright — a
	// key REFERENCE is not the same hazard as a `cat ~/.ssh/id_rsa`. No
	// policy in this slice judges the resulting EffectKeyMaterial (see its
	// own doc comment in effect.go), so it always abstains rather than
	// either approving or rejecting — the conservative middle ground until
	// a future slice reviews it deliberately.
	KindKeyMaterial
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
	case KindDataOrAtFile:
		return "data-or-at-file"
	case KindRemote:
		return "remote"
	case KindEnvAssign:
		return "env-assign"
	case KindUnmodeled:
		return "unmodeled"
	case KindChdir:
		return "chdir"
	case KindExec:
		return "exec"
	case KindKeyMaterial:
		return "key-material"
	default:
		return "role-invalid"
	}
}

// OperandRole is the schema's description of one operand slot. Program roles
// carry the Dialect the program text is written in; a Remote role carries the
// default Operation instead (a generic string, not overloading Dialect —
// Dialect names a PROGRAM LANGUAGE, Operation names a REMOTE VERB); every
// other kind leaves both empty.
type OperandRole struct {
	Kind      RoleKind
	Dialect   string
	Operation string
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
	DataOrAtFile = OperandRole{Kind: KindDataOrAtFile}
	EnvAssign    = OperandRole{Kind: KindEnvAssign}
	Unmodeled    = OperandRole{Kind: KindUnmodeled}
	Chdir        = OperandRole{Kind: KindChdir}
	Exec         = OperandRole{Kind: KindExec}
	KeyMaterial  = OperandRole{Kind: KindKeyMaterial}
)

// Program returns the operand role for program text in the named dialect.
func Program(dialect string) OperandRole {
	return OperandRole{Kind: KindProgram, Dialect: dialect}
}

// Remote returns the operand role for a remote-resource operand whose default
// operation is operation (e.g. "push").
func Remote(operation string) OperandRole {
	return OperandRole{Kind: KindRemote, Operation: operation}
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
	// TransformAppend downgrades every truncate path effect to modify (tee -a
	// appends to its file operands instead of rewriting them).
	TransformAppend
	// TransformForce rewrites every EffectRemote whose Operation is "push" to
	// "force-push", generically by effect shape (git push -f/--force/
	// --force-with-lease).
	TransformForce
	// TransformDeleteRef rewrites every EffectRemote whose Operation is "push"
	// to "delete-ref", generically by effect shape (git push -d/--delete).
	TransformDeleteRef
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
	case TransformAppend:
		return "append"
	case TransformForce:
		return "force"
	case TransformDeleteRef:
		return "delete-ref"
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
	// ArityN: exactly len(FlagSpec.Operands) values, each the next separate
	// token, each with its OWN role from Operands in order (jq's `--arg NAME
	// VALUE`, `--rawfile NAME FILE`). A glued value is an error, as is
	// running out of argv before every operand is supplied (slice 3n).
	ArityN
)

// FlagSpec is the schema entry for one flag spelling. Operand is the role of
// the flag's value when Arity gives it one; Operands is the per-position
// role list an ArityN flag consumes instead (and its length is that arity).
// Transform is applied generically once the flag is seen.
type FlagSpec struct {
	Arity     Arity
	Operand   OperandRole
	Operands  []OperandRole
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
// interpretation insufficient (fail closed) — UNLESS LeadingOptional is set
// and there are ZERO positionals at all, in which case Leading is satisfied
// vacuously (git push's optional leading remote: `git push` alone has no
// remote operand and no refspecs either, which an implicit effect covers
// instead; `git push origin main` still requires the full layout once ANY
// positional is present).
//
// RestOverride is the swap-A-ROLE-IN counterpart to LeadingSkippedByFlags/
// TrailingSkippedByFlags, which only ever REMOVE a role: when any of
// RestOverride.Flags appeared, EVERY Rest positional takes RestOverride.Role
// instead of Rest. It exists for a command whose Rest positionals mean one
// thing ordinarily and something else, SAFE, under a specific flag —
// `git branch <pattern>` creates a ref (Rest: Unmodeled) but `git branch
// --list <pattern>` filters a listing (RestOverride: {Flags: ["-l",
// "--list"], Role: Literal}); `git config <key> <value>` writes (Rest:
// Unmodeled) but `git config --get <key> <value-pattern>` filters which
// existing value is printed (RestOverride keyed on --get/--get-all/
// --get-regexp). Leading and Trailing have no analogous override: no schema
// in this registry needs one on those ends, so it stays scoped to Rest rather
// than growing three overrides speculatively. Zero value (empty Flags) is a
// no-op.
//
// StdinToken is the schema-level spelling of "read standard input instead of
// a file" (`-` for the coreutils family): a positional token equal to it in a
// path-role slot is a stdin effect, not a path effect. Empty means the
// command has no such token.
type PositionalSpec struct {
	Leading                []OperandRole
	LeadingSkippedByFlags  []string
	LeadingOptional        bool
	Rest                   OperandRole
	RestOverride           RestOverride
	MinRest                int
	Trailing               []OperandRole
	TrailingSkippedByFlags []string
	StdinToken             string
}

// RestOverride names the flag spellings that, when any appeared, replace
// PositionalSpec.Rest with Role for every Rest positional — see
// PositionalSpec.RestOverride's doc comment for the worked cases.
type RestOverride struct {
	Flags []string
	Role  OperandRole
}

// resolveRoles assigns a role to each of n positionals given the set of flag
// spellings that appeared. It reports false with a reason when the layout
// cannot be satisfied.
func (p PositionalSpec) resolveRoles(n int, flagsSeen map[string]bool) ([]OperandRole, string, bool) {
	leading, trailing := p.layout(n, flagsSeen)
	need := len(leading) + len(trailing) + p.MinRest
	if n < need {
		return nil, fmt.Sprintf("too few positionals: %d given, need at least %d", n, need), false
	}
	rest := p.Rest
	if anySeen(p.RestOverride.Flags, flagsSeen) {
		rest = p.RestOverride.Role
	}
	roles := make([]OperandRole, n)
	for i := range roles {
		switch {
		case i < len(leading):
			roles[i] = leading[i]
		case i >= n-len(trailing):
			roles[i] = trailing[i-(n-len(trailing))]
		default:
			roles[i] = rest
		}
	}
	return roles, "", true
}

// layout is the ONE place the effective Leading and Trailing slots for an
// invocation of n positionals are computed (the skip-by-flag and
// LeadingOptional rules), shared by resolveRoles and restCount so the two
// can never disagree about which positionals are Rest.
func (p PositionalSpec) layout(n int, flagsSeen map[string]bool) (leading, trailing []OperandRole) {
	leading = p.Leading
	if anySeen(p.LeadingSkippedByFlags, flagsSeen) {
		leading = nil
	}
	if p.LeadingOptional && n == 0 {
		leading = nil
	}
	trailing = p.Trailing
	if anySeen(p.TrailingSkippedByFlags, flagsSeen) {
		trailing = nil
	}
	return leading, trailing
}

// restCount reports how many of n positionals resolve to the Rest role —
// the ones left after the Leading and Trailing slots are filled — for
// ImplicitEffect.WhenNoRestPositionals. Never negative: an invocation too
// short to fill its slots has zero Rest positionals (and resolveRoles has
// already failed it).
func (p PositionalSpec) restCount(n int, flagsSeen map[string]bool) int {
	leading, trailing := p.layout(n, flagsSeen)
	if c := n - len(leading) - len(trailing); c > 0 {
		return c
	}
	return 0
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

// ImplicitEffect is an effect a schema declares WITHOUT an operand: it fires
// from the mere presence (or absence) of positionals, not from consuming argv
// text. Role's Kind picks the effect shape (a path role emits an EffectPath
// at Target; KindRemote emits an EffectRemote naming Target as the Resource,
// with Role.Operation as the Operation; KindExec emits an EffectExec naming
// Target as its Source — e.g. "go test" — with no path/resource semantics at
// all, slice 3x, tc-lc8f item 4e). WhenNoPositionals restricts emission
// to invocations with ZERO resolved positionals (a pathspec-less `git clean`,
// a remote-less `git push`); false means "always", regardless of what
// positionals were also given (`git status`'s implicit read of the working
// tree). Dynamic marks a target that is not statically known (the default
// remote git push resolves from config at runtime) — this is the SAME
// Dynamic semantics as an operand effect's, just supplied by the schema
// instead of discovered from the argv text.
// WhenFlags further restricts emission to invocations where at least one of
// the named flag spellings was seen (grep's implicit recursive-from-"."
// read, which only makes sense under -r/-R). Empty means no flag condition;
// every condition set must hold (AND).
//
// WhenNoRestPositionals restricts emission to invocations where zero
// positionals resolved to the REST role — positionals consumed by a
// Leading or Trailing slot do not count. It exists for grep: the pattern
// occupies the sole Leading slot unless -e/-f supplied it, so under
// WhenNoPositionals a bare positional pattern (`grep -r TODO`) counted as
// "a positional was given" and the implicit recursive read never fired —
// grep fell through to a stdin read instead of the honest "reads
// everything under ." (tc-q9ak item 2; before that, a documented
// imprecision on grepSchema). Counting only Rest positionals is the
// condition grep's semantics actually express: it searches "." when no
// FILE operand was given, whatever supplied the pattern.
//
// The restCount is computed by PositionalSpec.restCount from the same
// layout resolveRoles uses, so the two cannot disagree.
type ImplicitEffect struct {
	Role                  OperandRole
	Target                string
	Dynamic               bool
	WhenNoPositionals     bool
	WhenNoRestPositionals bool
	WhenFlags             []string
	// RemoteFamily sets Effect.Family on a KindRemote implicit effect (slice
	// 3y, tc-lc8f item 4f; tc-vn5z item 3): empty (the default) keeps every
	// PRE-EXISTING KindRemote implicit effect (git's default-remote push, bd/
	// dolt) routed to effectpolicy.RemoteMutation exactly as before this
	// field existed. "kubectl" routes instead to effectpolicy.
	// KubeContextPolicy — see Effect.Family's own doc comment for why a
	// SEPARATE policy is needed rather than widening RemoteMutation's fixed
	// per-Operation table.
	RemoteFamily string
}

// CommandSchema is the schema VALUE for one command. Provenance records the
// tool/version the entry was verified against. Flags is keyed by every
// spelling (`-n` and `--number` are separate keys). EndOfOptions says whether
// `--` ends flag parsing. PositionalsEndOptions says that the FIRST positional
// ends flag parsing too (the getopt `+`/POSIXLY_CORRECT convention: a wrapper
// such as xargs or a shell hands every later `-x` to the command it runs, not
// to itself). Interpreter names a non-generic interpreter; empty means
// GenericInterpreter.
//
// Subcommands is the subcommand-dispatch shape: when non-empty, Flags is the
// GLOBAL-option table (scanned until the first positional, which becomes the
// subcommand key), and Positionals/Stdin/Stdout/ImplicitEffects belong to the
// SUBCOMMAND schemas instead — the parent's own copies of those fields are
// unused. See interpretSubcommand for the dispatch semantics; it recurses on
// the same code path, so a Subcommands value MAY itself have Subcommands.
//
// ImplicitEffects lists effects the schema emits without an operand (see
// ImplicitEffect); the generic interpreter emits them like any other effect,
// so transforms and policies apply to them identically.
type CommandSchema struct {
	Name                  string
	Provenance            string
	Flags                 map[string]FlagSpec
	Positionals           PositionalSpec
	ImplicitEffects       []ImplicitEffect
	Stdin                 StdinSpec
	Stdout                StdoutKind
	UnknownFlag           UnknownFlagPolicy
	EndOfOptions          bool
	PositionalsEndOptions bool
	Interpreter           string
	Subcommands           map[string]CommandSchema
	// DefaultSubcommand names the Subcommands entry to dispatch to when the
	// first positional is NOT a subcommand key; that positional is then
	// handed to the default subcommand as its own first positional rather
	// than consumed (yq's implicit `eval`, slice 3n). Empty means an
	// unknown key is an unmodeled subcommand (git's shape).
	DefaultSubcommand string
	// VerbFamily is the ARGV-SHAPED build-tool VERB-DISPATCH shape (tc-8og1
	// item 3 sub-slice 4; tc-vn5z Q4, ruled 2026-09-08: "reuse interpreter_
	// subcommand.go's recursion... for argv-shaped children"). Non-empty
	// makes GenericInterpreter.Interpret route through interpretVerbDispatch
	// (interpreter_subcommand.go) instead of the ordinary flag-table scan —
	// see that function's own doc comment for the full contract. It names
	// the build-tool FAMILY stamped onto the resulting EffectExec
	// (Effect.Family) — matching evalcontract.VerbScopedApproval.Tool and
	// deletable.Kind.Name (slice 3ag), e.g. "just", "npm", "devbox" — which
	// is why a SUBCOMMAND schema that dispatches through this shape (npm's
	// "run", devbox's "run") sets VerbFamily to the TOOL's name, not its
	// own Name field ("run" would not match npmKind.Name).
	//
	// Mutually exclusive with Subcommands: GenericInterpreter.Interpret
	// checks Subcommands FIRST, so a schema with both set would silently
	// get Subcommands' behavior only — this package never constructs one.
	// Unlike Subcommands (which repurposes Positionals/ImplicitEffects/
	// Stdin/Stdout to a CHILD schema), a VerbFamily schema has no child
	// schema to hand those fields to, so they keep their ordinary,
	// top-level meaning here: they describe THIS schema's own
	// bare-invocation case — no verb positional at all (`just` alone, `npm
	// run` alone) — which interpretVerbDispatch falls back to, UNLESS
	// DefaultVerb overrides that fallback (see its own doc comment).
	VerbFamily string
	// DefaultVerb is a VerbFamily schema's verb-dispatch shape for a tool
	// whose BARE invocation (no verb positional at all) still DISPATCHES —
	// unlike just/npm run/devbox run, whose bare form only LISTS available
	// recipes/scripts (a safe, inert introspection covered by the schema's
	// ordinary top-level Stdout/ImplicitEffects fallback, see VerbFamily's
	// own doc comment). tc-8og1 item 3 sub-slice 5 (nix run installable
	// vetting; tc-vn5z Q4): `nix run` with ZERO positional arguments is
	// documented, and verified live against this host's installed nix (Nix)
	// 2.34.8 (2026-09-08, `nix run --help`'s own synopsis plus an empty-
	// directory smoke test — `nix run` alone in a directory with no
	// flake.nix fails with "could not find a flake.nix file", proving the
	// installable resolution is attempted immediately, not merely listed),
	// to behave EXACTLY like `nix run .` — it resolves and executes the
	// CURRENT directory's own flake default app/package. Empty (the zero
	// value, every pre-existing VerbFamily schema) preserves
	// interpretVerbDispatch's original "no verb positional -> fall back to
	// the schema's own top-level fields" behaviour unchanged. Non-empty
	// makes interpretVerbDispatch additionally emit an EffectExec{Family:
	// VerbFamily, Operation: DefaultVerb, Source: "implicit"} — ON TOP OF,
	// not instead of, the schema's own top-level fields (which run first,
	// via the ordinary finish() pass, exactly as they would with
	// DefaultVerb absent) — so a future DefaultVerb schema that ALSO wants
	// an ordinary implicit path/stdio effect on its bare form is not
	// precluded from having one.
	DefaultVerb string
}
