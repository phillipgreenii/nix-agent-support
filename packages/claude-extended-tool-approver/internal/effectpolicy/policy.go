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
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/deletable"
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
// from matching `example.com`).
type PolicyContext struct {
	PathEval    *patheval.PathEvaluator
	CWD         string
	VettedHosts []string
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
func DefaultPolicies() []Policy {
	return []Policy{
		NoWriteToReadOnlyPath{},
		DeleteAccess{},
		NoReadOfSecretPath{},
		NoReadOfUnreadablePath{},
		NetworkAccess{},
		RemoteMutation{},
		StdioIsLocal{},
		ProgramInterpreted{},
		EnvAssignment{},
		ChdirScoped{},
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
// three data sets above: an injector name is Forbidden; an injector-ask or
// ask name is Unknown, because the live rule's answer for these names
// depends on a VALUE judgement (preservesCallerValue, the hermetic-HOME
// relief) this slice does not model at all — Unknown/Abstain is the honest
// mapping for "the live rule would ask a human here", not a guess in either
// direction; any other static name is outside this slice's vocabulary of
// known-bad names and is Permitted.
//
// Accepted gap, explicit by design: VALUES are not modeled. Every Set of an
// ask-class NAME (PATH, HOME) is Unknown regardless of value, even the
// extend-shaped value (`PATH="$PATH:/nix/store/…/bin"`) the live rule would
// Approve after inspecting it — the live rule's relief is strictly more
// permissive here, which makes this an accepted spike-stricter divergence
// (see testdata/agreement.txt), never a safety gap: this policy never
// silently Approves an ask/injector-ask name.
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
		return Finding{Verdict: Unknown, Reason: "ask variable; live rule's verdict depends on value, which this slice does not model"}, true
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
//  6. deletable.Classify (workspace
//     declarations, tc-z806.3):
//     Protected (.git, .worktrees, a pn
//     workforest set, ~/.ssh)               -> Forbidden
//     Deletable (gitignored, build/ of a
//     gradle project, ~/.cache, go's build
//     cache, a temp root)                   -> Permitted, even where the
//     zone is unknown: a declaration that a path is disposable vouches for
//     removing it
//  7. zone unknown, no declaration          -> Unknown   (not known writable)
//  8. writable, not deletable              -> Unknown   ("needs consent")
//
// Step 3 includes denyRead deliberately: the ruling says protections above
// this rule win, and a path the operator has marked unreadable is protected
// whether or not it is also marked unwritable — refusing to remove it is the
// fail-safe reading. Step 4 uses the same secretRead helper the read-side
// policies use, so a secret is named the same way whichever access class
// touches it. Step 5 runs BEFORE step 6, so a declared-deletable cache that
// sits in a read-only zone (go's module cache under patheval's `~/go/pkg`)
// is still Forbidden — the zone is the older, narrower decision and wins.
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
func secretRead(p string, ctx PolicyContext) (string, bool) {
	if secretpath.IsSecret(p) {
		return "secret path", true
	}
	if ctx.PathEval != nil {
		if resolved := ctx.PathEval.ResolvePath(p); resolved != "" && secretpath.IsSecret(resolved) {
			return "secret path (resolved)", true
		}
	}
	return "", false
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
//   - "push", "mutate": a write needing explicit consent — Unknown, never
//     Permitted here ("mutate" is bd's issue-writing verbs, slice 3n).
//   - "force-push", "delete-ref": known-bad, unreviewable rewrites of a
//     shared ref — Forbidden.
//   - "dolt-server": starting, stopping or killing a Dolt SQL server (bd
//     dolt start/stop/killall, slice 3n) — Forbidden. This machine forbids
//     it outright: ~/.claude/CLAUDE.md "Beads / Dolt: no rogue auto-start"
//     (BEADS_DOLT_AUTO_START=0; "You MUST NOT start a dolt server on your
//     own initiative") and .claude/rules/beads-remote-server.md ("Never
//     run bd dolt start — the Dolt server is a remote k3s service").
//   - anything else is outside this vocabulary and fails closed to Unknown.
type RemoteMutation struct{}

// Name implements Policy.
func (RemoteMutation) Name() string { return "remote-mutation" }

// Judge implements Policy.
func (RemoteMutation) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectRemote {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "remote resource is a runtime expansion"}, true
	}
	switch e.Operation {
	case "read":
		return Finding{Verdict: Permitted, Reason: "read of a named remote resource"}, true
	case "push":
		return Finding{Verdict: Unknown, Reason: "remote ref update requires consent"}, true
	case "mutate":
		return Finding{Verdict: Unknown, Reason: "remote resource mutation requires consent"}, true
	case "force-push":
		return Finding{Verdict: Forbidden, Reason: "force-push rewrites a shared ref"}, true
	case "delete-ref":
		return Finding{Verdict: Forbidden, Reason: "delete-ref removes a shared ref"}, true
	case "dolt-server":
		return Finding{Verdict: Forbidden, Reason: "this machine never starts or stops a Dolt server (CLAUDE.md: no rogue auto-start; beads-remote-server rule)"}, true
	default:
		return Finding{Verdict: Unknown, Reason: "unrecognised remote operation " + e.Operation}, true
	}
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
