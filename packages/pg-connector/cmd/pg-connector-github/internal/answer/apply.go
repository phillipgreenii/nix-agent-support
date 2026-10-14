package answer

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// groups returns a copy of p.Groups so a result never aliases the Provenance;
// nil when there is none (the field is then omitted from the wire).
func (p Provenance) groups() []schema.GroupInfo {
	if len(p.Groups) == 0 {
		return nil
	}
	return append([]schema.GroupInfo(nil), p.Groups...)
}

// ApplyPR sets the annotations on a show answer. PR already carries as_of and
// stale, so those are set too (as_of only when a group was served).
func ApplyPR(r *schema.PR, p Provenance) {
	age := p.AgeSeconds
	r.ServedFrom = p.ServedFrom
	r.AgeSeconds = &age
	r.Groups = p.groups()
	r.Stale = p.Stale
	if p.AsOf != "" {
		r.AsOf = p.AsOf
	}
}

// ApplyFiles sets the annotations on a files answer (which has no as_of).
func ApplyFiles(r *schema.PRFilesResult, p Provenance) {
	age, stale := p.AgeSeconds, p.Stale
	r.ServedFrom = p.ServedFrom
	r.AgeSeconds = &age
	r.Stale = &stale
	r.Groups = p.groups()
}

// ApplyCommits sets the annotations on a commits answer (which has no as_of).
func ApplyCommits(r *schema.PRCommitsResult, p Provenance) {
	age, stale := p.AgeSeconds, p.Stale
	r.ServedFrom = p.ServedFrom
	r.AgeSeconds = &age
	r.Stale = &stale
	r.Groups = p.groups()
}

// ApplyPending sets the annotations on a review_pending answer, as_of included
// (only when a group was served).
func ApplyPending(r *pr.PendingReviewResult, p Provenance) {
	age, stale := p.AgeSeconds, p.Stale
	r.ServedFrom = p.ServedFrom
	r.AgeSeconds = &age
	r.Stale = &stale
	r.Groups = p.groups()
	if p.AsOf != "" {
		r.AsOf = p.AsOf
	}
}

// ApplyRuns sets the annotations on EVERY element of a list_runs answer, whose
// result is an array that must stay an array; an empty list carries none (only
// the router annotation, Record, then reports it). Each element gets its own
// copy of groups[] and age_seconds.
func ApplyRuns(runs []schema.CIRun, p Provenance) {
	for i := range runs {
		age := p.AgeSeconds
		runs[i].ServedFrom = p.ServedFrom
		runs[i].AgeSeconds = &age
		runs[i].Groups = p.groups()
		runs[i].Stale = p.Stale
		if p.AsOf != "" {
			runs[i].AsOf = p.AsOf
		}
	}
}
