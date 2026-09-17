package sync

import (
	"regexp"
	"strconv"
)

// Kind names the three bead shapes sync produces/consumes, reproduced
// verbatim from the docket design's section 7.5 "Bead shapes" table.
const (
	KindAnchor        = "anchor"
	KindFeedbackCycle = "feedback-cycle"
	KindReviewRequest = "review-request"
)

// reviewPrKeyRE/processFeedbackKeyRE/anchorTitleRE parse the three bead-title
// shapes design section 7.5 pins verbatim: `review-pr: <repo>#<n>`,
// `process-feedback: <repo>#<n>`, and `<repo>#<n>: <pr title>` (the
// merge-request/anchor shape — the rest of the title, the PR's own title, is
// unconstrained free text, so this only anchors on the FIRST `#<digits>: `
// sequence; repo remotes never contain `#`, so `[^#]+` cannot mis-split on a
// `#` that belongs to the PR title instead of the repo/number separator).
var (
	reviewPrKeyRE        = regexp.MustCompile(`^review-pr: ([^#]+)#(\d+)$`)
	processFeedbackKeyRE = regexp.MustCompile(`^process-feedback: ([^#]+)#(\d+)$`)
	anchorTitleRE        = regexp.MustCompile(`^([^#]+)#(\d+): `)
)

// ClassifyBead is the bead-shape recognition / dedup-key resolution
// capability this packet's Contract "Produces" section names: given a
// candidate bead's title and metadata, decide whether it is an anchor, a
// feedback cycle, or a review request for some (repo, pr_number), or none of
// those. This is the SAME parsing convention this packet's own adoption
// sweep (adoptFromWorkBeads, below) and this docket's sibling "pg-desk: run
// issue for the beads backend" packet's reverse (bead -> linked PR)
// resolution both use — do not implement a second parser for the same
// title/metadata shapes.
//
// Classification is title-driven: none of the three kinds carries a "kind"
// metadata field (design section 7.5's table), so metadata alone can never
// distinguish which kind a bead is — only its title can. metadata is
// consulted only as a cross-check: when the title parses to a (repo,
// pr_number) AND metadata separately carries its own repo/pr_number, a
// disagreement between the two is treated as unclassifiable (ok=false)
// rather than trusting either blindly.
func ClassifyBead(title string, metadata map[string]string) (kind, repo string, prNumber int, ok bool) {
	metaRepo, metaNumber, hasMeta := metadataRepoPR(metadata)

	check := func(k, r string, n int) (string, string, int, bool) {
		if hasMeta && (metaRepo != r || metaNumber != n) {
			return "", "", 0, false
		}
		return k, r, n, true
	}

	if m := reviewPrKeyRE.FindStringSubmatch(title); m != nil {
		n, _ := strconv.Atoi(m[2])
		return check(KindReviewRequest, m[1], n)
	}
	if m := processFeedbackKeyRE.FindStringSubmatch(title); m != nil {
		n, _ := strconv.Atoi(m[2])
		return check(KindFeedbackCycle, m[1], n)
	}
	if m := anchorTitleRE.FindStringSubmatch(title); m != nil {
		n, _ := strconv.Atoi(m[2])
		return check(KindAnchor, m[1], n)
	}
	return "", "", 0, false
}

// metadataRepoPR reads a bead's metadata.repo/metadata.pr_number pair (the
// design's own dedup-key vocabulary for anchors and review requests), or
// ok=false when either is absent/unparseable.
func metadataRepoPR(metadata map[string]string) (repo string, prNumber int, ok bool) {
	if metadata == nil {
		return "", 0, false
	}
	repo = metadata["repo"]
	if repo == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(metadata["pr_number"])
	if err != nil {
		return "", 0, false
	}
	return repo, n, true
}
