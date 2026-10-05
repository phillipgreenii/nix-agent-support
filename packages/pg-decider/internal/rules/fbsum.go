package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// fbsumLabelPrefix marks the digest of the unaddressed-feedback set a
// process-feedback cycle covers (ported from pg-desk internal/sync/fbsum.go).
const fbsumLabelPrefix = "fbsum:"

// fbsumDigest is the digest of a sorted set of unaddressed comment ids: each
// id sha256-hashed null-byte-terminated, hex-encoded and truncated to 12
// characters. It is empty when nothing is unaddressed, so a healthy PR never
// carries a stale fbsum label. Ported unchanged from pg-desk's fbsumDigest;
// the caller passes the ids sorted.
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

// fbsumStaleLabels lists the fbsum: labels that are not the current digest,
// so an update that adds the new marker drops the old ones in the same
// action (ported from pg-desk's staleFbsumLabels).
func fbsumStaleLabels(labels []string, keepDigest string) []string {
	var out []string
	for _, l := range labels {
		if strings.HasPrefix(l, fbsumLabelPrefix) && l != fbsumLabelPrefix+keepDigest {
			out = append(out, l)
		}
	}
	return out
}

// fbsumCycleDescription renders the cycle's description: the unaddressed
// items (ported from pg-desk's renderCycleDescription).
func fbsumCycleDescription(repo string, prNumber int, ids []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unaddressed reviewer feedback on %s#%d.\n\n%d unaddressed item(s)", repo, prNumber, len(ids))
	if len(ids) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(ids, ", "))
	}
	b.WriteString(".")
	return b.String()
}
