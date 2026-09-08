// Package evalcontract is the PORT of the effect-graph spike: the
// hook-independent request/response types an adapter (a PreToolUse hook, a
// CLI, a test) uses to ask for a decision. It deliberately does not import
// internal/hookio; Decision is a new type, not an alias of the hook's.
//
// Known import cycle to fix later: effectgraph reaches internal/hookio
// transitively through cmdparse, which imports hookio for *hookio.HookInput
// (LeavesOf/RootLeavesOf's parameter) — not, as of the effect-graph spike's
// slice 3r, for any type this package or effectgraph names: Redirection moved
// out of hookio into the zero-dependency internal/hooktypes. The HookInput edge
// is a separate follow-up, tied to HookInput.ParsedLeaf/ParsedRoot being `any`.
package evalcontract

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"

// Request is what the caller knows about the command. ProjectRoot may be left
// empty; the evaluator then detects it from CWD. Dialect is the shell the
// command is written in (informational in this slice; parsing is bash).
// VettedHosts are the network hosts the caller trusts, as domain suffixes
// (`example.com` covers the apex and subdomains, `.internal.example`
// subdomains only); nil vets nothing.
//
// RemoteLifecycle is OPERATOR CONFIGURATION for an EffectRemote target's
// lifecycle-verb class, keyed by target ("dolt") with a verdict class value
// ("reject" is the only class recognised today). It is this spike's stand-in
// for a future rules.json binding (the production rules.json wiring is a
// follow-up, not this slice) — kept generic (target + class) rather than a
// dolt-specific boolean, since the same shape can carry a future target's
// lifecycle preference without a new field. nil configures nothing, so the
// policy's own default governs (see effectpolicy.RemoteMutation).
//
// KubeContexts/KubeContextDefaultAllow are OPERATOR CONFIGURATION for
// kubectl's own per-kube-context policy (slice 3y, tc-lc8f item 4f; tc-vn5z
// item 3 — operator ruling, Phillip 2026-09-07, verbatim: "kubectl should be
// configured to vary per context. ie, there could be a "dev" cluster which
// would allow most anythkng vs a "prod" which could be more restricted.").
// KubeContexts is keyed by context NAME (as given to `kubectl --context
// NAME`); each entry's Allow lists the EffectRemote Operation classes
// ("read", "mutation", "exec") permitted for that context. A context absent
// from this map falls back to KubeContextDefaultAllow (nil by default,
// i.e. no class allowed — Unknown/Abstain for every class, the conservative
// reading the ruling's own "restricted... could be more restricted" implies
// for an UNCONFIGURED context, as distinct from one deliberately configured
// narrow like "prod"). Like RemoteLifecycle, this is this spike's stand-in
// for a future rules.json binding — the production wiring is a follow-up,
// not this slice — mirroring its own "data on the request" pattern (slice
// 3u) rather than a kubectl-specific struct elsewhere.
// RemotePaths is OPERATOR CONFIGURATION for the categorized-path override
// hook a REMOTE-scope path effect consults before falling back to the
// default abstain (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4 — operator
// ruling, Phillip 2026-09-07, verbatim: "for ssh, abstain for paths should
// be thr default. however, we should allow some way to spexify a list of
// categorized paths."). Keyed by host (as ssh's own EffectNet/Effect.Remote
// name it — see cmddesc's sshInterpreter), each entry is an ORDERED list of
// prefix rules consulted in order, first match wins (RemotePathRule's own
// doc comment). This is this spike's stand-in for a future rules.json
// binding — the shape itself is NOT yet ruled on; see the design proposal
// appended to bead tc-vn5z's notes by this slice, and
// effectpolicy.remotePathGuard, the one reader — mirroring RemoteLifecycle/
// KubeContexts's own "data on the request, production wiring is a
// follow-up" pattern. nil (the default) configures nothing, so every
// remote path effect on every host abstains, per the ruling's own default.
type Request struct {
	Command                 string
	Dialect                 string
	CWD                     string
	ProjectRoot             string
	Env                     map[string]string
	VettedHosts             []string
	RemoteLifecycle         map[string]string
	KubeContexts            map[string]KubeContextRule
	KubeContextDefaultAllow []string
	RemotePaths             map[string][]RemotePathRule
}

// RemotePathRule is one categorized-path override entry (see Request.
// RemotePaths's doc comment): a path on a configured host whose PREFIX
// matches Prefix is classified Category instead of abstaining. Category is
// an OPEN vocabulary mirroring internal/deletable's local classification
// shape (Classify's Protected/Deletable/Writable, patheval's read/write
// zones) rather than a bespoke one, since the operator ruling's own
// "categorized paths" wording implies reusing a familiar taxonomy, not
// inventing a new one: "read-only" (read permitted, write/delete
// forbidden), "writable" (read/write permitted, delete needs consent —
// DeleteAccess's own local "writable but not deletable" shape), "deletable"
// (read/write/delete all permitted — the path is disposable), "protected"
// (every access class forbidden), "secret" (every access class forbidden,
// the WellKnownSecret-equivalent for a remote path). An unrecognised
// Category value is treated exactly like no match at all (fail closed to
// the ordinary remote-abstain default), never guessed at. This shape is a
// PROPOSAL, not yet operator-ruled — see the design note appended to bead
// tc-vn5z by this slice.
type RemotePathRule struct {
	Prefix   string
	Category string
}

// KubeContextRule is one kube context's operator-configured allow-list (see
// Request.KubeContexts's doc comment). Allow names the EffectRemote
// Operation classes ("read", "mutation", "exec") permitted for an
// invocation naming this context.
type KubeContextRule struct {
	Allow []string
}

// Decision is the verdict vocabulary. Ask exists in the vocabulary but nothing
// in this slice emits it.
type Decision int

const (
	// Abstain: the model is insufficient to decide (the zero value, so an
	// unset decision never reads as approval).
	Abstain Decision = iota
	// Approve: every node is understood and every effect permitted.
	Approve
	// Ask: defer to a human (reserved).
	Ask
	// Reject: at least one effect is known-forbidden.
	Reject
)

// String returns the deterministic decision name.
func (d Decision) String() string {
	switch d {
	case Abstain:
		return "abstain"
	case Approve:
		return "approve"
	case Ask:
		return "ask"
	case Reject:
		return "reject"
	default:
		return "decision-invalid"
	}
}

// Response carries the decision, a reason naming the deciding node and effect,
// and both graphs (the interpreted one carrying policy marks).
type Response struct {
	Decision    Decision
	Reason      string
	Structural  effectgraph.Graph
	Interpreted effectgraph.Graph
}
