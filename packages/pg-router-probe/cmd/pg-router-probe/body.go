// body.go: renders the bd issue body/comment template [Binding
// decisions: "Body template" code block] — mirrors local-alert-triage's
// own template shape:
//
//	Pg-Router-Escalation-Fingerprint: <fingerprint>
//
//	Source: pg-router-probe | ccpool-probe
//	Finding: <one-line description>
//	Since: <first-seen timestamp>
//	Evidence:
//	<raw evidence: alert payload / session id + tail excerpt / metric values>
//
// This binary always renders literal "Source: pg-router-probe" — the
// "| ccpool-probe" alternation in the design's own template documents
// the TWO sibling packets that both use this template, not a runtime
// choice either one makes.
package main

import (
	"fmt"
	"strings"
	"time"
)

const escalationSource = "pg-router-probe"

// renderBody renders the FULL body used both for a brand-new bead's
// --description and for a comment appended to an already-open one — the
// two call sites differ only in which pg-connector verb carries this same
// string (create's --description vs. comment's --body); later updates
// append via comment, never overwrite the original body [design: "Body
// template" closing sentence].
//
// predecessorID, when non-empty, names the newest CLOSED bead for the same
// fingerprint (pg2-3tt2e); it renders as a "Predecessor:" line so a human
// or triager can follow a re-firing alert back to its earlier escalation.
func renderBody(f finding, firstSeen time.Time, skippedNote, predecessorID string) string {
	body := fmt.Sprintf(
		"Pg-Router-Escalation-Fingerprint: %s\n\nSource: %s\nFinding: %s\nSince: %s\nEvidence:\n%s\n",
		f.Fingerprint, escalationSource, f.Summary, firstSeen.UTC().Format(time.RFC3339), f.Evidence,
	)
	if f.Escalation == escalationHuman {
		body += remediationSection(f)
	}
	if predecessorID != "" {
		body += "\nPredecessor: " + predecessorID + " (closed; this alert fired again)\n"
	}
	if skippedNote != "" {
		body += "\nNote: this run's other sub-check(s) were skipped/degraded: " + skippedNote + "\n"
	}
	return body
}

// remediationSection renders the alert's static annotations for a bead filed
// straight to the operator (escalation=human, pg2-x7ie2): such a bead never
// passes through a triager, so the bead itself must say what to do. The
// probe never closes beads, hence the closing instruction.
func remediationSection(f finding) string {
	var b strings.Builder
	b.WriteString("\nRemediation:\n")
	if f.AlertSummary != "" {
		b.WriteString(f.AlertSummary + "\n")
	}
	if f.AlertDescription != "" {
		b.WriteString(f.AlertDescription + "\n")
	}
	b.WriteString("Close this bead once the alert clears after remediation (pg-router-probe never closes beads).\n")
	return b.String()
}
