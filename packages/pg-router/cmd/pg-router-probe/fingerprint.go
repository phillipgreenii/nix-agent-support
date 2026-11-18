// fingerprint.go: the pg_router_escalation_fingerprint values this probe
// computes for each finding kind — exact-match only, no fuzzy matching at
// probe time (that judgment stays with the triager) [design: "Bead
// identity and schema", "Fingerprint metadata key" paragraph].
package main

import (
	"sort"
	"strings"
)

// escalationLabelKey is the alert-rule label that routes a firing alert's
// bead (pg2-x7ie2). The recognised value is escalationHuman; absent (or any
// other value) means the default route, an "escalated"-only bead for the
// triager. See escalationLabels in run.go.
const (
	escalationLabelKey = "escalation"
	escalationHuman    = "human"

	// grafanaFolderKey is the label Grafana stamps with the alert rule's
	// folder. A folder move or rename is not a change of the alert's
	// identity, so it is excluded from the fingerprint (operator-confirmed
	// intent: a folder rename MUST NOT orphan or duplicate open escalation
	// beads).
	grafanaFolderKey = "grafana_folder"
)

// grafanaAlertFingerprint renders "<rule-uid>|<sorted key=value labels>"
// [design: same paragraph, first bullet] — verbatim reuse of lat-survey's
// own fingerprint SCHEME (the scheme is reused; the code is not, per this
// packet's own Contract "Grafana" bullet).
//
// The "escalation" label (escalationLabelKey) is EXCLUDED from the hash
// (pg2-x7ie2): it is routing metadata set by the rule author, not part of
// the alert's identity, so moving an already-registered rule to
// `escalation: human` must keep matching the beads already open for it
// instead of forking a duplicate (the pg2-dvkbh/pg2-imr6o class).
// The "grafana_folder" label (grafanaFolderKey) is excluded for the same
// reason: moving a rule between folders must not change its identity.
// Fingerprints stored on beads filed before that exclusion still carry the
// folder; stripGrafanaFolder lets dedup compare across both forms.
// Annotations (summary/description) are never passed in here at all.
func grafanaAlertFingerprint(ruleUID string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == escalationLabelKey || k == grafanaFolderKey {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return ruleUID + "|" + strings.Join(parts, ",")
}

// stripGrafanaFolder removes the grafana_folder=<value> label from a
// stored or computed Grafana fingerprint ("<uid>|k=v,k=v"), so fingerprints
// recorded before the folder was excluded compare equal to current ones.
// A fingerprint with no "|" (non-grafana kinds) is returned unchanged. A
// folder value containing commas leaves fragments without "=" directly
// after the folder label; those are dropped with it.
func stripGrafanaFolder(fp string) string {
	uid, labels, ok := strings.Cut(fp, "|")
	if !ok {
		return fp
	}
	parts := strings.Split(labels, ",")
	kept := parts[:0:0]
	inFolder := false
	for _, p := range parts {
		switch {
		case strings.HasPrefix(p, grafanaFolderKey+"="):
			inFolder = true
		case inFolder && !strings.Contains(p, "="):
			// continuation of a comma-containing folder value
		default:
			inFolder = false
			kept = append(kept, p)
		}
	}
	return uid + "|" + strings.Join(kept, ",")
}

// binaryHashMismatchFingerprint is the fixed-string fingerprint for the
// daemon/handler binary hash sanity check [design: same paragraph, fourth
// bullet]. It carries no variable
// band/type — every hash-mismatch finding shares this exact string, so a
// matching existing bead is found purely by fingerprint equality and the
// dedup decision then turns on comparing the CURRENT hash against the
// value already recorded on that bead (dedup.go) — this packet's own
// choice, since the design's severity-band "nothing new" language is a
// poor fit for a fixed-string fingerprint with no band, and the design
// does not itself specify hash-mismatch's own dedup rule [Binding
// decisions: "'Nothing new' rule" bullet, hash-mismatch paragraph].
const binaryHashMismatchFingerprint = "binary-hash-mismatch"
