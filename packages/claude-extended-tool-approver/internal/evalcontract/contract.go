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
//
// BuildToolVerbs is OPERATOR CONFIGURATION for the build-tool family
// (just/npm/devbox/nix/prek/... — tc-8og1 item 3, sub-slice 3 of 5;
// tc-vn5z Q1-Q5, ruled 2026-09-08). See VerbScopedApproval's own doc
// comment for the class/child design those rulings settled. nil (the
// default) approves nothing: every build-tool-family EffectExec
// (Effect.Family != "") abstains, the safe base default — mirroring
// RemoteLifecycle/KubeContexts/RemotePaths's own "data on the request,
// production wiring is a follow-up" pattern exactly. The reader is
// effectpolicy.TrustedCheckoutExec, extended in place per Q2's ruling
// ("leaning toward extending TrustedCheckoutExec's marker list ... one
// policy for every EffectExec regardless of producing tool") rather than a
// new per-family policy.
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
	BuildToolVerbs          []VerbScopedApproval
}

// VerbClassProjectTied is VerbScopedApproval's default Class — see
// VerbScopedApproval's own doc comment. It requires deletable.
// DiscoveredVerbs' independent, live confirmation IN ADDITION to this
// declaration (Q3's ruling); it is NEVER sufficient by itself.
const VerbClassProjectTied = "project-tied"

// VerbClassInstallableReference is VerbScopedApproval's SECOND judged class
// (tc-8og1 item 3 sub-slice 5, the final sub-slice of the build-tool family
// design; tc-vn5z Q1-Q4, ruled 2026-09-08) — added for `nix run`'s
// installable-reference child (Q4's own named example of a shape
// interpreter_subcommand.go's recursion alone cannot fully judge). Unlike
// VerbClassProjectTied, this class is sufficient BY ITSELF: it never
// requires deletable.DiscoveredVerbs confirmation, because Q1's ruling
// deliberately reserves flake.nix apps to operator rules.json data — "can't
// be safely lexically scanned" — so no live-discovery mechanism for a Nix
// installable exists, or ever will under Q1 as ruled, to independently
// confirm one. Per Q3's ruling ("operator data governs everything reached
// by reference"), a flake installable — even a LOCAL one (`.`, `.#foo`) —
// is reached BY REFERENCE (attribute-path resolution requires evaluating
// the flake, unlike a justfile recipe header's plain, un-evaluated text),
// so the operator's own declaration is the WHOLE trust mechanism, not an
// eligibility gate a second, independent source must also confirm.
//
// Scoped narrowly and DELIBERATELY (this sub-slice's own choice, not an
// operator ruling — see effectpolicy's own classifier, evalcontract package
// has no direct access to it, this comment records the POLICY, not the
// mechanics): only a LOCAL flake reference — the literal current-directory
// flakeref "." or a "."-relative attribute selector on it (".#<attr>",
// which may itself carry a "^<output>" output selector) — is ever judged
// Permitted under this class, regardless of what Verb string the operator
// declares. A remote/registry-resolved installable (`nixpkgs#hello`,
// `github:owner/repo#app`, `git+https://...`, a bare registry id, an
// absolute or relative PATH reference outside the bare "."/".#attr" pair,
// a raw /nix/store path) is DELIBERATELY left Unknown even when an operator
// declares it under this class — see effectpolicy's own local/non-local
// classifier (policy.go) for the full rationale and the documented
// follow-up this narrowing leaves for a later slice: vetting a REMOTE
// installable's trust (an unpinned flake registry lookup, an arbitrary
// git/GitHub fetch) is a materially different, higher-stakes risk this
// slice does not attempt to model or rule on.
const VerbClassInstallableReference = "installable-reference"

// VerbScopedApproval is one entry of Request.BuildToolVerbs. It mirrors
// production's internal/rules/configrules.VerbScopedApproval{Tool, Verb}
// shape EXACTLY (same field names, same "approve Tool only for a specific
// first subcommand" starting point — Q5's own ruling text names it
// "VerbScopedApproval" for that reason) but is a SEPARATE, spike-local
// type, not an alias or reuse of the production one: the spike does not
// import internal/rules/configrules (that package is wired into
// internal/setup.RuleChain, production's live path, and per tc-8og1's own
// "CONSTRAINT on ALL work from this bead" this spike is explicitly NOT
// wired into RuleChain). A future production rules.json binding —
// literally extending the real configrules.VerbScopedApproval — is a
// follow-up, not this slice, exactly like RemoteLifecycle/KubeContexts/
// RemotePaths's own "production wiring is a follow-up" pattern before it.
//
// Class and Child are the two fields Q5 ruled additive over the existing
// {Tool, Verb} shape (Phillip, 2026-09-08, verbatim on tc-vn5z): "add
// optional class/child fields to each existing verbScopedApprovals entry,
// default class='project-tied' ... per ADR 0033's additive-only
// invariant."
//
//   - Class selects the verb's trust-source vocabulary. "" (the zero
//     value) and VerbClassProjectTied ("project-tied") mean the SAME
//     thing, per the ruling's own default, and are the ONLY class this
//     slice's policy (effectpolicy.TrustedCheckoutExec) judges: an entry
//     names a (Tool, Verb) pair as ELIGIBLE for project-tied treatment,
//     but the actual Permitted verdict additionally requires
//     deletable.DiscoveredVerbs (slice 3ag) to independently find Verb
//     literally defined in the invoking project's own Tool-named kind —
//     per Q3's ruling (Phillip, 2026-09-08, verbatim on tc-vn5z):
//     "WORKSPACE vouches only for verbs literally defined in-project;
//     operator data (rules.json) governs everything reached by
//     reference." An operator's entry is therefore not sufficient on its
//     own to approve — it is the ELIGIBILITY gate ("Tool is a build-tool-
//     family tool I want judged this way"), never a substitute for live
//     discovery, and never guessed at when discovery disagrees.
//   - VerbClassInstallableReference ("installable-reference") is the
//     SECOND class judged, added by tc-8og1 item 3 sub-slice 5 (the final
//     sub-slice) for `nix run`'s installable child — see its own doc
//     comment for the full contract. Unlike VerbClassProjectTied, an
//     operator's declaration under this class IS sufficient by itself
//     (no independent discovery exists or is expected to, per Q1).
//   - Any OTHER Class value (still reserved, e.g. a future "wrapper" shape
//     for a child this policy does not yet judge) is recognised as DATA
//     here but not yet judged by any policy: Q2's ruling (Phillip,
//     2026-09-08, verbatim on tc-vn5z) is "there should be a spec for
//     build tools ... if we have an example of a needed extension, we can
//     discuss it then" — i.e. don't pre-design bespoke per-tool/per-class
//     policies ahead of a concrete need. An unrecognised Class therefore
//     makes TrustedCheckoutExec abstain (Unknown), never Permitted and
//     never Forbidden — the same fail-safe direction as an effect kind no
//     policy applies to.
//
// Child is RESERVED for tc-8og1 item 3 sub-slice 4 (Q4: "a new RoleKind
// for 'installable reference'/'opaque-body-by-name' children, or does
// interpreter_subcommand.go's recursion already cover them") to describe
// how a WRAPPER verb's child command is spelled. No policy in this slice
// constructs or reads a VerbChild value.
type VerbScopedApproval struct {
	Tool  string
	Verb  string
	Class string
	Child *VerbChild
}

// VerbChild is RESERVED DATA for a wrapper verb's child-expression
// descriptor (tc-8og1 item 3 sub-slice 4, Q4). Kind and ArgSeparator
// mirror the shape proposed in tc-vn5z's 2026-09-07 design note verbatim
// ("child: { kind: \"installable\", argSeparator: \"--\" }"), so that
// later slice's schema work has a stable field name to target without
// another additive migration. Not yet ruled on, and not consumed by any
// policy in this slice.
type VerbChild struct {
	Kind         string
	ArgSeparator string
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
