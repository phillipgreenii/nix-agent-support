// Package effectpolicy holds the POLICIES of the effect-graph spike and its
// composition root, Evaluate. A policy judges one Effect against a
// PolicyContext; it never sees a command name. Evaluate parses once, builds
// both graphs, folds policy findings into node marks, and folds marks into a
// Decision.
//
// Known import cycle to fix later: internal/hookio is reached transitively
// through effectgraph -> cmdparse; a guard test here asserts no direct import.
package effectpolicy

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/deletable"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// FindingVerdict is a policy's judgement of one effect.
type FindingVerdict int

const (
	// Unknown: the policy applies but cannot decide (dynamic path, unzoned
	// path). The zero value, so an unset finding never reads as permitted.
	Unknown FindingVerdict = iota
	// Permitted: the effect is allowed.
	Permitted
	// Forbidden: the effect is known-bad.
	Forbidden
)

// String returns the deterministic verdict name.
func (v FindingVerdict) String() string {
	switch v {
	case Unknown:
		return "unknown"
	case Permitted:
		return "permitted"
	case Forbidden:
		return "forbidden"
	default:
		return "verdict-invalid"
	}
}

// Finding is one policy's verdict on one effect, with a reason.
type Finding struct {
	Verdict FindingVerdict
	Reason  string
}

// PolicyContext is what a policy may consult besides the effect itself.
// VettedHosts are the hosts the caller trusts, with domain-suffix semantics:
// `example.com` matches the apex and its subdomains, `.internal.example`
// matches subdomains only (the leading dot is what stops `notexample.com`
// from matching `example.com`). RemoteLifecycle is operator configuration for
// an EffectRemote target's lifecycle-verb class (see evalcontract.Request's
// doc comment); RemoteMutation is its only reader. KubeContexts/
// KubeContextDefaultAllow are operator configuration for kubectl's
// per-context policy (see evalcontract.Request's doc comment); KubeContextPolicy
// is their only reader.
type PolicyContext struct {
	PathEval                *patheval.PathEvaluator
	CWD                     string
	VettedHosts             []string
	RemoteLifecycle         map[string]string
	KubeContexts            map[string]evalcontract.KubeContextRule
	KubeContextDefaultAllow []string
	// RemotePaths is OPERATOR CONFIGURATION for the categorized-path
	// override hook a remote-scope path effect consults before the
	// remote-abstain default (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4;
	// wildcard/default entry added slice 3am, tc-vn5z item 4b) — see
	// evalcontract.Request.RemotePaths's doc comment. remotePathGuard is
	// the only reader.
	RemotePaths map[string][]evalcontract.RemotePathRule
	// BuildToolVerbs is OPERATOR CONFIGURATION for the build-tool family
	// (tc-8og1 item 3 sub-slice 3; tc-vn5z Q1-Q5) — see
	// evalcontract.Request.BuildToolVerbs's and VerbScopedApproval's own
	// doc comments. TrustedCheckoutExec is the only reader.
	BuildToolVerbs []evalcontract.VerbScopedApproval
}

// RemoteLifecycleClass returns the operator-configured verdict class for a
// remote lifecycle target ("dolt"), or "" when the operator has configured
// nothing for it (RemoteLifecycle nil or the target absent).
func (c PolicyContext) RemoteLifecycleClass(target string) string {
	return c.RemoteLifecycle[target]
}

// kubeAllows reports whether allow (an Operation-class allow-list) names op.
func kubeAllows(allow []string, op string) bool {
	for _, a := range allow {
		if a == op {
			return true
		}
	}
	return false
}

// KubeContextAllows reports whether Operation class op is permitted for kube
// context name: an explicit KubeContexts entry's own Allow list when name is
// configured, else KubeContextDefaultAllow.
func (c PolicyContext) KubeContextAllows(name, op string) bool {
	if rule, known := c.KubeContexts[name]; known {
		return kubeAllows(rule.Allow, op)
	}
	return kubeAllows(c.KubeContextDefaultAllow, op)
}

// KubeContextKnown reports whether name has an explicit KubeContexts entry
// (as opposed to falling back to KubeContextDefaultAllow).
func (c PolicyContext) KubeContextKnown(name string) bool {
	_, known := c.KubeContexts[name]
	return known
}

// HostVetted reports whether host matches any VettedHosts entry under the
// domain-suffix semantics above. An empty host never matches.
func (c PolicyContext) HostVetted(host string) bool {
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	for _, entry := range c.VettedHosts {
		entry = strings.ToLower(entry)
		switch {
		case entry == "":
		case strings.HasPrefix(entry, "."):
			if strings.HasSuffix(host, entry) {
				return true
			}
		case host == entry || strings.HasSuffix(host, "."+entry):
			return true
		}
	}
	return false
}

// Policy judges effects. Judge reports (finding, true) when the policy applies
// to the effect and (_, false) when it does not.
type Policy interface {
	Name() string
	Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool)
}

// DefaultPolicies returns the spike's policy set. Each policy has exactly one
// concern; the read side is split so a secret-path hit and an unreadable-zone
// hit are distinguishable reasons. StdioIsLocal, ProgramInterpreted and
// EnvAssignment (slice 3f) were added so EffectStdio/EffectProgram/EffectEnv
// are no longer effect kinds "no policy judges" — see judgeNode's fail-closed
// fold in evaluate.go for why an unjudged effect can no longer ride to
// MarkPermitted for free.
//
// The five PATH policies (NoWriteToReadOnlyPath, DeleteAccess,
// NoReadOfSecretPath, NoReadOfUnreadablePath, NoWriteToSecretPath) are
// wrapped in remotePathGuard (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4): a
// single guard, applied here ONCE rather than copy-pasted into each policy's
// own Judge, that makes every one of them abstain by default on a
// REMOTE-scope path effect (Effect.Remote != "") unless the operator's
// categorized-path hook (PolicyContext.RemotePaths) supplies an override —
// see remotePathGuard's own doc comment. No other policy touches EffectPath,
// so no other entry needs wrapping.
func DefaultPolicies() []Policy {
	return []Policy{
		remotePathGuard{NoWriteToReadOnlyPath{}},
		remotePathGuard{DeleteAccess{}},
		remotePathGuard{NoReadOfSecretPath{}},
		remotePathGuard{NoReadOfUnreadablePath{}},
		remotePathGuard{NoWriteToSecretPath{}},
		NetworkAccess{},
		RemoteMutation{},
		KubeContextPolicy{},
		StdioIsLocal{},
		ProgramInterpreted{},
		EnvAssignment{},
		ChdirScoped{},
		TrustedCheckoutExec{},
	}
}

// remotePathGuard wraps a PATH policy so a path effect that lives in a
// REMOTE scope (Effect.Remote != "", stamped by effectgraph's builder OR
// directly by an interpreter that mixes local and remote operands on ONE
// leaf — slice 3ad's scp is the latter; see cmddesc.Effect.Remote's own doc
// comment for both producers) abstains by default, per the
// operator ruling on tc-vn5z (slice 3aa, tc-lc8f item 4g): "for ssh, abstain
// for paths should be thr default." It defers entirely to the wrapped
// policy for a non-remote effect, or for any effect the wrapped policy does
// not apply to at all (ok == false) — the guard adds no NEW applicability,
// it only overrides the VERDICT once the wrapped policy already says it
// applies. Before falling back to the abstain default, it consults
// PolicyContext.RemotePaths (evalcontract.Request.RemotePaths's own doc
// comment carries the design proposal and its provenance) so an operator
// who has explicitly categorized a path on a given host CAN still reach a
// real verdict — see remotePathCategoryFinding for how a category maps to
// one.
//
// This is implemented ONCE, here, rather than inside each of the five path
// policies' own Judge methods: every one of them would otherwise need the
// identical Remote/RemotePaths check inserted at the same point, which is
// exactly the kind of copy-paste this spike's "data first, one place per
// concern" convention exists to avoid.
//
// Slice 3am (tc-vn5z item 4b) extended remotePathOverride's lookup with a
// wildcard/default fallback (evalcontract.RemoteHostWildcard) — see that
// function's own doc comment for the precedence rule — without changing
// this guard's own shape at all: the guard still asks remotePathOverride
// exactly one question ("does anything override the abstain default for
// this effect") and does not itself know whether the answer came from a
// host-specific or a wildcard entry.
type remotePathGuard struct{ inner Policy }

// Name implements Policy.
func (g remotePathGuard) Name() string { return g.inner.Name() }

// Judge implements Policy.
func (g remotePathGuard) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	finding, ok := g.inner.Judge(e, ctx)
	if !ok || e.Kind != cmddesc.EffectPath || e.Remote == "" {
		return finding, ok
	}
	if override, matched := remotePathOverride(e, ctx); matched {
		return override, true
	}
	return Finding{Verdict: Unknown, Reason: "remote path on " + e.Remote + ": no local classification"}, true
}

// remotePathOverride reports the Finding an operator-configured
// RemotePathRule dictates for e, if one matches: RemotePaths[e.Remote] is
// consulted FIRST, in order, first PREFIX match wins (mirroring patheval's
// own longest-listed-first, first-match-wins zone convention). Only when
// that host-specific list produces no match at all — no entry for the host,
// or an entry whose rules never match this effect — does
// RemotePaths[evalcontract.RemoteHostWildcard] get a turn, under the SAME
// first-match-wins rule (tc-vn5z item 4b, ruled 2026-09-08: "the per-host
// categorized-path list gains a wildcard/default entry applying to any host
// not otherwise listed ... in addition to per-host entries"). This
// precedence — a specific host ALWAYS wins over the wildcard for the same
// effect — is this slice's own conservative call for a sub-question the
// ruling did not itself spell out (see evalcontract.RemoteHostWildcard's
// doc comment): it is the ordinary "more specific configuration overrides a
// default" reading, the same direction every other override/default pair in
// this package already takes (e.g. KubeContexts vs KubeContextDefaultAllow).
// matched is false when neither list matches at all (fail closed to the
// ordinary abstain default — see RemotePathRule's own doc comment).
func remotePathOverride(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if f, matched := matchRemotePathRules(ctx.RemotePaths[e.Remote], e); matched {
		return f, true
	}
	return matchRemotePathRules(ctx.RemotePaths[evalcontract.RemoteHostWildcard], e)
}

// matchRemotePathRules walks one host's (or the wildcard's) ordered
// RemotePathRule list for e, first PREFIX match wins, and reports the
// Finding remotePathCategoryFinding maps its Category to. Shared by
// remotePathOverride's two lookups (host-specific, then wildcard) so the
// matching loop itself is written once.
func matchRemotePathRules(rules []evalcontract.RemotePathRule, e cmddesc.Effect) (Finding, bool) {
	for _, rule := range rules {
		if rule.Prefix == "" || !strings.HasPrefix(e.Path, rule.Prefix) {
			continue
		}
		if f, ok := remotePathCategoryFinding(rule.Category, e.Access); ok {
			return f, true
		}
	}
	return Finding{}, false
}

// remotePathCategoryFinding maps one RemotePathRule.Category to the Finding
// for a path effect of the given access class — the RULED taxonomy
// RemotePathRule's own doc comment describes (tc-vn5z item 4b, ruled
// 2026-09-08), mirroring internal/deletable's local Protected/Deletable/
// Writable classification and patheval's read/write zones rather than
// inventing a new shape. ok is false for an unrecognised category (fail
// closed, never guessed at).
//
// "protected" and "secret" are two distinct Category strings (per the
// ruling) but currently produce the IDENTICAL Finding — see RemotePathRule's
// own doc comment for why that is this slice's deliberate, conservative
// choice on a sub-question the ruling left open, not an oversight: a future
// slice MAY diverge them (e.g. an AccessModify carve-out mirroring
// NoWriteToSecretPath's) once a concrete need is brought back for a ruling.
func remotePathCategoryFinding(category string, access cmddesc.PathAccess) (Finding, bool) {
	switch category {
	case "protected", "secret":
		return Finding{Verdict: Forbidden, Reason: "remote path categorized " + category}, true
	case "read-only":
		if access == cmddesc.AccessRead {
			return Finding{Verdict: Permitted, Reason: "remote path categorized read-only"}, true
		}
		return Finding{Verdict: Forbidden, Reason: "write to a remote path categorized read-only"}, true
	case "writable":
		if access == cmddesc.AccessDelete {
			return Finding{Verdict: Unknown, Reason: "delete of a remote path categorized writable needs consent"}, true
		}
		return Finding{Verdict: Permitted, Reason: "remote path categorized writable"}, true
	case "deletable":
		return Finding{Verdict: Permitted, Reason: "remote path categorized deletable"}, true
	default:
		return Finding{}, false
	}
}

// ChdirScoped judges every EffectChdir effect (cd, slice 3o). A statically
// known target is Permitted: the directory change itself is not a hazard —
// what it CHANGES is the working directory every later leaf in the same
// list resolves its relative paths against, and the graph builder has
// already re-based those leaves' path effects before any policy sees them
// (effectgraph's builder threads the CWD per list and subshell; a leaf
// downstream of a cd whose target is a runtime value is marked
// insufficient there, with its relative paths Dynamic). The metadata read
// of the target directory rides as an ordinary EffectPath and is judged by
// the read policies. A Dynamic target is Unknown here as well, so the cd
// node itself abstains, not just its successors.
type ChdirScoped struct{}

// Name implements Policy.
func (ChdirScoped) Name() string { return "chdir-scoped" }

// Judge implements Policy.
func (ChdirScoped) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectChdir {
		return Finding{}, false
	}
	if e.Dynamic {
		reason := "cd target is a runtime value; later commands' working directory is unknown"
		if e.Detail != "" {
			reason = "cd target is the " + e.Detail + "; later commands' working directory is unknown"
		}
		return Finding{Verdict: Unknown, Reason: reason}, true
	}
	return Finding{Verdict: Permitted, Reason: "later commands in this list are judged against the new working directory"}, true
}

// StdioIsLocal judges every EffectStdio effect: always Permitted. A standard
// stream carries no destination by itself — consuming or producing stdin/
// stdout/stderr is not, on its own, a hazard. WHERE that content actually
// FLOWS (into a file via a redirect, out to the network, into another
// command via a pipe) is judged by the effects those other actions
// themselves emit and by the graph-level policies that walk Flow edges
// (NoContentFlowToUnvettedNetwork in graphpolicy.go) — never by this policy.
// Before slice 3f, EffectStdio was simply an effect kind no policy judged,
// which worked out to the same Permitted-by-omission outcome for the many
// ordinary commands that read/write a stream (`cat README.md`'s stdout, for
// instance); this policy makes that outcome an explicit, named finding
// instead of a silent gap in the fold.
type StdioIsLocal struct{}

// Name implements Policy.
func (StdioIsLocal) Name() string { return "stdio-is-local" }

// Judge implements Policy.
func (StdioIsLocal) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectStdio {
		return Finding{}, false
	}
	return Finding{Verdict: Permitted, Reason: "stream flow is judged by graph-level policies, not here"}, true
}

// ProgramInterpreted judges every EffectProgram effect: always Permitted.
// This relies on an invariant enforced upstream, in
// cmddesc.interpState.program (internal/cmddesc/interpreter.go): a Program
// effect whose text is a live expansion, or whose dialect is unrecognised,
// or whose dialect interpretation itself came back insufficient, already
// marks the node's builder-level result insufficient — and judgeNode's
// switch in evaluate.go preserves a builder-set MarkInsufficient ahead of
// any node-level policy finding (Forbidden aside). So a Program effect that
// reaches THIS policy's Judge on a node that is not already insufficient is,
// by construction, one the interpreter understood well enough to model.
// Judging it Permitted here records that the effect is accounted for; it
// does not itself vouch for arbitrary program text — that vouching already
// happened, or the node would not still be eligible for Permitted at all.
type ProgramInterpreted struct{}

// Name implements Policy.
func (ProgramInterpreted) Name() string { return "program-interpreted" }

// Judge implements Policy.
func (ProgramInterpreted) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectProgram {
		return Finding{}, false
	}
	return Finding{Verdict: Permitted, Reason: "dialect interpretation already vouched for this program (see cmddesc.interpState.program)"}, true
}

// envInjectorVars, envInjectorAskVars and envAskVars are copied — name and
// one-line rationale per class — from internal/rules/envvars.go's
// injectorVars / injectorAskVars / askVars (the LIVE engine's env-var
// guard), not imported: a policy here never sees a command name or a
// cmdparse.ParsedCommand, so it cannot reuse a rule built around either, and
// this package's own doc comment already forbids reaching into
// internal/hookio's transitive closure that internal/rules/envvars sits in.
// A later migration that lets a policy consult the live rule tables directly
// should delete ONE of these two copies rather than let them diverge
// silently — see envvars.go's own doc comments for the full measured
// history behind each entry; only the one-line summary is repeated here.
var (
	// envInjectorVars: assignment is GUARANTEED to be a code-injection /
	// library-preload vector regardless of value (hijacks the dynamic
	// linker or a shell's startup before the "safe-looking" executable
	// ever runs) — envvars.go's injectorVars.
	envInjectorVars = map[string]bool{
		"LD_PRELOAD":            true,
		"DYLD_INSERT_LIBRARIES": true,
		"LD_LIBRARY_PATH":       true,
		"DYLD_LIBRARY_PATH":     true,
		"BASH_ENV":              true,
		"ZDOTDIR":               true,
	}
	// envInjectorAskVars: same injection family as envInjectorVars, but the
	// NAME also collides with everyday project-variable traffic, so the
	// live rule downgrades a name-only Reject to a user-overridable Ask —
	// envvars.go's injectorAskVars.
	envInjectorAskVars = map[string]bool{
		"ENV": true,
	}
	// envAskVars: dangerous but not guaranteed-unsafe for every value (a
	// legitimate PATH extension, a HOME override); the live rule's verdict
	// depends on the VALUE (preservesCallerValue / the hermetic-HOME
	// relief) and asks a human when it cannot prove the value safe —
	// envvars.go's askVars.
	envAskVars = map[string]bool{
		"PATH": true,
		"HOME": true,
	}
)

// EnvAssignment judges every EffectEnv effect. A read (EnvSet == false) is
// always Permitted: reading a variable's current value cannot itself change
// what the command touches. A set is Unknown when the effect records the
// NAME as a runtime expansion (Dynamic) — the policy cannot tell which
// variable is actually affected; investigated for this slice and found to
// be reachable only in theory today (see the EnvAssignment doc trailer
// below), but checked defensively since Effect already carries the field.
// Otherwise a set is classified by its statically known NAME against the
// three data sets above: an injector name is Forbidden; an injector-ask name
// is Unknown unconditionally (envvars.go's injectorAskVars split is itself
// value-independent — see that map's own doc — so there is no value relief
// to port here); an ask name (PATH, HOME) tries the hermetic-value reliefs
// below FIRST and only falls back to Unknown when none apply; any other
// static name is outside this slice's vocabulary of known-bad names and is
// Permitted.
//
// # Value modeling (slice 3an, tc-8og1 item 5; tc-ife3 item 5, RULED
// 2026-09-08: "Port the value-relief logic now")
//
// Ported from internal/rules/envvars.go — the LIVE engine's env-var guard —
// by COPY, not import, for the same reason envInjectorVars/
// envInjectorAskVars/envAskVars above already are (see their own doc
// comment): a policy here never sees a cmdparse.ParsedCommand, so it cannot
// reuse a rule built around one. Three of envvars.go's Approve predicates
// are ported, each by its CORE shape only:
//
//   - envPreservesCallerValue mirrors preservesCallerValue's EXTEND shape
//     (pg2-0q99a): the value keeps the caller's own value ($NAME/${NAME}) as
//     one whole ':'-separated component, and every OTHER component is a
//     literal static absolute path.
//   - envHermeticReplacement mirrors isHermeticEnvReplacement (pg2-d71my):
//     under a leaf's own `env -i`/`env --ignore-environment` (EnvCleared),
//     a REPLACEMENT value is safe when every ':'-component is a literal
//     static absolute path — there is no caller value left to preserve, so
//     this is a different, independent shape from the one above.
//   - envFreshHomeTempDir mirrors isHermeticHomeReplacement's `mktemp -d`
//     idiom ONLY (pg2-d71my), via cmdparse.IsFreshTempDirAssignment — the
//     one self-contained idiom of that predicate's three (see its own doc
//     in incommandvars.go); HOME only, matching the live rule's own scope.
//
// Deliberately NOT ported — each is a LATER, narrower widening layered onto
// the base logic above by a separate operator ruling, and each needs
// leaf-wide context (an earlier leaf's own assignments, or the root
// expression's later leaves) that a single Effect does not carry; porting
// them is a follow-up, not part of this slice:
//
//   - pg2-qhhil's in-command-assigned-$VAR component widening;
//   - pg2-kzqw2's certified-safe-substitution component widening (any value
//     carrying an embedded command/process substitution is conservatively
//     EXCLUDED from all three predicates here — see envPreservesCallerValue
//     and envHermeticReplacement's own doc — rather than partially modeled);
//   - pg2-7sqk8's consumption-scoped relief (mechanisms 1/2) — moot here in
//     any case: that relief exists ONLY to work around envvars.go's
//     first-match-wins CHAIN (an early decisive Approve there short-circuits
//     every later rule for the same LEAF), which this spike's per-effect,
//     fail-closed NODE fold does not share — approving one EffectEnv finding
//     can never suppress another effect's own finding on the same node, so
//     condition 3 of envvars.go's own Approve contract (assignmentIsWholeLeaf)
//     has no analogue to port either;
//   - pg2-sir2l's HOME rm+mkdir/bare-mkdir freshness widening, and its
//     separate tightening of HOME's OWN unclassified fallback from Ask to
//     Reject — that fallback tightening is a different ruling than this
//     one (this slice only ADDS Approve cases; the pre-existing Unknown
//     fallback for everything else is intentionally left unchanged).
//
// A value that fails every ported predicate falls to the SAME Unknown this
// policy already returned before this slice — an accepted, narrower
// spike-stricter divergence (see testdata/agreement.txt), never a safety
// gap: this policy still never silently Approves an ask name on a value it
// could not verify.
//
// Investigated-and-not-built note on Dynamic NAMEs: a NAME effect.Dynamic
// bit is not populated anywhere today (internal/cmddesc/effect.go's Dynamic
// field is shared across Path/Net/Remote/Env but no Env-effect construction
// site sets it — internal/effectgraph/build.go's leaf.EnvVars loop and
// internal/cmddesc/interpreter.go's envAssign both leave it false). That
// was confirmed not to be a live gap rather than left unnoticed: the two
// production paths that ever produce an EffectEnv Set both guarantee a
// static NAME by construction — a leading `NAME=VALUE` or `export
// NAME=VALUE` assignment word is recognised by cmdparse's SHELL GRAMMAR
// (identifier "=" word), which cannot itself contain an expansion, so
// internal/cmdparse's EnvAssignment.Name is never dynamic on that path; the
// other path (export's own KindEnvAssign operand role, for a token the
// grammar-level lift did not recognise as a plain assignment word, e.g. a
// quoted `"$NAME"=x`) already fails the WHOLE NODE closed in
// interpreter.go's envAssign — `st.fail("env assignment ... is a runtime
// expansion")` — before an Effect is even built, verified empirically
// against `export "$NAME"=x` before this slice's changes (Abstain, node
// insufficient, not Approve). So wiring a NAME-dynamic bit through
// build.go/interpreter.go was deliberately NOT done for this slice — it
// would touch files outside internal/effectpolicy for a case that cannot
// currently occur — and the `e.Dynamic` check below is retained purely as a
// forward-compatible guard against a future EffectEnv producer that does
// not share this invariant.
type EnvAssignment struct{}

// Name implements Policy.
func (EnvAssignment) Name() string { return "env-assignment" }

// envIsStaticAbsolutePath mirrors internal/rules/envvars.go's
// isStaticAbsolutePath: a literal ':'-delimited PATH/HOME component is safe
// only if it starts with '/' and contains nothing that could introduce an
// expansion, re-quote the value, or corrupt a downstream reason string. An
// EMPTY component (the CWD hazard: an empty PATH entry means "current
// directory" to the shell) is rejected by the leading-'/' requirement, same
// as envvars.go.
func envIsStaticAbsolutePath(component string) bool {
	if !strings.HasPrefix(component, "/") {
		return false
	}
	for i := 0; i < len(component); i++ {
		switch c := component[i]; {
		case c == '$' || c == '`' || c == '"' || c == '\'' || c == '\\':
			return false
		case c < 0x20 || c == 0x7f:
			return false
		}
	}
	return true
}

// envValueHasSubstitution reports whether value embeds any top-level
// command/process substitution — the conservative gate this port uses in
// place of envvars.go's splitPathValueComponents (pg2-kzqw2's
// substitution-boundary-aware split, deliberately not ported — see
// EnvAssignment's own doc comment). A value with ANY substitution is
// excluded from every predicate below rather than partially modeled: a
// naive strings.Split(value, ":") on a value like `$(date +%H:%M)` could
// otherwise mistake the substitution body's OWN ':' for a component
// boundary. This can only make the port MISS a value production would
// Approve (a false negative, landing on the pre-existing Unknown), never
// approve something it should not.
func envValueHasSubstitution(value string) bool {
	return len(cmdparse.EnumerateSubstitutions(value)) > 0
}

// envPreservesCallerValue mirrors internal/rules/envvars.go's
// preservesCallerValue CORE shape (pg2-0q99a's EXTEND form) only — see
// EnvAssignment's own doc comment for what is deliberately not ported.
func envPreservesCallerValue(name, value string, expansion cmdparse.ExpansionKind) bool {
	if expansion != cmdparse.ExpansionVarRef && expansion != cmdparse.ExpansionUnknown {
		return false
	}
	if envValueHasSubstitution(value) {
		return false
	}
	literal, ok := cmdparse.LiteralAssignmentValueText(value)
	if !ok {
		return false
	}
	selfRef, braceRef := "$"+name, "${"+name+"}"
	preserved := false
	for _, component := range strings.Split(literal, ":") {
		switch {
		case component == selfRef || component == braceRef:
			preserved = true
		case envIsStaticAbsolutePath(component):
			// an ordinary static absolute path component: acceptable.
		default:
			return false
		}
	}
	return preserved
}

// envHermeticReplacement mirrors internal/rules/envvars.go's
// isHermeticEnvReplacement: under a leaf's own `env -i`/
// `env --ignore-environment` (the caller checks EnvCleared before calling
// this), a REPLACEMENT value is safe when every ':'-delimited component
// (or the whole value, for a non-list-shaped value like a bare HOME) is a
// literal static absolute path — there is no caller value left to preserve,
// so this is independent of envPreservesCallerValue's self-reference check.
func envHermeticReplacement(value string) bool {
	if envValueHasSubstitution(value) {
		return false
	}
	literal, ok := cmdparse.LiteralAssignmentValueText(value)
	if !ok || literal == "" {
		return false
	}
	for _, component := range strings.Split(literal, ":") {
		if !envIsStaticAbsolutePath(component) {
			return false
		}
	}
	return true
}

// envFreshHomeTempDir mirrors internal/rules/envvars.go's
// isHermeticHomeReplacement's `mktemp -d` idiom ONLY
// (cmdparse.IsFreshTempDirAssignment) — HOME=$(mktemp -d), a fresh,
// session-unique directory nothing could have pre-staged content in. The
// rm+mkdir/bare-mkdir widening (pg2-sir2l) is deliberately not ported — see
// EnvAssignment's own doc comment.
func envFreshHomeTempDir(value string, expansion cmdparse.ExpansionKind) bool {
	return cmdparse.IsFreshTempDirAssignment(cmdparse.EnvAssignment{Value: value, Expansion: expansion})
}

// Judge implements Policy.
func (EnvAssignment) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectEnv {
		return Finding{}, false
	}
	if !e.EnvSet {
		return Finding{Verdict: Permitted, Reason: "env read"}, true
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "env NAME is a runtime expansion"}, true
	}
	switch {
	case envInjectorVars[e.EnvName]:
		return Finding{Verdict: Forbidden, Reason: "injector variable (loader/exec hijack)"}, true
	case envInjectorAskVars[e.EnvName]:
		return Finding{Verdict: Unknown, Reason: "injector-ask variable; live rule's verdict depends on value, which this slice does not model"}, true
	case envAskVars[e.EnvName]:
		if envPreservesCallerValue(e.EnvName, e.EnvValue, e.EnvExpansion) {
			return Finding{Verdict: Permitted, Reason: "sensitive env var preserves the caller's value and adds only static absolute paths (ported from envvars.go's preservesCallerValue core shape)"}, true
		}
		if e.EnvCleared && envHermeticReplacement(e.EnvValue) {
			return Finding{Verdict: Permitted, Reason: "sensitive env var is a static replacement under a hermetic env -i invocation (ported from envvars.go's isHermeticEnvReplacement)"}, true
		}
		if e.EnvName == "HOME" && envFreshHomeTempDir(e.EnvValue, e.EnvExpansion) {
			return Finding{Verdict: Permitted, Reason: "HOME replacement is grounded in a mktemp -d fresh temp dir (ported from envvars.go's isHermeticHomeReplacement mktemp -d idiom)"}, true
		}
		return Finding{Verdict: Unknown, Reason: "ask variable; value did not match a modeled hermetic-approval shape"}, true
	default:
		return Finding{Verdict: Permitted, Reason: "static name outside the known-bad vocabulary"}, true
	}
}

// NoWriteToReadOnlyPath applies to every write-class path effect EXCEPT
// delete (create, modify, truncate — PathAccess.IsWrite minus
// AccessDelete), whatever command produced it: Forbidden when patheval's
// zone (or a sandbox denyWrite entry) forbids writing, Unknown when the
// path is dynamic or unzoned, Permitted otherwise.
//
// Deletes are carved out (tc-z806.1) so that a delete effect gets EXACTLY
// ONE finding, from DeleteAccess below, which re-implements this policy's
// zone/denyWrite checks as its own protection ladder and then goes further
// (writable is not enough to delete). Had this policy kept judging deletes
// too, a writable-but-not-deletable path would carry a Permitted finding
// from here and an Unknown one from DeleteAccess; the fold's outcome would
// be the same (Unknown wins), but the reason text would name whichever
// policy happened to run first and the two policies would silently
// disagree about the same effect. With the carve-out, a policy set that
// omits DeleteAccess leaves deletes UNJUDGED, which judgeNode's fail-closed
// fold turns into Insufficient — never Permitted — so the carve-out cannot
// widen anything.
type NoWriteToReadOnlyPath struct{}

// Name implements Policy.
func (NoWriteToReadOnlyPath) Name() string { return "no-write-to-read-only-path" }

// Judge implements Policy.
func (NoWriteToReadOnlyPath) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectPath || !e.Access.IsWrite() || e.Access == cmddesc.AccessDelete {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "path is a runtime expansion"}, true
	}
	if ctx.PathEval == nil {
		return Finding{Verdict: Unknown, Reason: "no path evaluator"}, true
	}
	if ctx.PathEval.IsDenyWrite(e.Path) {
		return Finding{Verdict: Forbidden, Reason: "path is denyWrite"}, true
	}
	access := ctx.PathEval.Evaluate(e.Path)
	switch {
	case access.CanWrite():
		return Finding{Verdict: Permitted, Reason: "zone " + access.String()}, true
	case access == patheval.PathUnknown:
		return Finding{Verdict: Unknown, Reason: "zone " + access.String()}, true
	default:
		return Finding{Verdict: Forbidden, Reason: "write to " + access.String() + " zone"}, true
	}
}

// DeleteAccess has one concern: EffectPath effects with Access ==
// AccessDelete. It is the policy half of the operator-ruled delete model
// (Phillip, 2026-09-07, design bead tc-z806 — verbatim there and in
// internal/deletable's package doc): "rm would be rejected for paths which
// aren't at least writable. for writable it should abstain (by default) and
// if deletable, then it can approve (by default). in both cases, there could
// be other rules which change the default."
//
// Ladder, protections first, so a gitignored `.env` or a `~/.ssh` key is
// Forbidden before deletability is ever a question:
//
//  1. dynamic path                          -> Unknown
//  2. no evaluator                          -> Unknown
//  3. sandbox denyWrite or denyRead entry   -> Forbidden
//  4. secret path (raw or resolved)         -> Forbidden
//  5. zone reject or read-only              -> Forbidden (not writable)
//  6. the path IS a worktree root
//     (deletable.AtWorktreeRoot, tc-lc8f
//     item 4a — see below)                  -> judged by worktree STATE, not
//     the Protected category: clean -> Permitted, dirty -> Forbidden, clean
//     but ignored files present -> Unknown, state undeterminable -> Unknown
//  7. the path IS a pn workforest SET
//     CONTAINER (deletable.
//     IsWorkforestSetContainer, tc-8og1
//     item 1 — see below)                    -> judged by the WORST state
//     among its member worktree slots: any member dirty -> Forbidden, every
//     member clean -> Permitted, otherwise -> Unknown
//  8. deletable.Classify (workspace
//     declarations, tc-z806.3):
//     Protected (.git, .worktrees, a pn
//     workforest set's own workforests_dir
//     entry not itself a set container,
//     ~/.ssh)                                -> Forbidden
//     Deletable (gitignored, build/ of a
//     gradle project, ~/.cache, go's build
//     cache, a temp root)                   -> Permitted, even where the
//     zone is unknown: a declaration that a path is disposable vouches for
//     removing it
//  9. zone unknown, no declaration          -> Unknown   (not known writable)
//
// 10. writable, not deletable              -> Unknown   ("needs consent")
//
// Step 3 includes denyRead deliberately: the ruling says protections above
// this rule win, and a path the operator has marked unreadable is protected
// whether or not it is also marked unwritable — refusing to remove it is the
// fail-safe reading. Step 4 uses the same secretRead helper the read-side
// policies use, so a secret is named the same way whichever access class
// touches it. Step 5 runs BEFORE steps 6/7/8, so a declared-deletable cache
// that sits in a read-only zone (go's module cache under patheval's
// `~/go/pkg`) is still Forbidden — the zone is the older, narrower decision
// and wins, and a worktree sitting in a read-only zone is never approved
// merely for being clean.
//
// Step 6 (worktree state) — operator ruling (Phillip, 2026-09-07, verbatim,
// recorded on bead tc-vn5z): "removing a worktree is fine, assuming it osnt
// dirty. well, a completely clean one can be approved. use rejext if there
// are dirtt workspace. abstain for if clean but ignored files exist." This
// SUPERSEDES slice 3l's unconditional Protected on a `.worktrees` entry (git
// kind) and a pn workspace's workforests_dir entry (pn kind) — but ONLY for
// the worktree ROOT ITSELF, as a unit: `.git/` (always a directory for the
// primary/canonical clone) stays Protected via step 8 exactly as before, and
// so does any path INSIDE a worktree that is not the worktree's own root
// (deletable.AtWorktreeRoot's doc comment). Step 6 runs BEFORE steps 7/8 so a
// worktree root's fate is decided by its state, never by the blanket
// category deletable.Classify would otherwise assign it.
//
// Step 7 (workforest set state, tc-8og1 item 1) extends step 6's ruling to a
// pn workforest SET CONTAINER — `<workforests_dir>/<set>`, one level above
// the per-repo worktree slots step 6 already judges
// (`<workforests_dir>/<set>/<repo>`, two levels under workforests_dir; see
// deletable.IsDeclaredWorktreeSlot's doc comment for the depth history). A
// set container has no `.git` of its own to probe (each MEMBER repo does),
// so it is judged by the worst state among its members instead
// (deletable.ProbeWorkforestSetState) rather than by ProbeWorktreeState
// directly — never treated as a slot in its own right, and never falling
// through to step 8's blanket Protected declaration on the workforests_dir
// entry.
//
// "By default" in the ruling means a consumer rule ABOVE this policy may
// widen (a project that declares its build/ disposable) or narrow; this
// policy never sees a command name and never decides more than the effect
// in front of it. Breadth (`-r`, a whole tree vs one file) is NOT a factor,
// by the same ruling: the class is per path.
type DeleteAccess struct{}

// Name implements Policy.
func (DeleteAccess) Name() string { return "delete-access" }

// Judge implements Policy.
func (DeleteAccess) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectPath || e.Access != cmddesc.AccessDelete {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "path is a runtime expansion"}, true
	}
	if ctx.PathEval == nil {
		return Finding{Verdict: Unknown, Reason: "no path evaluator"}, true
	}
	if ctx.PathEval.IsDenyWrite(e.Path) {
		return Finding{Verdict: Forbidden, Reason: "path is denyWrite"}, true
	}
	if ctx.PathEval.IsDenyRead(e.Path) {
		return Finding{Verdict: Forbidden, Reason: "path is denyRead (protected paths are never deletable)"}, true
	}
	if reason, secret := secretRead(e.Path, ctx); secret {
		return Finding{Verdict: Forbidden, Reason: reason + " (protected paths are never deletable)"}, true
	}
	access := ctx.PathEval.Evaluate(e.Path)
	if access == patheval.PathReject || access == patheval.PathReadOnly {
		return Finding{Verdict: Forbidden, Reason: "delete of " + access.String() + " zone"}, true
	}
	if abs := ctx.PathEval.ResolvePath(e.Path); abs != "" {
		if deletable.AtWorktreeRoot(abs) {
			return worktreeRemovalFinding(abs)
		}
		if deletable.IsWorkforestSetContainer(abs) {
			return workforestSetRemovalFinding(abs)
		}
	}
	class, why := deletable.Classify(ctx.PathEval, e.Path)
	switch class {
	case deletable.Protected:
		return Finding{Verdict: Forbidden, Reason: why}, true
	case deletable.Deletable:
		return Finding{Verdict: Permitted, Reason: why}, true
	case deletable.Writable:
		return Finding{Verdict: Unknown, Reason: "delete of a writable path needs consent (" + why + ")"}, true
	default:
		return Finding{Verdict: Unknown, Reason: why}, true
	}
}

// worktreeRemovalFinding maps deletable.ProbeWorktreeState(abs) to a Finding
// per the operator ruling recorded on DeleteAccess's own doc comment (step
// 6): clean -> Permitted, dirty -> Forbidden, clean-but-ignored -> Unknown,
// undeterminable -> Unknown (the probe's error, when there is one, is folded
// into the reason so a caller can tell "not actually a git repository"
// apart from "clean").
func worktreeRemovalFinding(abs string) (Finding, bool) {
	state, err := deletable.ProbeWorktreeState(abs)
	switch state {
	case deletable.WorktreeClean:
		return Finding{Verdict: Permitted, Reason: "worktree root is clean (no tracked modifications, no untracked or ignored files): " + abs}, true
	case deletable.WorktreeDirty:
		return Finding{Verdict: Forbidden, Reason: "worktree root is dirty (tracked modifications, staged changes, or untracked files): " + abs}, true
	case deletable.WorktreeCleanIgnored:
		return Finding{Verdict: Unknown, Reason: "worktree root is clean but has ignored files: " + abs}, true
	default:
		reason := "worktree state could not be determined: " + abs
		if err != nil {
			reason += " (" + err.Error() + ")"
		}
		return Finding{Verdict: Unknown, Reason: reason}, true
	}
}

// workforestSetRemovalFinding maps deletable.ProbeWorkforestSetState(abs) —
// the WORST WorktreeState among a pn workforest set container's own member
// slots — to a Finding, per the SAME operator ruling worktreeRemovalFinding
// applies to a single slot (tc-8og1 item 1, extending tc-vn5z's "removing a
// worktree is fine, assuming it osnt dirty" to a set of them): any member
// dirty -> Forbidden (Reject), every member clean -> Permitted (Approve),
// otherwise (a mix, an ignored-only member, an undeterminable member, or no
// members at all) -> Unknown (Abstain). ProbeWorkforestSetState never
// returns WorktreeCleanIgnored itself (its own doc comment folds that case
// into WorktreeUnknown), so that branch here is unreachable in practice but
// kept for the same reason worktreeRemovalFinding keeps it: a Finding
// mapping should not silently miscategorize a WorktreeState value it wasn't
// specifically told to expect.
func workforestSetRemovalFinding(abs string) (Finding, bool) {
	state, err := deletable.ProbeWorkforestSetState(abs)
	switch state {
	case deletable.WorktreeClean:
		return Finding{Verdict: Permitted, Reason: "workforest set is clean (every member worktree is clean): " + abs}, true
	case deletable.WorktreeDirty:
		return Finding{Verdict: Forbidden, Reason: "workforest set has a dirty member worktree: " + abs}, true
	case deletable.WorktreeCleanIgnored:
		return Finding{Verdict: Unknown, Reason: "workforest set has a clean-but-ignored member worktree: " + abs}, true
	default:
		reason := "workforest set member states could not be determined, or a member is not all-clean: " + abs
		if err != nil {
			reason += " (" + err.Error() + ")"
		}
		return Finding{Verdict: Unknown, Reason: reason}, true
	}
}

// NoReadOfSecretPath has one concern: a read of a SECRET path (secretpath,
// on the raw or the resolved path) is Forbidden. A dynamic path is Unknown
// (it might resolve to a secret). Any other read is outside this policy's
// concern — it does not apply, and readability is NoReadOfUnreadablePath's
// job.
type NoReadOfSecretPath struct{}

// Name implements Policy.
func (NoReadOfSecretPath) Name() string { return "no-read-of-secret-path" }

// Judge implements Policy.
func (NoReadOfSecretPath) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectPath || e.Access != cmddesc.AccessRead {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "path is a runtime expansion"}, true
	}
	if reason, secret := secretRead(e.Path, ctx); secret {
		return Finding{Verdict: Forbidden, Reason: reason}, true
	}
	return Finding{}, false
}

// secretRead reports whether a statically known path is a secret path, on
// the raw text or on patheval's resolution of it. It is shared by the
// node-level secret policy and the graph-level flow policy so both name a
// secret the same way.
//
// tc-lc8f item 3z (deletable.go's "# NON-SECRET declarations" doc comment
// carries the operator rulings): a GenericSecretsDir match (the bare,
// role-describing `secrets` path component — secretpath.Classify) is no
// longer forbidden UNCONDITIONALLY. It is forbidden unless the project's
// own workspace declaration (deletable.NonSecret) vouches for the path as
// non-secret. A WellKnownSecret match (a specific credential store or file
// — `.ssh`/`.gnupg`, the credential basenames, `*.pem`/`*.key`) is
// unaffected: it stays forbidden regardless of any project declaration,
// exactly as before this slice.
func secretRead(p string, ctx PolicyContext) (string, bool) {
	if reason, secret := classifiedSecretRead(p, ctx, ""); secret {
		return reason, true
	}
	if ctx.PathEval != nil {
		if resolved := ctx.PathEval.ResolvePath(p); resolved != "" {
			if reason, secret := classifiedSecretRead(resolved, ctx, " (resolved)"); secret {
				return reason, true
			}
		}
	}
	return "", false
}

// classifiedSecretRead applies secretpath.Classify to candidate and decides
// whether it is a secret read: WellKnownSecret is unconditionally forbidden
// (secretRead's doc explains why); GenericSecretsDir is forbidden UNLESS
// deletable.NonSecret declares the path non-secret. suffix is appended to
// the reason text (secretRead's pre-existing "(resolved)" annotation for
// the second, symlink-resolved check).
//
// candidate is mapped through stripGoPackagePattern before it is handed to
// deletable.NonSecret (never before secretpath.Classify — Classify matches
// the `secrets` component in "./internal/rules/secrets/..." exactly as
// well as in the stripped form, so stripping earlier would buy nothing and
// would change what every OTHER caller of Classify sees): a `go
// test`/`go build`/... package-pattern operand names a directory in Go's
// own package-pattern SYNTAX, not in a form patheval/deletable's path
// machinery or a `git ls-files` probe can resolve directly.
func classifiedSecretRead(candidate string, ctx PolicyContext, suffix string) (string, bool) {
	switch secretpath.Classify(candidate) {
	case secretpath.WellKnownSecret:
		return "secret path" + suffix, true
	case secretpath.GenericSecretsDir:
		if nonSecret, _ := deletable.NonSecret(ctx.PathEval, stripGoPackagePattern(candidate)); nonSecret {
			return "", false
		}
		return "secret path" + suffix, true
	default:
		return "", false
	}
}

// stripGoPackagePattern maps a Go package-pattern operand's "/..." suffix
// (e.g. "./internal/rules/secrets/..." -> "./internal/rules/secrets") to
// the directory it names. Deliberately done HERE, in the policy layer, not
// in cmddesc (which would have to know this is a secrecy concern rather
// than a generic path fact) or in deletable (whose Kind declarations are
// go-agnostic by design — see deletable.go's doc comment): this mapping is
// specific to how ONE tool family's operand SYNTAX maps onto a path, which
// is a policy-layer judgment call, not a project-specification concern nor
// a cmddesc parsing concern.
func stripGoPackagePattern(path string) string {
	return strings.TrimSuffix(path, "/...")
}

// NoWriteToSecretPath is the WRITE-side counterpart of NoReadOfSecretPath
// (slice 3ab, tc-lc8f item 4h; tc-vn5z item 5): a write-class path effect
// (create, modify, or truncate — Access.IsWrite() minus AccessDelete, the
// same carve-out NoWriteToReadOnlyPath documents on its own doc comment)
// whose target is a WELL-KNOWN secret store is Forbidden, exactly as
// unconditionally as a READ of the same path already is — EXCEPT for the
// AccessModify carve-out documented below (slice 3af).
//
// Deletes are carved out for the identical reason NoWriteToReadOnlyPath
// carves them out: DeleteAccess already judges every AccessDelete path
// effect, including its own secretRead check (its doc comment's ladder step
// 4), so a delete effect must get EXACTLY ONE finding rather than two
// policies silently agreeing (or, worse, disagreeing on the reason text).
//
// The GenericSecretsDir/WellKnownSecret split mirrors secretRead's own
// split (slice 3z, tc-lc8f item 3z: "Tracked-by-git means non-secret") but
// is NOT a verbatim copy of its verdicts: a WellKnownSecret match (a
// specific credential store or file — .ssh/.gnupg, the credential
// basenames, *.pem/*.key) stays unconditionally Forbidden for AccessCreate
// and AccessTruncate, matching the read side exactly, because writing NEW
// content into (or truncating) a named credential store is exactly as
// disqualifying as reading it — either way the operator's key material is
// being touched by an agent action nobody reviewed. A bare GenericSecretsDir
// match (the role-describing "secrets" path component with no project
// declaration vouching for it), however, is Unknown here rather than
// Forbidden: unlike a read, which IRREVERSIBLY discloses whatever is
// already there, a write to an unproven "secrets"-named location has not
// yet disclosed or destroyed anything — it is exactly the "needs consent"
// shape this policy set already gives an ordinary ambiguous write
// (NoWriteToReadOnlyPath's own "zone unknown" case, DeleteAccess's
// "writable, not deletable" case), not the irrevocable-harm shape a secret
// read or a well-known-secret write is. When deletable.NonSecret vouches
// for the path (a tracked, non-gitignored file — the SAME declaration
// secretRead already consults), this policy does not apply at all, and an
// unrelated write policy (NoWriteToReadOnlyPath) is free to reach its own,
// ordinary zone-based verdict.
//
// # AccessModify carve-out (tc-8og1 item 2, slice 3af)
//
// 3ab's corpus root-cause found `git rm <path>/.env` Rejecting: gitRmSchema
// (registry.go) models every git-rm/git-mv positional as PathModify, not
// PathDelete, per the tc-z806 operator ruling "git rm can be considered the
// same as edit because the value can be retrieved from git history" — but
// this policy still Forbade a WellKnownSecret PathModify unconditionally,
// so a TRACKED `.env` (whose content is, by that same ruling, recoverable
// from history — exactly the reasoning tc-z806 gave for demoting git rm
// from PathDelete to PathModify in the first place) could never be
// git-rm'd or git-mv'd. deletable.NonSecret already answers "is this path's
// content recoverable/non-secret because git tracks it" for the read side
// (slice 3z); AccessModify is the one write access class the tc-z806
// ruling itself says shares that recoverability property, so this is the
// SAME declaration, applied to the ONE access class the ruling covers.
//
// The carve-out is deliberately narrower than "AccessModify + WellKnownSecret
// is always Unknown-by-default like GenericSecretsDir": it only ADDS an
// escape hatch when deletable.NonSecret returns true (tracked, not
// gitignored); an UNTRACKED WellKnownSecret modify stays Forbidden, exactly
// as before this slice. Generalising the untracked branch to Unknown too
// was considered and rejected: the policy layer has no way to tell "this
// Modify effect came from git rm/mv" from "this Modify effect came from an
// ordinary in-place edit" (Effect carries no command provenance, by this
// spike's own "nothing branches on a command name" design) — sed's -i is
// modeled as the identical PathModify access class (registry_breadth.go),
// and the existing golden `sed_i_ssh_config` ("sed -i 's/a/b/' ~/.ssh/config"
// -> Reject) exercises exactly that case: ~/.ssh/config is WellKnownSecret,
// AccessModify, and untracked (no git workspace at $HOME in the fixture).
// Loosening untracked-Modify to Unknown would flip that existing Reject to
// Abstain — a real regression against an unrelated, already-settled golden,
// not something this slice's corpus finding calls for. So: tracked ->
// Approve (this slice's fix); untracked -> unchanged Forbidden (deliberately
// NOT touched). See golden_test.go's dotenv-verdict-table comment for the
// full tracked/untracked x rm/git-rm/git-mv matrix this produces.
type NoWriteToSecretPath struct{}

// Name implements Policy.
func (NoWriteToSecretPath) Name() string { return "no-write-to-secret-path" }

// Judge implements Policy.
func (NoWriteToSecretPath) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectPath || !e.Access.IsWrite() || e.Access == cmddesc.AccessDelete {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "path is a runtime expansion"}, true
	}
	return secretWrite(e.Path, e.Access, ctx)
}

// secretWrite reports the Finding a write to a statically known path
// deserves, on the raw path text or on patheval's resolution of it —
// mirroring secretRead's own raw-then-resolved two-pass shape exactly (see
// secretRead's doc comment) so a symlinked secret store is caught the same
// way whichever access class touches it. ok is false when neither the raw
// nor the resolved path is any kind of secret path at all, letting the
// write fall through to whichever other write policy (NoWriteToReadOnlyPath)
// has an opinion. access is threaded through so classifiedSecretWrite can
// apply the AccessModify carve-out (NoWriteToSecretPath's own doc comment,
// "AccessModify carve-out" section) only to the one access class it covers.
//
// This is a SEPARATE helper from secretRead/classifiedSecretRead, not a
// parameterisation of them: NoReadOfSecretPath and DeleteAccess both already
// depend on secretRead's exact (string, bool) "is this secret" signature,
// and the write side's GenericSecretsDir tier needs a DIFFERENT verdict
// (Unknown, not Forbidden — see NoWriteToSecretPath's own doc comment for
// why), not merely a different reason string. Duplicating the two-pass
// control flow here keeps both read- and write-side helpers simple single-
// purpose functions rather than growing secretRead an extra parameter only
// the write side needs.
func secretWrite(p string, access cmddesc.PathAccess, ctx PolicyContext) (Finding, bool) {
	if f, ok := classifiedSecretWrite(p, access, ctx, ""); ok {
		return f, true
	}
	if ctx.PathEval != nil {
		if resolved := ctx.PathEval.ResolvePath(p); resolved != "" {
			if f, ok := classifiedSecretWrite(resolved, access, ctx, " (resolved)"); ok {
				return f, true
			}
		}
	}
	return Finding{}, false
}

// classifiedSecretWrite applies secretpath.Classify to candidate and decides
// the write-side Finding: WellKnownSecret is Forbidden UNLESS access is
// AccessModify AND deletable.NonSecret declares the path non-secret (the
// tc-8og1 item 2 carve-out — NoWriteToSecretPath's own doc comment has the
// full ruling and why the untracked branch of AccessModify is deliberately
// left Forbidden rather than relaxed to Unknown); GenericSecretsDir is
// Unknown UNLESS deletable.NonSecret declares the path non-secret,
// regardless of access class, in which case this policy has no opinion (ok
// false) and an ordinary write policy decides. suffix is appended to the
// reason text (secretWrite's "(resolved)" annotation for the symlink-
// resolved pass), matching classifiedSecretRead's own convention.
//
// candidate is mapped through stripGoPackagePattern before it is handed to
// deletable.NonSecret, for the identical reason classifiedSecretRead does —
// see that function's doc comment.
func classifiedSecretWrite(candidate string, access cmddesc.PathAccess, ctx PolicyContext, suffix string) (Finding, bool) {
	switch secretpath.Classify(candidate) {
	case secretpath.WellKnownSecret:
		if access == cmddesc.AccessModify {
			if nonSecret, _ := deletable.NonSecret(ctx.PathEval, stripGoPackagePattern(candidate)); nonSecret {
				return Finding{}, false
			}
		}
		return Finding{Verdict: Forbidden, Reason: "write to secret path" + suffix}, true
	case secretpath.GenericSecretsDir:
		if nonSecret, _ := deletable.NonSecret(ctx.PathEval, stripGoPackagePattern(candidate)); nonSecret {
			return Finding{}, false
		}
		return Finding{Verdict: Unknown, Reason: "write to a path named \"secrets\"" + suffix + " needs consent (no project declaration vouches for it as non-secret)"}, true
	default:
		return Finding{}, false
	}
}

// NetworkAccess judges net effects against the vetted-host list: a dynamic
// host is Unknown; content flowing IN from a vetted host is Permitted;
// content flowing OUT is Unknown even to a vetted host (an upload needs
// explicit consent in this slice); an unvetted host is Unknown. It never
// returns Forbidden — an unvetted host is not known-bad.
type NetworkAccess struct{}

// Name implements Policy.
func (NetworkAccess) Name() string { return "network-access" }

// Judge implements Policy.
func (NetworkAccess) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectNet {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "host is a runtime expansion"}, true
	}
	if !ctx.HostVetted(e.Host) {
		return Finding{Verdict: Unknown, Reason: "host is not vetted"}, true
	}
	if e.Direction == cmddesc.NetOutbound {
		return Finding{Verdict: Unknown, Reason: "upload to a vetted host requires consent"}, true
	}
	return Finding{Verdict: Permitted, Reason: "vetted host"}, true
}

// RemoteMutation judges EffectRemote effects only. A dynamic resource
// (its target is only known at runtime — the default remote a bare `git
// push` would use) is Unknown, since the policy cannot tell what it names.
// The Operation vocabulary (data, not command names):
//
//   - "read": a read of a named remote resource (bd list/show/ready, slice
//     3n) — Permitted. Reading is not a mutation; the resource's content
//     flowing somewhere dangerous is the flow policies' concern.
//
//   - "push", "mutate": a write needing explicit consent — Unknown, never
//     Permitted here ("mutate" is bd's issue-writing verbs, slice 3n).
//     EXCEPTION: a DryRun-marked "push" (TransformDryRun, cmddesc/
//     transform.go) is Permitted — see the DryRun paragraph below.
//
//   - "force-push", "delete-ref": known-bad, unreviewable rewrites of a
//     shared ref — Forbidden. EXCEPTION: DryRun-marked, see below.
//
//   - DryRun (slice 3w; tc-lc8f item 4d, tc-ife3 item 2): operator ruling
//     (Phillip, 2026-09-07, verbatim, recorded on tc-ife3/tc-vn5z): "git
//     push force shiuld be abstain with -n as nothong happens." Normalized:
//     a dry run (`-n`/`--dry-run`, any flag order, with `-f`/`--force`/
//     `--force-with-lease`/`-d`/`--delete` in any order) of what would
//     otherwise be a FORBIDDEN mutation (force-push, delete-ref) is neither
//     production's unconditional Reject (wrong — nothing happens) nor an
//     automatic Approve (also wrong — a dry run of a forbidden mutation is
//     not something to auto-clear) — it is Unknown. A dry run of the
//     ORDINARY "push" operation is unaffected by this ruling and stays
//     Permitted, matching its pre-existing behavior (the effect used to be
//     REMOVED by TransformDryRun before it ever reached this policy; now it
//     survives, marked, and this policy judges it Permitted directly — same
//     net verdict, more explicit representation). This is expressed purely
//     as data: e.Operation (already a small open vocabulary) crossed with
//     e.DryRun (cmddesc.Effect's new field) — nothing here or in
//     TransformDryRun branches on a command name, so any future schema
//     whose Operation lands in the forbidden class gets the same treatment
//     automatically.
//
//   - "dolt-server": starting, stopping or killing a Dolt SQL server (bd
//     dolt start/stop/killall, slice 3n). REVISED by slice 3u per an
//     operator ruling (Phillip, 2026-09-07, verbatim, recorded on tc-vn5z):
//     "for bd dolt, the default foe stsrt/stop/killall should be to
//     abstain, but my persoanl confog on this would be yo reject."
//     (typos corrected, meaning unambiguous from context: bd dolt
//     start/stop/killall default to Abstain/Unknown; Reject is the
//     OPERATOR'S PERSONAL CONFIGURATION, not a hard-coded default). This
//     SUPERSEDES slice 3n's unconditional Forbidden for "dolt-server":
//     the default is now Unknown ("needs consent, exactly like a git
//     push"), and Forbidden only when PolicyContext.RemoteLifecycleClass
//     for the effect's Target (Resource) is "reject" — data on the
//     request (evalcontract.Request.RemoteLifecycle), this spike's
//     stand-in for a future rules.json binding. Nothing here still names
//     BEADS_DOLT_AUTO_START or "no rogue auto-start" as a hard-coded
//     policy fact; that machine invariant is now expressed as the
//     OPERATOR'S OWN configured value, supplied by the caller, not baked
//     into this policy.
//
//   - anything else is outside this vocabulary and fails closed to Unknown.
//
// EXCLUDES any EffectRemote whose Family is non-empty (slice 3y, tc-lc8f
// item 4f; tc-vn5z item 3): "kubectl" (and any future context-bearing
// remote family) is judged by a SEPARATE policy, KubeContextPolicy below,
// because its Operation vocabulary ("read"/"mutation"/"exec") is judged PER
// KUBE CONTEXT, not by this policy's fixed per-Operation table — see
// Effect.Family's own doc comment. Without this exclusion RemoteMutation
// would ALSO judge a kubectl effect unconditionally (its "read" case is an
// unconditional Permitted, exactly the blanket read-permission the
// operator's ruling forbids for kubectl), and the two policies' findings
// would combine worst-of on the SAME effect (judgeNode's fold) rather than
// KubeContextPolicy owning the verdict alone.
type RemoteMutation struct{}

// Name implements Policy.
func (RemoteMutation) Name() string { return "remote-mutation" }

// Judge implements Policy.
func (RemoteMutation) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectRemote || e.Family != "" {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "remote resource is a runtime expansion"}, true
	}
	switch e.Operation {
	case "read":
		return Finding{Verdict: Permitted, Reason: "read of a named remote resource"}, true
	case "push":
		if e.DryRun {
			return Finding{Verdict: Permitted, Reason: "dry run of an ordinary push mutates nothing"}, true
		}
		return Finding{Verdict: Unknown, Reason: "remote ref update requires consent"}, true
	case "mutate":
		return Finding{Verdict: Unknown, Reason: "remote resource mutation requires consent"}, true
	case "force-push":
		if e.DryRun {
			return Finding{Verdict: Unknown, Reason: "dry run of a force-push mutates nothing, but a forbidden mutation's dry run is not auto-approved either (operator ruling, Phillip 2026-09-07, tc-ife3/tc-vn5z)"}, true
		}
		return Finding{Verdict: Forbidden, Reason: "force-push rewrites a shared ref"}, true
	case "delete-ref":
		if e.DryRun {
			return Finding{Verdict: Unknown, Reason: "dry run of a delete-ref mutates nothing, but a forbidden mutation's dry run is not auto-approved either (operator ruling, Phillip 2026-09-07, tc-ife3/tc-vn5z)"}, true
		}
		return Finding{Verdict: Forbidden, Reason: "delete-ref removes a shared ref"}, true
	case "dolt-server":
		if ctx.RemoteLifecycleClass(e.Resource) == "reject" {
			return Finding{Verdict: Forbidden, Reason: "operator configuration rejects Dolt server lifecycle changes to " + e.Resource + " (tc-vn5z)"}, true
		}
		return Finding{Verdict: Unknown, Reason: "Dolt server lifecycle change requires consent (default; operator MAY configure rejection, tc-vn5z)"}, true
	default:
		return Finding{Verdict: Unknown, Reason: "unrecognised remote operation " + e.Operation}, true
	}
}

// KubeContextPolicy judges every EffectRemote effect whose Family is
// "kubectl" (registry_breadth.go's kubectl schema family, slice 3y; tc-lc8f
// item 4f, tc-vn5z item 3) — deliberately a SEPARATE policy from
// RemoteMutation, which explicitly EXCLUDES Family != "" effects (see its own
// doc comment), because kubectl's Operation vocabulary ("read", "mutation",
// "exec") is judged PER KUBE CONTEXT, not by a fixed Operation-to-verdict
// table: RemoteMutation's own "read"/"mutate" cases are UNCONDITIONAL (a
// git/bd read is always Permitted, a bd write is always Unknown), which is
// exactly what the operator's ruling on kubectl forbids.
//
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on bead tc-vn5z):
// "kubectl should be configured to vary per context. ie, there could be a
// "dev" cluster which would allow most anythkng vs a "prod" which could be
// more restricted." Normalized: kubectl reads are NOT permitted
// unconditionally; the policy varies per kube CONTEXT from OPERATOR
// CONFIGURATION (evalcontract.Request.KubeContexts/KubeContextDefaultAllow,
// this spike's stand-in for a future rules.json binding — mirroring
// RemoteLifecycle's own "data on the request" pattern, slice 3u): a context
// listed there allows only the Operation classes named in its Allow list; an
// unlisted context falls back to KubeContextDefaultAllow (empty by default,
// i.e. Unknown for every class). When the context cannot be determined
// STATICALLY from the command (no --context given at all, or its value is a
// runtime expansion — cmddesc's kubectlInterpreter's own doc comment covers
// how e.Resource/e.Dynamic get set), the verdict is Unknown/Abstain — never
// a blanket read permission, per the ruling's own worked example.
//
// A DryRun-marked "mutation" (kubectl's own `--dry-run=client`, cmddesc's
// TransformDryRun, extended by slice 3y to include "mutation" in
// remoteMutationOps) is judged as if it were a "read": nothing actually
// changes the cluster, so it needs only the SAME per-context permission a
// genuine read would (slice 3w's dry-run precedent, applied here to a third
// Operation vocabulary). `--dry-run=server` is NOT marked (kubectl itself
// still contacts the API server to run admission/validation), so it stays
// judged as an ordinary, un-marked "mutation" — unchanged from a real
// mutation.
type KubeContextPolicy struct{}

// Name implements Policy.
func (KubeContextPolicy) Name() string { return "kube-context-policy" }

// Judge implements Policy.
func (KubeContextPolicy) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectRemote || e.Family != "kubectl" {
		return Finding{}, false
	}
	if e.Dynamic || e.Resource == "" {
		return Finding{Verdict: Unknown, Reason: "kube context is not statically known from the command (tc-vn5z)"}, true
	}
	op := e.Operation
	if op == "mutation" && e.DryRun {
		op = "read"
	}
	context := e.Resource
	if ctx.KubeContextKnown(context) {
		if ctx.KubeContextAllows(context, op) {
			return Finding{Verdict: Permitted, Reason: fmt.Sprintf("kube context %q allows %s (operator configuration, tc-vn5z)", context, op)}, true
		}
		return Finding{Verdict: Forbidden, Reason: fmt.Sprintf("kube context %q does not allow %s (operator configuration, tc-vn5z)", context, op)}, true
	}
	if ctx.KubeContextAllows(context, op) {
		return Finding{Verdict: Permitted, Reason: fmt.Sprintf("kube context %q is not configured; operator default allows %s (tc-vn5z)", context, op)}, true
	}
	return Finding{Verdict: Unknown, Reason: fmt.Sprintf("kube context %q is not configured (default: needs consent, tc-vn5z)", context)}, true
}

// NoReadOfUnreadablePath has one concern: patheval READABILITY of a read path
// effect. Forbidden when the zone is reject or a sandbox denyRead entry
// matches, Unknown when the path is dynamic or unzoned (or there is no
// evaluator), Permitted when the zone can be read.
type NoReadOfUnreadablePath struct{}

// Name implements Policy.
func (NoReadOfUnreadablePath) Name() string { return "no-read-of-unreadable-path" }

// Judge implements Policy.
func (NoReadOfUnreadablePath) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectPath || e.Access != cmddesc.AccessRead {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "path is a runtime expansion"}, true
	}
	if ctx.PathEval == nil {
		return Finding{Verdict: Unknown, Reason: "no path evaluator"}, true
	}
	if ctx.PathEval.IsDenyRead(e.Path) {
		return Finding{Verdict: Forbidden, Reason: "path is denyRead"}, true
	}
	access := ctx.PathEval.Evaluate(e.Path)
	switch {
	case access.CanRead():
		return Finding{Verdict: Permitted, Reason: "zone " + access.String()}, true
	case access == patheval.PathReject:
		return Finding{Verdict: Forbidden, Reason: "read of " + access.String() + " zone"}, true
	default:
		return Finding{Verdict: Unknown, Reason: "zone " + access.String()}, true
	}
}

// TrustedCheckoutExec judges every EffectExec effect (slice 3x, tc-lc8f item
// 4e; tc-vn5z item 1) — the go tool's own registry schema (cmddesc/
// registry_breadth.go's goTestSchema and siblings) is this effect's only
// producer today. Operator ruling (Phillip, 2026-09-07, verbatim, recorded
// on tc-vn5z item 1): "go test and go generate are fine. go run is trickier.
// i would like it to be parsed, but i dont think there will be a
// definitition of the gonrun for the spexifox situatikn. so abstoan on it."
// Normalized: executing the checkout's own test/generate code, and the go
// toolchain's ordinary build-cache traffic (build/vet/fmt/list/env/version/
// mod), are a PERMITTED class of "executing trusted checkout code" — citing
// this repo's `docs/adr/0053-ceta-threat-model.md`'s "2. What is trusted vs.
// what is screened" (the CWD / project tree is TRUSTED state, "not fenced
// off as untrusted"), and matching what production already approves today
// (internal/rules/buildtools's baseApprovedTools unconditionally lists
// "go") — but ONLY when the
// invocation is actually running inside a checkout this machine recognises,
// never unconditionally by command name: this policy's only question is
// "is CWD inside a declared git/go workspace" (deletable.
// InsideMarkerWorkspace over deletable.DefaultKinds' git/go Markers —
// internal/deletable/workspace.go's gitKind/goKind), Permitted when so,
// Unknown otherwise (a go tool running against some OTHER directory this
// machine has no workspace declaration for is not vouched for merely by
// being the `go` binary). `go run` is deliberately NOT judged by this policy
// at all: goRunSchema's own positional role is KindUnmodeled, which fails
// the interpretation closed (builder-level Insufficient, never Forbidden)
// before any policy sees an effect — "parsed" (its flags and target are
// visible in the interpreted graph) but always Abstain, exactly per the
// ruling's "abstain on it", regardless of what this policy would say.
//
// # Build-tool family routing (tc-8og1 item 3 sub-slice 3; tc-vn5z Q1-Q5,
// # ruled 2026-09-08)
//
// A build-tool-family verb (just/npm/devbox/... — cmddesc's own schema for
// them is tc-8og1 item 3 sub-slice 4, NOT this slice) is expected to
// produce an EffectExec with Effect.Family set to the tool's basename and
// Effect.Operation set to the invoked verb — the SAME two generic,
// per-kind-reusable fields EffectRemote's own kubectl routing already
// established (Family="kubectl" routes to KubeContextPolicy instead of
// RemoteMutation's fixed table; see cmddesc.Effect.Family's own doc
// comment, "kept as a generic string ... so a future ... family can reuse
// the same routing without a new field"). Per Q2's ruling (Phillip,
// 2026-09-08, verbatim on tc-vn5z): "there should be a spec for build
// tools ... leaning toward extending TrustedCheckoutExec's marker list" —
// answering "does a project-tied verb's EffectExec route through the
// EXISTING TrustedCheckoutExec ... or a new per-tool policy?" with "extend
// the existing policy ... one policy for every EffectExec regardless of
// producing tool" — this is handled IN PLACE below (judgeBuildToolVerb),
// not by a sibling policy type, unlike the kubectl/RemoteMutation split.
// No cmddesc schema stamps Family/Operation onto a real EffectExec yet
// (that is sub-slice 4), so this branch has no live golden/corpus trigger
// in this slice — proven only by policy_test.go's direct Judge() calls,
// the same "building block, no behavior change yet" shape slice 3ag's
// workspace verb-discovery facet already established.
//
// Sub-slice 5 (slice 3ak, the FINAL sub-slice of the build-tool family
// design) adds judgeBuildToolVerb's second judged Class,
// evalcontract.VerbClassInstallableReference, for `nix run`'s installable
// child — see that constant's own doc comment (evalcontract/contract.go)
// and judgeBuildToolVerb's own doc comment below for the full contract.
type TrustedCheckoutExec struct{}

// Name implements Policy.
func (TrustedCheckoutExec) Name() string { return "trusted-checkout-exec" }

// Judge implements Policy.
func (TrustedCheckoutExec) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectExec {
		return Finding{}, false
	}
	if ctx.CWD == "" {
		return Finding{Verdict: Unknown, Reason: "no working directory to locate a checkout from"}, true
	}
	abs := patheval.ResolveRealPath(ctx.CWD)
	if abs == "" {
		return Finding{Verdict: Unknown, Reason: "working directory does not resolve"}, true
	}
	if e.Family != "" {
		return judgeBuildToolVerb(e, abs, ctx)
	}
	if deletable.InsideMarkerWorkspace(deletable.DefaultKinds(), []string{"git", "go"}, abs) {
		return Finding{Verdict: Permitted, Reason: "CWD is inside a recognised git/go workspace (ADR 0053's \"executing trusted checkout code\")"}, true
	}
	return Finding{Verdict: Unknown, Reason: "CWD is not inside any recognised git/go workspace"}, true
}

// judgeBuildToolVerb judges a build-tool-family EffectExec — see
// TrustedCheckoutExec's own doc comment, "Build-tool family routing"
// section, for the Family/Operation contract and provenance. e.Family
// names the tool basename (e.g. "just"); e.Operation names the invoked
// verb (e.g. "check"). A dynamic verb (a runtime expansion — Dynamic is
// shared across effect kinds exactly like the path/net effects already
// use it) is Unknown, mirroring NetworkAccess/KubeContextPolicy's own
// "dynamic input -> Unknown" first check. Otherwise: no matching
// PolicyContext.BuildToolVerbs entry for (Family, Operation) is Unknown
// ("not operator-declared"). A matching entry's Class then selects one of
// two judged ladders (evalcontract.VerbScopedApproval's own doc comment
// has the full class vocabulary):
//
//   - "" / evalcontract.VerbClassProjectTied: Permitted ONLY when
//     deletable.DiscoveredVerbs (slice 3ag) independently finds Operation
//     among the verbs a Kind named Family discovers at or above abs —
//     otherwise Unknown ("operator declared it, but the workspace's own
//     files do not currently define it — abstain, never guess", per Q3's
//     ruling and this slice's own brief).
//   - evalcontract.VerbClassInstallableReference (tc-8og1 item 3 sub-slice
//     5): Permitted when, and ONLY when, Operation is itself shaped like a
//     LOCAL flake reference (isLocalFlakeInstallable) — the operator's
//     declaration is sufficient by itself here (no independent discovery
//     exists for a Nix installable, per Q1), but a non-local reference
//     (a remote/registry-resolved installable) stays Unknown even when
//     declared — see evalcontract.VerbClassInstallableReference's own doc
//     comment for why.
//
// Any OTHER Class value is Unknown ("not yet judged by this policy").
func judgeBuildToolVerb(e cmddesc.Effect, abs string, ctx PolicyContext) (Finding, bool) {
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "verb is a runtime expansion"}, true
	}
	entry, found := findVerbScopedApproval(ctx.BuildToolVerbs, e.Family, e.Operation)
	if !found {
		return Finding{Verdict: Unknown, Reason: fmt.Sprintf("no operator declaration for %s verb %q", e.Family, e.Operation)}, true
	}
	class := entry.Class
	if class == "" {
		class = evalcontract.VerbClassProjectTied
	}
	switch class {
	case evalcontract.VerbClassProjectTied:
		if workspaceVouchesForVerb(deletable.DefaultKinds(), abs, e.Family, e.Operation) {
			return Finding{Verdict: Permitted, Reason: fmt.Sprintf("workspace's own %s declaration defines %q as a project-tied verb", e.Family, e.Operation)}, true
		}
		return Finding{Verdict: Unknown, Reason: fmt.Sprintf("%s verb %q is operator-declared project-tied, but no %s file at or above CWD defines it — abstaining rather than guessing", e.Family, e.Operation, e.Family)}, true
	case evalcontract.VerbClassInstallableReference:
		if isLocalFlakeInstallable(e.Operation) {
			return Finding{Verdict: Permitted, Reason: fmt.Sprintf("operator declared %s installable %q vetted (a local flake reference — no independent workspace confirmation exists for flake.nix apps, per Q1's ruling, so the declaration alone governs)", e.Family, e.Operation)}, true
		}
		return Finding{Verdict: Unknown, Reason: fmt.Sprintf("%s installable %q is not a local flake reference (only \".\" and \".#<attr>\" are modeled by this policy); a remote or registry-resolved installable is deliberately out of scope even when operator-declared", e.Family, e.Operation)}, true
	default:
		return Finding{Verdict: Unknown, Reason: fmt.Sprintf("verb class %q is not yet judged by this policy", class)}, true
	}
}

// isLocalFlakeInstallable reports whether s — a `nix run` installable
// operand, captured verbatim as Effect.Operation (tc-8og1 item 3 sub-slice
// 5) — is one of the two flake-reference spellings
// evalcontract.VerbClassInstallableReference judges: the bare
// current-directory flakeref "." (nix's own default when no installable is
// given at all — see cmddesc's nixRunSchema.DefaultVerb doc comment) or a
// "."-relative attribute selector on it, ".#<attr>" (attr may itself carry
// a "^<output>" output selector, e.g. ".#foo^bin" — this function does not
// parse further than the "."/".#"" prefix; the full attr/output text is
// compared verbatim against the operator's declared Verb string by
// findVerbScopedApproval's exact-match lookup, upstream of this call).
//
// Every OTHER syntactically valid nix installable is deliberately treated
// as NOT local, even where a human might call some of them "local enough":
// a relative/absolute PATH flakeref ("./sub", "../sib", "path:...", "/abs")
// — these could point at a flake OUTSIDE the current project tree, or
// require path-arithmetic this function does not attempt; an INDIRECT
// flakeref (a bare registry id like "nixpkgs" or "blender-bin", with or
// without "#attr") — resolved through the (mutable, operator/global)
// flake registry, not project content; a URL-schemed flakeref (github:,
// gitlab:, sourcehut:, git+https:, git+ssh:, tarball:, flake:...) — an
// explicit remote fetch; or a raw /nix/store path. See
// evalcontract.VerbClassInstallableReference's own doc comment for why
// this scope is deliberately narrow (a documented follow-up, not a gap
// found and left unaddressed by accident).
func isLocalFlakeInstallable(s string) bool {
	if s == "." {
		return true
	}
	return strings.HasPrefix(s, ".#")
}

// findVerbScopedApproval returns the first BuildToolVerbs entry matching
// (tool, verb) exactly, or ok=false when none does.
func findVerbScopedApproval(entries []evalcontract.VerbScopedApproval, tool, verb string) (entry evalcontract.VerbScopedApproval, ok bool) {
	for _, e := range entries {
		if e.Tool == tool && e.Verb == verb {
			return e, true
		}
	}
	return evalcontract.VerbScopedApproval{}, false
}

// workspaceVouchesForVerb reports whether deletable.DiscoveredVerbs finds
// verb among the verbs a Kind named tool discovers at or above abs — the
// "WORKSPACE vouches ... literally defined in-project" check Q3's ruling
// calls for. An empty verb never matches (no operator entry should ever
// have an empty Verb reach here in practice, but this keeps the function
// total and fail-safe on its own).
func workspaceVouchesForVerb(kinds []deletable.Kind, abs, tool, verb string) bool {
	if verb == "" {
		return false
	}
	for _, vs := range deletable.DiscoveredVerbs(kinds, abs) {
		if vs.Kind != tool {
			continue
		}
		for _, v := range vs.Verbs {
			if v == verb {
				return true
			}
		}
	}
	return false
}
