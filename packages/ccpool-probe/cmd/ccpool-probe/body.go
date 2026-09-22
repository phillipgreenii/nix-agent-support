// body.go: renders the bd issue body/comment template [Binding
// decisions: "Body template" code block] — mirrors local-alert-triage's
// own template shape (and packages/pg-router-probe/cmd/pg-router-probe/body.go's
// identical rendering):
//
//	Pg-Router-Escalation-Fingerprint: <fingerprint>
//
//	Source: pg-router-probe | ccpool-probe
//	Finding: <one-line description>
//	Since: <first-seen timestamp>
//	Evidence:
//	<raw evidence: alert payload / session id + tail excerpt / metric values>
//
// This binary always renders literal "Source: ccpool-probe" — the
// "pg-router-probe |" alternation in the design's own template documents
// the TWO sibling packets that both use this template, not a runtime
// choice either one makes.
package main

import (
	"fmt"
	"time"
)

const escalationSource = "ccpool-probe"

// renderBody renders the FULL body used both for a brand-new bead's
// --description and for a comment appended to an already-open one — the
// two call sites differ only in which pg-connector verb carries this same
// string (create's --description vs. comment's --body); later updates
// append via comment, never overwrite the original body [design: "Body
// template" closing sentence].
func renderBody(f finding, firstSeen time.Time, skippedNote string) string {
	body := fmt.Sprintf(
		"Pg-Router-Escalation-Fingerprint: %s\n\nSource: %s\nFinding: %s\nSince: %s\nEvidence:\n%s\n",
		f.Fingerprint, escalationSource, f.Summary, firstSeen.UTC().Format(time.RFC3339), f.Evidence,
	)
	if skippedNote != "" {
		body += "\nNote: this run's other sub-check(s) were skipped/degraded: " + skippedNote + "\n"
	}
	return body
}
