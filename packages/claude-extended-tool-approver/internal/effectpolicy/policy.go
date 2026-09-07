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
type PolicyContext struct {
	PathEval *patheval.PathEvaluator
	CWD      string
}

// Policy judges effects. Judge reports (finding, true) when the policy applies
// to the effect and (_, false) when it does not.
type Policy interface {
	Name() string
	Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool)
}

// DefaultPolicies returns the spike's policy set.
func DefaultPolicies() []Policy {
	return []Policy{NoWriteToReadOnlyPath{}, NoReadOfSecretPath{}}
}

// NoWriteToReadOnlyPath applies to every write-class path effect, whatever
// command produced it: Forbidden when patheval's zone (or a sandbox denyWrite
// entry) forbids writing, Unknown when the path is dynamic or unzoned,
// Permitted otherwise.
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

// NoReadOfSecretPath applies to every read path effect: Forbidden when the raw
// or resolved path is a secret (secretpath) or patheval rejects/denies reading
// it, Unknown when dynamic or unzoned, Permitted otherwise.
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
	if secretpath.IsSecret(e.Path) {
		return Finding{Verdict: Forbidden, Reason: "secret path"}, true
	}
	if ctx.PathEval == nil {
		return Finding{Verdict: Unknown, Reason: "no path evaluator"}, true
	}
	if resolved := ctx.PathEval.ResolvePath(e.Path); resolved != "" && secretpath.IsSecret(resolved) {
		return Finding{Verdict: Forbidden, Reason: "secret path (resolved)"}, true
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
