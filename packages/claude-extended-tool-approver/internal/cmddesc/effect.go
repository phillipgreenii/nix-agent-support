package cmddesc

import (
	"fmt"
	"strings"
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

	// EffectProgram fields.
	Program string
	Dialect string

	// EffectEnv fields. EnvSet is true for an assignment, false for a read.
	EnvName string
	EnvSet  bool

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
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " (%s)", e.Detail)
	}
	return b.String()
}
