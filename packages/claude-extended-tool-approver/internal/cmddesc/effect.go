package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// EffectKind enumerates the effect vocabulary a policy can judge.
type EffectKind int

const (
	// EffectOpaque is "something happens that the model cannot describe" — a
	// leaf with no schema. It can never be permitted.
	EffectOpaque EffectKind = iota
	// EffectPath is a filesystem access of some class on a path.
	EffectPath
	// EffectProgram is execution of program text in a dialect.
	EffectProgram
	// EffectEnv is a read or set of an environment variable.
	EffectEnv
	// EffectNet is network traffic to/from a host.
	EffectNet
	// EffectStdio is consumption or production of a standard stream.
	EffectStdio
	// EffectRemote is a read or mutation of a resource that lives outside the
	// local filesystem and is reached BY NAME (a git remote today; a k8s
	// context or a cloud bucket later).
	EffectRemote
	// EffectChdir is a change of the SHELL's working directory (cd, slice
	// 3o): Path is the target as written, Dynamic when it is a runtime value.
	// It is a graph-level effect — the builder re-bases later leaves in the
	// same list against it — and a policy judges only whether the target is
	// statically known.
	EffectChdir
	// EffectExec is a build/test TOOL operating ON or WITHIN a trusted
	// checkout (slice 3x; tc-lc8f item 4e, tc-vn5z item 1): either running
	// the checkout's own code (go test's compiled test binary, go
	// generate's //go:generate directives) or writing to the tool's own
	// declared, disposable build cache (go build/vet/fmt/list/env/version/
	// mod's GOCACHE/GOMODCACHE traffic — internal/deletable/workspace.go's
	// goKind). Deliberately NOT EffectProgram: that kind's own policy
	// (ProgramInterpreted) is unconditionally Permitted once the dialect
	// interpreter has vouched for the text, which is wrong here — this
	// effect is conditional on the invocation running inside a recognised
	// checkout, judged by TrustedCheckoutExec
	// (internal/effectpolicy/policy.go). Detail and Source (Effect's shared
	// free-text fields) carry the human-readable explanation; no new struct
	// field was needed.
	EffectExec
	// EffectKeyMaterial is a REFERENCE (by path) to a credential file used to
	// authenticate a remote connection — ssh/scp's `-i FILE` (slice 3aa,
	// tc-lc8f item 4g; tc-vn5z item 4). It is deliberately distinct from
	// EffectPath: the file's CONTENT is never read/disclosed anywhere this
	// model can observe (it is handed to the local client's own key-exchange
	// machinery), so treating it as an ordinary read would make
	// NoReadOfSecretPath (internal/effectpolicy/policy.go) Forbid every
	// `-i ~/.ssh/id_rsa` invocation outright, which conflates "reference a
	// key to authenticate with" with "disclose a secret's bytes". No policy
	// in DefaultPolicies judges this kind (deliberately — see
	// cmddesc.KindKeyMaterial's own doc comment), so judgeNode's fail-closed
	// fold (effectpolicy/evaluate.go) always treats it as "no policy judges
	// this effect": Insufficient/Abstain, never Approve or Reject, until a
	// future slice reviews key-material references deliberately.
	EffectKeyMaterial
)

// String returns the deterministic kind name.
func (k EffectKind) String() string {
	switch k {
	case EffectOpaque:
		return "opaque"
	case EffectPath:
		return "path"
	case EffectProgram:
		return "program"
	case EffectEnv:
		return "env"
	case EffectNet:
		return "net"
	case EffectStdio:
		return "stdio"
	case EffectRemote:
		return "remote"
	case EffectChdir:
		return "chdir"
	case EffectExec:
		return "exec"
	case EffectKeyMaterial:
		return "key-material"
	default:
		return "effect-invalid"
	}
}

// PathAccess is the access class of a path effect. It is the EFFECT-level
// vocabulary (what the command does), distinct from patheval's zone
// classification (what the environment allows) which the policies consult.
type PathAccess int

const (
	// AccessRead reads content.
	AccessRead PathAccess = iota
	// AccessCreate creates a new file.
	AccessCreate
	// AccessModify changes an existing file in place (append, edit).
	AccessModify
	// AccessDelete removes a file.
	AccessDelete
	// AccessTruncate truncates and rewrites a file.
	AccessTruncate
)

// String returns the deterministic access name.
func (a PathAccess) String() string {
	switch a {
	case AccessRead:
		return "read"
	case AccessCreate:
		return "create"
	case AccessModify:
		return "modify"
	case AccessDelete:
		return "delete"
	case AccessTruncate:
		return "truncate"
	default:
		return "access-invalid"
	}
}

// IsWrite reports whether the access class changes the filesystem. Anything
// that is not a pure read is a write, so a class added later fails closed.
func (a PathAccess) IsWrite() bool { return a != AccessRead }

// NetDirection is the direction CONTENT flows in a network effect: outbound
// means local content leaves for Host (an upload, a POST body), inbound means
// content arrives from Host (a fetch). Both are the local side connecting out;
// listening for connections is not modeled in this slice.
type NetDirection int

const (
	// NetOutbound: local content flows out to Host.
	NetOutbound NetDirection = iota
	// NetInbound: content flows in from Host.
	NetInbound
)

// String returns the deterministic direction name.
func (d NetDirection) String() string {
	if d == NetInbound {
		return "inbound"
	}
	return "outbound"
}

// StdioStream names a standard stream.
type StdioStream int

const (
	// StreamStdin is standard input.
	StreamStdin StdioStream = iota
	// StreamStdout is standard output.
	StreamStdout
	// StreamStderr is standard error.
	StreamStderr
)

// String returns the deterministic stream name.
func (s StdioStream) String() string {
	switch s {
	case StreamStdin:
		return "stdin"
	case StreamStdout:
		return "stdout"
	case StreamStderr:
		return "stderr"
	default:
		return "stream-invalid"
	}
}

// Effect is one typed, judgeable consequence of running a leaf. Kind selects
// which field group is meaningful; the others stay zero. Source records where a
// path effect came from ("arg <i>", "redirect", "stdin") so a reason can point
// at it.
type Effect struct {
	Kind EffectKind

	// EffectPath fields. Dynamic is true when the path text contains a runtime
	// expansion and so is NOT statically known; policies must treat it as
	// unknown. FromPositional is true when the path came from a POSITIONAL
	// operand (as opposed to a flag's value, a redirection, or a program's
	// dialect interpretation); TransformInPlace keys on it. It is provenance,
	// not identity, so String() does not render it — Source already names the
	// argument index.
	Path           string
	Access         PathAccess
	Dynamic        bool
	Source         string
	FromPositional bool

	// Remote is the host a PATH effect's target lives on, when the leaf that
	// produced it is inside a REMOTE scope (slice 3aa, tc-lc8f item 4g;
	// tc-vn5z item 4 — an `ssh HOST CMD` child, and anything nested inside
	// it), OR when the effect names a remote operand directly on a leaf that
	// is not itself scoped remote at all (slice 3ad, tc-lc8f item 4i; tc-vn5z
	// item 4 follow-up — scp's `[user@]host:path`/`scp://host/path`
	// operands, which sit on the SAME local `scp` node as an ordinary local
	// path operand, so there is no remote CHILD/scope to stamp through).
	// "" (the default) means the path is local. There are exactly two
	// producers: effectgraph's builder (cmddesc has no notion of "scope"),
	// which stamps every EffectPath on a node whose scope descends from a
	// remote child invocation with that child's host — see build.go's
	// interpret, the "Remote-scope stamping" comment; and cmddesc's own
	// scpInterpreter (interpreter_scp.go), which sets it DIRECTLY on the one
	// operand effect it already knows is remote, since a single scp leaf can
	// mix local and remote operands and the builder's scope-wide stamp would
	// be all-or-nothing. The builder's own stamping loop only OVERWRITES an
	// effect's Remote when the NODE's scope itself is remote (scpInterpreter's
	// node never is), so the two producers never race or double-stamp one
	// effect. Either way, a path policy can tell "this filesystem path is not
	// this process's local filesystem" from the effect alone, without
	// walking the graph itself. See internal/effectpolicy/policy.go's
	// remotePathGuard, the ONE place that reads this field.
	Remote string

	// EffectProgram fields.
	Program string
	Dialect string

	// EffectEnv fields. EnvSet is true for an assignment, false for a read.
	EnvName string
	EnvSet  bool

	// EnvValue, EnvExpansion and EnvCleared (slice 3an, tc-8og1 item 5;
	// tc-ife3 item 5) carry the ASSIGNMENT'S value, exactly as
	// cmdparse.EnvAssignment classified it, so effectpolicy.EnvAssignment can
	// port internal/rules/envvars.go's hermetic-env-value approval logic
	// (preservesCallerValue / isHermeticEnvReplacement /
	// isHermeticHomeReplacement's mktemp -d idiom) instead of judging the
	// NAME alone. EnvValue is cmdparse.EnvAssignment.Value verbatim (the
	// live engine's own input to LiteralAssignmentValueText); EnvExpansion
	// is that same assignment's Expansion census; EnvCleared is the LEAF's
	// own EnvCleared (true under `env -i`/`env --ignore-environment`), not a
	// per-assignment fact — it is copied onto every EffectEnv the leaf
	// produces because a policy sees one Effect at a time and has no other
	// way to learn it. All three are the zero value for a read (EnvSet ==
	// false) and for export's own KindEnvAssign operand role
	// (interpreter.go's envAssign, reached only for a bare `export NAME`
	// that marks an existing variable exported — cmdparse.liftAssignmentArgs
	// already lifts every literal `NAME=VALUE` export argument into the
	// leaf's EnvVars before cmddesc ever sees it, so envAssign never has a
	// VALUE token to carry here).
	EnvValue     string
	EnvExpansion cmdparse.ExpansionKind
	EnvCleared   bool

	// EffectNet fields. Dynamic (shared with the path fields) is true when the
	// URL is a runtime expansion, in which case Host holds the raw text. Method
	// is the request method when the protocol has one ("" otherwise).
	Host      string
	Direction NetDirection
	Method    string

	// EffectStdio fields. Metadata is true when only metadata (not content)
	// flows on the stream.
	Stream   StdioStream
	Metadata bool

	// EffectRemote fields. Resource is the token as given (a remote name like
	// "origin", or a URL); Operation is a small OPEN vocabulary ("push",
	// "force-push", "delete-ref" today) naming what happens to it. Dynamic
	// (shared with the path/net fields) is true when the resource is not
	// statically known (an implicit default remote, resolved from config at
	// runtime). DryRun is true when a dry-run flag (TransformDryRun) applied
	// to this effect — slice 3w (tc-lc8f item 4d; tc-ife3 item 2): a dry run
	// no longer ERASES a remote-mutation effect, it MARKS it, so a
	// forbidden-class operation (force-push, delete-ref) survives to
	// RemoteMutation's policy as a distinct, judgeable state instead of
	// vanishing into an automatic Approve. Marking (rather than deleting)
	// also makes the mark ORDER-INDEPENDENT: retargetRemote (TransformForce/
	// TransformDeleteRef) rewrites only Operation, so DryRun set before a
	// force/delete retarget survives it unchanged, and DryRun set after one
	// still lands on the already-retargeted Operation — either flag order on
	// the command line reaches the same final (Operation, DryRun) pair. It
	// is field-general (not remote-specific in name) in case a future
	// dry-runnable effect kind needs the same marker, though only
	// EffectRemote sets it today.
	Resource  string
	Operation string
	DryRun    bool

	// Family marks WHICH remote-effect FAMILY produced this EffectRemote
	// (slice 3y, tc-lc8f item 4f; tc-vn5z item 3): empty (the zero value,
	// used by every pre-existing producer — git's remote positional, bd's
	// implicit "beads"/"dolt" effects) keeps the ORIGINAL routing, judged
	// unconditionally by effectpolicy.RemoteMutation's fixed Operation
	// vocabulary ("read"/"mutate"/"push"/... always mean the same verdict).
	// "kubectl" (cmddesc/registry_breadth.go's kubectl schema family) routes
	// instead to effectpolicy.KubeContextPolicy, which RemoteMutation
	// explicitly excludes (see its own doc comment) — kubectl's Operation
	// vocabulary ("read"/"mutation"/"exec") is judged PER KUBE CONTEXT
	// (Resource, for a Family=="kubectl" effect, holds the context NAME —
	// see cmddesc's kubectlInterpreter for how it gets there), not by a
	// fixed table, which RemoteMutation's unconditional cases cannot
	// express. Kept as a generic string (not a bool) so a future
	// context-bearing remote family can reuse the same routing without a
	// new field.
	Family string

	// EffectOpaque detail (and free text for any kind).
	Detail string
}

// String renders the effect deterministically; it is the text used in Mermaid
// labels and decision reasons, so it must depend only on the effect's fields.
func (e Effect) String() string {
	var b strings.Builder
	b.WriteString(e.Kind.String())
	switch e.Kind {
	case EffectPath:
		fmt.Fprintf(&b, ":%s %s", e.Access, e.Path)
		if e.Dynamic {
			b.WriteString(" (dynamic)")
		}
		if e.Remote != "" {
			fmt.Fprintf(&b, " {remote:%s}", e.Remote)
		}
		if e.Source != "" {
			fmt.Fprintf(&b, " [%s]", e.Source)
		}
	case EffectProgram:
		fmt.Fprintf(&b, ":%s %q", e.Dialect, e.Program)
	case EffectEnv:
		if e.EnvSet {
			fmt.Fprintf(&b, ":set %s", e.EnvName)
		} else {
			fmt.Fprintf(&b, ":read %s", e.EnvName)
		}
	case EffectNet:
		fmt.Fprintf(&b, ":%s %s", e.Direction, e.Host)
		if e.Method != "" {
			b.WriteString(" " + e.Method)
		}
		if e.Dynamic {
			b.WriteString(" (dynamic)")
		}
		if e.Source != "" {
			fmt.Fprintf(&b, " [%s]", e.Source)
		}
	case EffectStdio:
		fmt.Fprintf(&b, ":%s", e.Stream)
		if e.Metadata {
			b.WriteString(" metadata")
		} else {
			b.WriteString(" content")
		}
	case EffectRemote:
		fmt.Fprintf(&b, ":%s %s", e.Operation, e.Resource)
		if e.Family != "" {
			fmt.Fprintf(&b, " {%s}", e.Family)
		}
		if e.Dynamic {
			b.WriteString(" (dynamic)")
		}
		if e.DryRun {
			b.WriteString(" (dry-run)")
		}
		if e.Source != "" {
			fmt.Fprintf(&b, " [%s]", e.Source)
		}
	case EffectChdir:
		fmt.Fprintf(&b, " %s", e.Path)
		if e.Dynamic {
			b.WriteString(" (dynamic)")
		}
		if e.Source != "" {
			fmt.Fprintf(&b, " [%s]", e.Source)
		}
	case EffectExec:
		if e.Source != "" {
			fmt.Fprintf(&b, ": %s", e.Source)
		}
	case EffectKeyMaterial:
		fmt.Fprintf(&b, ": %s", e.Path)
		if e.Dynamic {
			b.WriteString(" (dynamic)")
		}
		if e.Source != "" {
			fmt.Fprintf(&b, " [%s]", e.Source)
		}
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " (%s)", e.Detail)
	}
	return b.String()
}
