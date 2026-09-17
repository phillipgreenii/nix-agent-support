package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
)

// fbsumLabelPrefix stashes the digest of the unaddressed-feedback set a
// feedback-cycle bead already covers — ported verbatim from
// packages/pg-pr/internal/beadsbridge/bridge.go's fbsumLabelPrefix.
const fbsumLabelPrefix = "fbsum:"

// unaddressedCommentIDs returns the sorted comment ids whose disposition is
// still "open" (neither will-fix, wont-fix, nor no-action) — design section
// 7.5's "unaddressed feedback" set. interpret.computeDispositions already
// sorts by CommentID, but this package re-sorts defensively rather than
// depending on that internal ordering guarantee.
func unaddressedCommentIDs(dispositions []interpret.Disposition) []string {
	var out []string
	for _, d := range dispositions {
		if d.Verdict == interpret.DispositionOpen {
			out = append(out, d.CommentID)
		}
	}
	sort.Strings(out)
	return out
}

// fbsumDigest computes the unaddressed-feedback set's digest: a
// straight port of packages/pg-pr/internal/store/feedback.go's
// UnaddressedFeedback digest computation (a sha256 hash over the sorted
// fingerprint set, each entry null-byte-terminated, hex-encoded and
// truncated to 12 chars) — here over sorted comment ids rather than
// pg-pr's own feedback-row fingerprints, since gather.Facts/interpret carry
// no separate fingerprint concept (documented deviation, matching
// interpret.go's own "no Go source to port from, re-implemented against
// this phase's actual data shape" precedent). Returns "" when there is
// nothing unaddressed — mirroring UnaddressedFeedback's own "Unaddressed ==
// 0 => no digest" contract, so an empty/healthy PR never carries a stale
// fbsum label.
func fbsumDigest(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// staleFbsumLabels lists the `fbsum:` labels that are NOT the current
// digest, so an update that adds the new marker drops the old ones in the
// same call — ported verbatim from
// packages/pg-pr/internal/beadsbridge/bridge.go's staleFbsumLabels.
func staleFbsumLabels(labels []string, keepDigest string) []string {
	var out []string
	for _, l := range labels {
		if strings.HasPrefix(l, fbsumLabelPrefix) && l != fbsumLabelPrefix+keepDigest {
			out = append(out, l)
		}
	}
	return out
}

// hasLabel reports whether want is present in labels.
func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// renderCycleDescription renders the feedback cycle's description — the
// rendered summary of unaddressed items design section 7.5 pins as the
// bead's description. A simplified rendering relative to pg-pr's own
// renderCycleNote (which breaks down by feedback kind and lists raising
// reviewer logins from a persisted FeedbackSummary): gather.Facts/
// interpret.Dispositions carry only comment ids and verdicts, not kind or
// author (documented deviation — see interpret.go's own package doc for
// the same "Facts does not carry X" pattern), so this reports the count and
// the comment ids only.
func renderCycleDescription(repo string, prNumber int, ids []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unaddressed reviewer feedback on %s#%d.\n\n%d unaddressed item(s)", repo, prNumber, len(ids))
	if len(ids) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(ids, ", "))
	}
	b.WriteString(".")
	return b.String()
}
