// Package verdict is pg-pr's pluggable, versioned verdict grammar: it
// classifies a review-comment body into two independent axes — Findings
// (clean | problems | unknown) and Authority (approved | withheld | pending |
// absent) — dispatched over caller-supplied, config-declared "generations" of
// the grammar, so a future bot-comment-format change is a config edit, not a
// Go change (bead pg2-4dz88.1.4; generation shape designed in pg2-4dz88.1.3).
//
// This mirrors docs/behavior's approval vocabulary directly: every verdict is
// classified on the two axes named by `INV-APPROVAL-2`, and a verdict body
// this package cannot classify under any configured generation surfaces as
// an observable Unknown/Pending result rather than being silently folded
// into Clean or Absent (`INV-APPROVAL-5`).
//
// Like internal/cirollup (this module's other pure classifier package with
// an injected config object), Classifier takes plain data rather than
// importing internal/config: the caller — a separate, out-of-scope sibling
// leaf — converts []config.VerdictGeneration into []Generation. This package
// does no config loading, no sync/store wiring, and no observability;
// Result's MatchedGeneration field is the hook a future counter/log
// consumer needs, deliberately left unused here.
package verdict

import (
	"fmt"
	"regexp"
	"strings"
)

// Findings is the verdict axis answering "did this approver find problems
// with the PR" (docs/behavior glossary "Findings").
type Findings string

// Authority is the verdict axis answering "does this approver's verdict
// currently stand as an approval" (docs/behavior glossary "Authority").
type Authority string

const (
	// Clean means the matched generation's grammar reported no outstanding
	// findings.
	Clean Findings = "clean"
	// Problems means the matched generation's grammar reported outstanding
	// findings.
	Problems Findings = "problems"
	// FindingsUnknown means no configured generation's grammar recognized a
	// findings signal in the body — either because no generation's grammar
	// resolved at all, or because the body carried no configured generation's
	// BodyMarker in the first place. Never inferred to be Clean (`INV-APPROVAL-5`).
	FindingsUnknown Findings = "unknown"
)

const (
	// Approved means the matched generation's grammar reported an explicit
	// approval-granting signal, with no contradiction.
	Approved Authority = "approved"
	// Withheld means the matched generation's grammar reported the verdict
	// does not currently stand as an approval — either explicitly (a
	// blocked/denied signal matched) or by the generation's implicit default
	// for a Problems finding with no authority signal at all. Authority is
	// NEVER Approved when a contradiction was detected.
	Withheld Authority = "withheld"
	// Pending means a configured generation's BodyMarker was found in the
	// body, but no generation's grammar resolved a Findings value at all
	// (e.g. an in-progress placeholder comment). Distinct from Absent: a
	// Pending body IS a verdict, just not yet a resolved one.
	Pending Authority = "pending"
	// Absent means no configured generation's BodyMarker appeared anywhere
	// in the body: the body is not a verdict at all and is ignored, never
	// misclassified against a login or a pattern that happens to appear
	// without the anchoring marker.
	Absent Authority = "absent"
)

// Generation describes one generation of the review-comment verdict grammar
// that Classify evaluates a comment body against. Its four fields mirror
// config.VerdictGeneration (internal/config/config.go) field-for-field; this
// package deliberately does not import internal/config — following the
// internal/cirollup precedent, a pure classifier package takes plain data,
// not a config type. A separate, out-of-scope sibling leaf converts
// []config.VerdictGeneration to []Generation.
//
// Pattern-list convention: neither FindingsPatterns nor AuthorityPatterns is
// keyed by axis value in the landed config schema (each is a flat
// []string), so this package defines its own convention for extracting a
// three/four-valued axis out of a flat pattern list:
//
//   - FindingsPatterns[0], if present, is the CLEAN signal pattern.
//     FindingsPatterns[1:], if any, are alternative PROBLEMS signal
//     patterns — any one matching counts.
//   - AuthorityPatterns[0], if present, is the APPROVED signal pattern.
//     AuthorityPatterns[1:], if any, are alternative WITHHELD signal
//     patterns — any one matching counts.
//   - A missing index (an empty or short slice) means that signal can never
//     fire; it is not an error.
//
// See Classifier.Classify's doc comment for the full resolution algorithm
// this feeds, including the two grammar-contradiction rules.
type Generation struct {
	// ID identifies this generation. Reported back as Result.MatchedGeneration
	// on a definite match, and named in New's compile-error messages.
	ID string
	// BodyMarker is the anchor substring that must appear as a plain,
	// contiguous substring of the comment body (checked with strings.Contains
	// — never split across a line break, and never inferred from a login)
	// for this generation's grammar to apply at all.
	BodyMarker string
	// FindingsPatterns is a list of Go regular-expression strings; see the
	// pattern-list convention above.
	FindingsPatterns []string
	// AuthorityPatterns is a list of Go regular-expression strings; see the
	// pattern-list convention above.
	AuthorityPatterns []string
}

// Result is the outcome of classifying one comment body.
type Result struct {
	// MatchedGeneration is the ID of the generation whose grammar definitively
	// resolved a Findings value (Clean or Problems) for this body. It is
	// empty whenever Findings is FindingsUnknown — whether that is because no
	// configured generation's BodyMarker appeared in the body at all
	// (Authority Absent) or because a BodyMarker matched but no generation's
	// patterns resolved anything (Authority Pending). A future caller
	// distinguishes "matched generation N cleanly" from "marker present, no
	// match" by checking MatchedGeneration != "" versus Authority == Pending.
	MatchedGeneration string
	Findings          Findings
	Authority         Authority
	// Contradiction is true when the matched generation's own grammar
	// produced an internally-inconsistent reading: see Classify's doc for
	// the two rules. Authority is never Approved when Contradiction is true.
	Contradiction bool
	// ContradictionReason is a human-readable explanation, set iff
	// Contradiction is true.
	ContradictionReason string
}

// compiledGeneration is a Generation with its patterns pre-compiled once at
// New time, mirroring agentregistry.entry / cirollup.Excluder.
type compiledGeneration struct {
	id       string
	marker   string
	clean    *regexp.Regexp   // FindingsPatterns[0]; nil if absent
	problems []*regexp.Regexp // FindingsPatterns[1:]
	approved *regexp.Regexp   // AuthorityPatterns[0]; nil if absent
	withheld []*regexp.Regexp // AuthorityPatterns[1:]
}

// Classifier evaluates comment bodies against a fixed, pre-compiled set of
// Generations. Safe for concurrent use (read-only after New).
type Classifier struct {
	generations []compiledGeneration
}

// New compiles every generation's patterns and returns an error naming the
// generation (its ID) and the field (findings_patterns vs authority_patterns,
// with its index) on the first uncompilable regex — mirroring
// agentregistry.New's precedent. A nil or empty generations slice is valid:
// the resulting Classifier classifies every body FindingsUnknown/Absent, with
// no error (bead pg2-4dz88.1.4's "zero configured generations" requirement).
func New(generations []Generation) (*Classifier, error) {
	compiled := make([]compiledGeneration, 0, len(generations))
	for _, g := range generations {
		cg := compiledGeneration{id: g.ID, marker: g.BodyMarker}

		findingsRes, err := compilePatterns(g.FindingsPatterns)
		if err != nil {
			return nil, fmt.Errorf("verdict: generation %q: findings_patterns: %w", g.ID, err)
		}
		if len(findingsRes) > 0 {
			cg.clean = findingsRes[0]
			cg.problems = findingsRes[1:]
		}

		authorityRes, err := compilePatterns(g.AuthorityPatterns)
		if err != nil {
			return nil, fmt.Errorf("verdict: generation %q: authority_patterns: %w", g.ID, err)
		}
		if len(authorityRes) > 0 {
			cg.approved = authorityRes[0]
			cg.withheld = authorityRes[1:]
		}

		compiled = append(compiled, cg)
	}
	return &Classifier{generations: compiled}, nil
}

// compilePatterns compiles every pattern in order, returning an error naming
// the first uncompilable one's index.
func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("[%d]: compile %q: %w", i, p, err)
		}
		out[i] = re
	}
	return out, nil
}

// Classify walks the Classifier's generations in the ORDER they were
// declared (declaration order is load-bearing — see config.Config's
// VerdictGenerations doc) and returns the two-axis Result for body.
//
// Resolution algorithm:
//
//  1. A generation is a CANDIDATE only if its BodyMarker is a contiguous
//     substring of body. Generations whose marker is absent are skipped
//     entirely — their patterns are never even evaluated.
//  2. Each candidate generation resolves Findings independently: Problems if
//     any of its problems patterns match, else Clean if its clean pattern
//     matches, else FindingsUnknown for that generation.
//  3. A candidate that resolves FindingsUnknown is not a "definite" match and
//     cannot win; only candidates that resolve Clean or Problems compete.
//  4. Among definite candidates, the HIGHEST DECLARED GENERATION WINS: later
//     entries in the slice override earlier ones, so Classify returns the
//     last definite candidate found while walking in order. This is
//     deterministic and never panics, even when every generation in the
//     slice matches.
//  5. If no candidate resolves definitely but at least one generation was a
//     candidate at all (its marker matched), the body IS a verdict but an
//     unrecognized one: Result{Findings: FindingsUnknown, Authority: Pending}.
//  6. If no generation was even a candidate (no configured BodyMarker
//     appears in body), the body is not a verdict at all:
//     Result{Findings: FindingsUnknown, Authority: Absent}.
//
// Within a definite candidate, Authority resolves from Findings plus the
// authority patterns, and flags two grammar contradictions along the way
// (both leave Authority at Withheld — a contradiction is never reported as
// Approved):
//
//   - Findings Problems but the approved pattern ALSO matches: a "problems"
//     body must not also carry an authority-granting line.
//   - Findings Clean but NEITHER the approved pattern NOR any withheld
//     pattern matches: a "clean but not approved" (partial-clean-shaped)
//     body must carry the corresponding withheld signal explaining why.
//
// Absent either contradiction, Findings Problems with no approved-pattern
// match resolves to Authority Withheld by default (a problems verdict is
// never silently treated as approved), and Findings Clean resolves to
// whichever of Approved/Withheld its own pattern explicitly matched.
func (c *Classifier) Classify(body string) Result {
	var (
		best         Result
		haveDefinite bool
		anyCandidate bool
	)
	for _, g := range c.generations {
		if g.marker == "" || !strings.Contains(body, g.marker) {
			continue
		}
		anyCandidate = true

		findings, authority, contradiction, reason := g.classify(body)
		if findings == FindingsUnknown {
			continue
		}
		best = Result{
			MatchedGeneration:   g.id,
			Findings:            findings,
			Authority:           authority,
			Contradiction:       contradiction,
			ContradictionReason: reason,
		}
		haveDefinite = true
	}
	if haveDefinite {
		return best
	}
	if anyCandidate {
		return Result{Findings: FindingsUnknown, Authority: Pending}
	}
	return Result{Findings: FindingsUnknown, Authority: Absent}
}

// classify resolves one generation's Findings/Authority for body, per the
// algorithm documented on Classifier.Classify.
func (g compiledGeneration) classify(body string) (findings Findings, authority Authority, contradiction bool, reason string) {
	cleanMatch := g.clean != nil && g.clean.MatchString(body)
	problemsMatch := matchAny(g.problems, body)
	approvedMatch := g.approved != nil && g.approved.MatchString(body)
	withheldMatch := matchAny(g.withheld, body)

	switch {
	case problemsMatch:
		findings = Problems
	case cleanMatch:
		findings = Clean
	default:
		return FindingsUnknown, Pending, false, ""
	}

	switch findings {
	case Problems:
		if approvedMatch {
			return findings, Withheld, true,
				"problems finding also matched an authority-granting pattern"
		}
		return findings, Withheld, false, ""
	default: // Clean
		switch {
		case approvedMatch:
			return findings, Approved, false, ""
		case withheldMatch:
			return findings, Withheld, false, ""
		default:
			return findings, Withheld, true,
				"clean finding has no corresponding authority-withheld pattern match"
		}
	}
}

// matchAny reports whether any pattern in pats matches body. Nil/empty-safe.
func matchAny(pats []*regexp.Regexp, body string) bool {
	for _, re := range pats {
		if re.MatchString(body) {
			return true
		}
	}
	return false
}
