// Package checkinterpret is pg-pr's pluggable, config-driven check/status
// interpreter registry: it classifies a check/status NAME against a
// per-repo ordered list of interpreter declarations (pattern(s) that claim
// a name, and which interpreter Type is responsible for it), then
// dispatches a claimed name to that Type's classification function.
//
// This generalizes internal/cirollup.Excluder's role (bead pg2-4dz88.2):
// where Excluder only ever answered "is this name excluded from the CI
// rollup?", a Registry answers "which interpreter claims this name, and
// what does that interpreter say about it?" — of which "excluded from CI
// health" (the approval-gate interpreter's own contribution, left to the
// out-of-scope integration leaf pg2-4dz88.2.6) is one possible answer, not
// the only one a future interpreter Type could give.
//
// Design ruling (pg2-4dz88.2's grooming review, carried onto this bead):
// this package and internal/verdict (the SIBLING pluggable classifier for
// the per-approver-verdict axis) deliberately do NOT share a code-level
// abstraction, even though both are config-driven pluggable classifiers
// with a warn-and-skip-on-invalid-pattern convention. The input shapes
// differ (a short machine-generated status string with a single
// tri/four-state axis and fraction parsing, vs. free-text comment bodies
// with generation precedence and two independent axes), and so do the
// "unknown" correctness properties (an unclaimed CHECK must roll up into
// CI health exactly as today; an unmatched VERDICT must become an
// observable signal). Conflating the two domains risks one's fallback
// semantics leaking into the other. They share NAMING/DESIGN CONVENTION
// only — config-driven, warn-and-skip on a bad pattern, an explicit tested
// unknown-fallback — copied by inspection, not by shared code.
//
// Like internal/cirollup and internal/verdict, this package takes plain
// data rather than importing internal/config or pkg/api: Interpreter
// mirrors config.CheckInterpreterConfig field-for-field, and Classify
// takes plain strings rather than an api.CIRun. Converting
// []config.CheckInterpreterConfig to []Interpreter, and api.CIRun fields
// to the plain strings Classify wants, is a separate, out-of-scope sibling
// leaf (pg2-4dz88.2.6) — exactly mirroring how internal/verdict.Generation
// vs config.VerdictGeneration split responsibility.
package checkinterpret

import (
	"log/slog"
	"regexp"
)

// Interpreter is one entry in a repo's check/status interpreter registry:
// which check/status names it claims (Patterns, Go regular-expression
// strings) and which interpreter Type is responsible for classifying
// them. Mirrors config.CheckInterpreterConfig (internal/config/config.go)
// field-for-field; see the package doc for why this package defines its
// own type rather than importing that one.
type Interpreter struct {
	Patterns []string
	Type     string
}

// ApprovalGateType is the Type tag this package's one concrete
// interpreter (ClassifyApprovalGate) is dispatched for.
const ApprovalGateType = "approval-gate"

// compiledEntry is an Interpreter with its Patterns pre-compiled once at
// New time, mirroring cirollup.Excluder / verdict.compiledGeneration. Type
// is carried through verbatim and is NOT validated against a known set —
// see New's doc comment for why.
type compiledEntry struct {
	pats []*regexp.Regexp
	typ  string
}

// Registry claims check/status names against a fixed, pre-compiled set of
// Interpreter declarations, in declaration order, and dispatches a
// claimed name to its interpreter Type's classification function. Safe
// for concurrent use (read-only after New).
type Registry struct {
	entries []compiledEntry
}

// New compiles every Interpreter's Patterns and returns the resulting
// Registry. Unlike verdict.New, it returns no error: mirroring
// cirollup.NewExcluder's convention (this bead's own resolved design
// decision), a mis-configured pattern must not break the sync loop, so an
// invalid pattern is warn-and-skip (via slog) rather than a constructor
// error — later valid patterns, in the same entry or a later entry, still
// match.
//
// New does NOT validate Type against a known set. Only ApprovalGateType
// is dispatched by Classify today, but an entry naming any other Type is
// still compiled and still claims names via Claim — a future interpreter
// Type is wired up by teaching Classify to dispatch it, not by relaxing a
// validation this constructor never had. See Classify's doc comment for
// what happens when a name is claimed by a Type this package cannot yet
// dispatch.
//
// A nil/empty interpreters slice is valid and produces a Registry that
// claims nothing — mirrors cirollup.NewExcluder's and verdict.New's "nil
// or empty is valid" precedent. Likewise, an Interpreter with an empty (or
// entirely invalid) Patterns list claims nothing — never everything.
func New(interpreters []Interpreter) *Registry {
	reg := &Registry{}
	for _, ip := range interpreters {
		entry := compiledEntry{typ: ip.Type}
		for _, p := range ip.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				slog.Default().Warn("checkinterpret: invalid interpreter pattern skipped",
					"pattern", p, "type", ip.Type, "err", err.Error())
				continue
			}
			entry.pats = append(entry.pats, re)
		}
		reg.entries = append(reg.entries, entry)
	}
	return reg
}

// Claim reports which interpreter Type claims name, if any. Nil-safe (a
// nil *Registry claims nothing).
//
// Precedence when two entries could both match one name: FIRST-DECLARED
// WINS — the first entry, in declaration order, with a matching pattern
// determines the returned Type. This is the pinned choice for a design
// decision pg2-4dz88.2.4's brief deliberately left open. It mirrors
// cirollup.Excluder.Match's implicit behavior (Match only ever needed ANY
// pattern across the whole excluder to match, which is indistinguishable
// from "first entry wins" when there was only ever one Type to report),
// rather than verdict.Classify's LAST-declared-wins (chosen there because
// later verdict generations represent newer grammar versions that should
// override older ones — a rationale that does not apply to interpreter
// declarations, which are not versioned).
func (r *Registry) Claim(name string) (string, bool) {
	if r == nil {
		return "", false
	}
	for _, e := range r.entries {
		for _, re := range e.pats {
			if re.MatchString(name) {
				return e.typ, true
			}
		}
	}
	return "", false
}

// Classify claims name, then dispatches to the claimed interpreter Type's
// classification function with conclusion and description.
//
// ok is false when NO configured interpreter claims name. This is the
// mandatory "unclaimed check must roll up unchanged" signal (the central
// acceptance criterion of pg2-4dz88.2.4): a future integration point
// (pg2-4dz88.2.6, out of scope here) uses ok==false to fall through
// unchanged to cirollup.Classify's existing behavior for that name. This
// package proves only its OWN contract — that unclaimed names are
// distinguishable from any interpreted Result — and deliberately does not
// import internal/cirollup to prove it against Classify's live behavior;
// that would create a new inter-package dependency solely for a test.
//
// ok is true whenever an interpreter claims name — including when the
// claimed Type is ApprovalGateType but the classification itself resolves
// to Unknown (a claimed-but-unparseable description), AND including when
// the claimed Type is one this package does not (yet) know how to
// dispatch. Both report Result{State: Unknown}, true: the name WAS
// claimed by a configured interpreter (distinct from "no interpreter
// configured for this name at all"), even though the definite state
// could not be determined.
func (r *Registry) Classify(name, conclusion, description string) (Result, bool) {
	typ, ok := r.Claim(name)
	if !ok {
		return Result{}, false
	}
	if typ == ApprovalGateType {
		return ClassifyApprovalGate(conclusion, description), true
	}
	// Claimed by a configured interpreter Type this build does not yet
	// know how to dispatch (a future Type beyond this bead's scope).
	return Result{State: Unknown}, true
}
