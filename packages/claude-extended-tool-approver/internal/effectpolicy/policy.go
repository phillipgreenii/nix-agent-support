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
// hit are distinguishable reasons.
func DefaultPolicies() []Policy {
	return []Policy{NoWriteToReadOnlyPath{}, NoReadOfSecretPath{}, NoReadOfUnreadablePath{}, NetworkAccess{}}
}

// NoWriteToReadOnlyPath applies to every write-class path effect (create,
// modify, delete, truncate — PathAccess.IsWrite), whatever command produced
// it: Forbidden when patheval's zone (or a sandbox denyWrite entry) forbids
// writing, Unknown when the path is dynamic or unzoned, Permitted otherwise.
type NoWriteToReadOnlyPath struct{}

// Name implements Policy.
func (NoWriteToReadOnlyPath) Name() string { return "no-write-to-read-only-path" }

// Judge implements Policy.
func (NoWriteToReadOnlyPath) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectPath || !e.Access.IsWrite() {
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
