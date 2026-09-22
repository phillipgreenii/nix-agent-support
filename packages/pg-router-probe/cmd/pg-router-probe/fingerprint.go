// fingerprint.go: the pg_router_escalation_fingerprint values this probe
// computes for each finding kind — exact-match only, no fuzzy matching at
// probe time (that judgment stays with the triager) [design: "Bead
// identity and schema", "Fingerprint metadata key" paragraph].
package main

import (
	"sort"
	"strings"
)

// grafanaAlertFingerprint renders "<rule-uid>|<sorted key=value labels>"
// [design: same paragraph, first bullet] — verbatim reuse of lat-survey's
// own fingerprint SCHEME (the scheme is reused; the code is not, per this
// packet's own Contract "Grafana" bullet).
func grafanaAlertFingerprint(ruleUID string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return ruleUID + "|" + strings.Join(parts, ",")
}

// queueGrowthFingerprint renders "queue-growth:<type>" [design: same
// paragraph, fourth bullet]. type is "queue-depth" or "backlog" in this
// probe's own usage (checks.go).
func queueGrowthFingerprint(kind string) string {
	return "queue-growth:" + kind
}

// binaryHashMismatchFingerprint is the fixed-string fingerprint for the
// daemon/handler binary hash sanity check [design: same paragraph, fourth
// bullet]. Unlike queue-growth's fingerprint, this one carries no variable
// band/type — every hash-mismatch finding shares this exact string, so a
// matching existing bead is found purely by fingerprint equality and the
// dedup decision then turns on comparing the CURRENT hash against the
// value already recorded on that bead (dedup.go) — this packet's own
// choice, since the design's severity-band "nothing new" language is a
// poor fit for a fixed-string fingerprint with no band, and the design
// does not itself specify hash-mismatch's own dedup rule [Binding
// decisions: "'Nothing new' rule" bullet, hash-mismatch paragraph].
const binaryHashMismatchFingerprint = "binary-hash-mismatch"
