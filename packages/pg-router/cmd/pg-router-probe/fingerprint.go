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

// volatileResourceLabels are OTel resource/scope labels that describe how
// the emitting binary was built or instrumented, not what the alert is
// about. They change whenever the emitting daemon is rebuilt (for example a
// Go toolchain bump), and a changed value must not orphan or duplicate the
// open escalation beads for the alert. Each is excluded from the
// fingerprint for that reason:
//   - process_runtime_version, process_runtime_name,
//     process_runtime_description: the language runtime of the emitter
//     (e.g. go1.26.8 vs go1.26.10).
//   - service_version: the emitter's release version.
//   - otel_scope_name, otel_scope_version: the instrumentation library
//     that produced the series.
//   - telemetry_sdk_name, telemetry_sdk_version, telemetry_sdk_language:
//     the OTel SDK that exported it.
var volatileResourceLabels = map[string]bool{
	"process_runtime_version":     true,
	"process_runtime_name":        true,
	"process_runtime_description": true,
	"service_version":             true,
	"otel_scope_name":             true,
	"otel_scope_version":          true,
	"telemetry_sdk_name":          true,
	"telemetry_sdk_version":       true,
	"telemetry_sdk_language":      true,
}

// excludedFromFingerprint reports whether a label is left out of the
// Grafana alert fingerprint, and stripped from stored ones at compare time.
func excludedFromFingerprint(k string) bool {
	return k == grafanaFolderKey || volatileResourceLabels[k]
}

// grafanaAlertFingerprint renders "<rule-uid>|<sorted key=value labels>"
// [design: same paragraph, first bullet] — verbatim reuse of lat-survey's
// own fingerprint SCHEME (the scheme is reused; the code is not, per this
// packet's own Contract "Grafana" bullet).
//
// The "escalation" label (escalationLabelKey) is EXCLUDED from the hash:
// it is routing metadata set by the rule author, not part of the alert's
// identity, so moving an already-registered rule to `escalation: human`
// must keep matching the beads already open for it instead of forking a
// duplicate.
// The "grafana_folder" label (grafanaFolderKey) is excluded for the same
// reason: moving a rule between folders must not change its identity.
// The volatileResourceLabels (build/runtime/SDK identity of the emitting
// binary, e.g. process_runtime_version) are excluded because a rebuild of
// the emitter must not refile every open bead; see that variable for the
// per-label reasons.
// Fingerprints stored on beads filed before these exclusions still carry
// the labels; stripExcludedLabels lets dedup compare across both forms.
// Annotations (summary/description) are never passed in here at all.
func grafanaAlertFingerprint(ruleUID string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == escalationLabelKey || excludedFromFingerprint(k) {
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

// stripExcludedLabels removes every excluded label (grafana_folder and the
// volatileResourceLabels) from a stored or computed Grafana fingerprint
// ("<uid>|k=v,k=v"), so fingerprints recorded before an exclusion existed
// compare equal to current ones. A fingerprint with no "|" (non-grafana
// kinds) is returned unchanged. A value containing commas leaves fragments
// without "=" directly after its label; those are dropped with it.
func stripExcludedLabels(fp string) string {
	uid, labels, ok := strings.Cut(fp, "|")
	if !ok {
		return fp
	}
	parts := strings.Split(labels, ",")
	kept := parts[:0:0]
	inExcluded := false
	for _, p := range parts {
		k, _, hasEq := strings.Cut(p, "=")
		switch {
		case hasEq && excludedFromFingerprint(k):
			inExcluded = true
		case inExcluded && !hasEq:
			// continuation of a comma-containing excluded value
		default:
			inExcluded = false
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
