package sync

import (
	"encoding/json"
)

// workBeadEntity is the minimal subset of an `issue list` entity this
// package decodes for adoption — title/metadata (for ClassifyBead),
// priority/labels (to diff the anchor's conflict-priority nudge without a
// separate `issue show` call, since gather's own `issue list --query
// work-beads` fan-out already returns full entities), state (to recognize a
// closed review-pr bead) and updated_at (to pick the "newest" review-pr bead
// when seeding the last-reviewed head SHA — design section 7.5). Mirrors
// gather.go's own workBeadEntity, widened for this packet's own needs;
// deliberately hand-decoded rather than importing
// packages/pg-connector/pkg/schema (see gather.go's prShowFields doc
// comment for why this docket's packages stay off that import).
type workBeadEntity struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	State     string            `json:"state"`
	Priority  string            `json:"priority"`
	Labels    []string          `json:"labels"`
	Metadata  map[string]string `json:"metadata"`
	UpdatedAt string            `json:"updated_at"`
}

type workBeadsFanOut struct {
	Entities []workBeadEntity `json:"entities"`
}

// adoption is what adoptFromWorkBeads found for one PR among the beads
// gather's own `issue list --query work-beads` call already fetched
// (Facts.WorkBeads) — first-run adoption "on every run, not just literally
// the first" (design section 7.5), reusing that existing call rather than
// sync issuing its own `issue list`.
type adoption struct {
	Anchor *workBeadEntity
	// AnchorNeedsMetadataBackfill is true when Anchor was matched by its
	// `<repo>#<n>:` title prefix alone (no repo/pr_number metadata yet) —
	// design section 7.5: "adopts merge-request beads that carry no repo
	// and pr_number metadata by the <repo>#<n>: title prefix ... a bead
	// adopted by title gets repo and pr_number written on it by the first
	// apply run."
	AnchorNeedsMetadataBackfill bool
	Cycle                       *workBeadEntity
	Review                      *workBeadEntity
}

// adoptFromWorkBeads classifies raw (Facts.WorkBeads, the full fan-out
// result from gather's own `issue list --query work-beads` call) down to
// this one (repo, prNumber)'s anchor/cycle/review-request, via
// ClassifyBead. Malformed/empty raw degrades to an empty adoption (nothing
// found) rather than erroring — gather's own WorkBeads is best-effort
// (Facts.Degraded already names a failed gather call); sync must not
// reintroduce a hard failure over data gather itself treated as optional.
func adoptFromWorkBeads(raw json.RawMessage, repo string, prNumber int) adoption {
	var out adoption
	if len(raw) == 0 {
		return out
	}
	var fanOut workBeadsFanOut
	if err := json.Unmarshal(raw, &fanOut); err != nil {
		return out
	}

	var newestReview *workBeadEntity
	for i := range fanOut.Entities {
		e := &fanOut.Entities[i]
		kind, r, n, ok := ClassifyBead(e.Title, e.Metadata)
		if !ok || r != repo || n != prNumber {
			continue
		}
		switch kind {
		case KindAnchor:
			out.Anchor = e
			_, _, hasMeta := metadataRepoPR(e.Metadata)
			out.AnchorNeedsMetadataBackfill = !hasMeta
		case KindFeedbackCycle:
			out.Cycle = e
		case KindReviewRequest:
			if newestReview == nil || e.UpdatedAt > newestReview.UpdatedAt {
				newestReview = e
			}
		}
	}
	out.Review = newestReview
	return out
}
