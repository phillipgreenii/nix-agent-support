package interpret

import "sort"

// Feedback disposition verdicts — the section 7.7 vocabulary
// (`pg-desk feedback set <pr> <comment-id> --disposition open|will-fix|
// wont-fix|no-action`).
const (
	DispositionOpen     = "open"
	DispositionWillFix  = "will-fix"
	DispositionWontFix  = "wont-fix"
	DispositionNoAction = "no-action"
)

// computeDispositions evaluates the base disposition rule set over every
// comment and thread of the PR (top-level comments plus every review's
// inline comments) — "live recompute, idempotent" per the design's Interpret
// bullet. There is no Go source in this repo to port "df-feedback's rule
// set" from (see interpret.go's package doc) — this is a documented, cited
// deviation, not a port: a comment/thread the code host has already marked
// Resolved needs no further action (DispositionNoAction); everything else
// starts DispositionOpen. Recorded overrides are NOT applied here — see
// ApplyDispositionOverrides.
//
// Sorted by CommentID for determinism (the packet's own
// deterministic-with-fixed-clock acceptance criterion requires byte-identical
// output across runs with the same input; map iteration order is not
// stable, so every comment is appended in the PR's own fixed order and then
// sorted).
func computeDispositions(pr prShow) []Disposition {
	var out []Disposition
	add := func(c prComment) {
		if c.ID == "" {
			return
		}
		verdict := DispositionOpen
		if c.Resolved {
			verdict = DispositionNoAction
		}
		out = append(out, Disposition{CommentID: c.ID, Verdict: verdict})
	}
	for _, c := range pr.Comments {
		add(c)
	}
	for _, r := range pr.Reviews {
		for _, c := range r.Comments {
			add(c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CommentID < out[j].CommentID })
	return out
}

// ApplyDispositionOverrides merges recorded operator/agent overrides
// (annotation-table rows, keyed by comment id -> disposition verdict) onto
// base, returning a NEW slice — base is never mutated. An override always
// wins over the rule set's own verdict, on every call (the binding
// decision's "on every re-run" — see interpret.go's Disposition doc for why
// this is a separate exported step rather than folded into Interpret
// itself: Interpret's pinned signature is (facts, clock, cfg), which has no
// parameter carrying the store's annotation overrides — only packet 6, which
// reads the store, can supply them. Calling this after every Interpret run
// is what makes an override survive a re-run: the rule set recomputes its
// own verdict from scratch every time, and this function reapplies the same
// override on top of it every time, so the two calls together are
// idempotent and the override's value never drifts.
//
// An override entry naming a comment id absent from base (e.g. the comment
// no longer exists) is silently ignored — there is no row in base to carry
// it.
func ApplyDispositionOverrides(base []Disposition, overrides map[string]string) []Disposition {
	out := make([]Disposition, len(base))
	copy(out, base)
	if len(overrides) == 0 {
		return out
	}
	for i, d := range out {
		if v, ok := overrides[d.CommentID]; ok {
			d.Verdict = v
			d.Overridden = true
			out[i] = d
		}
	}
	return out
}
