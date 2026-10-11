package focus

import (
	"encoding/json"
	"strings"
)

// What `focus explain` reads about a source's bead beyond its state word: the
// bead's hold marker and its latest audit comment, both from the store (the
// bead entity's stored issue snapshot), never from the tracker.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; it is a pure function of Inputs.

// auditRulePrefix begins the rule line of an audit comment the decider writes
// on a focus bead.
const auditRulePrefix = "rule: focus.item"

// BeadNote is what the store holds about the bead of one source: Present is
// false when there is no such bead. Marker is the bead's hold marker ("" when
// absent). AuditStored is true when the bead's stored snapshot carries a
// comment list at all; Audit is the body of the latest comment of the audit
// rule ("" when there is none).
type BeadNote struct {
	Present     bool
	Key         Key
	Marker      string
	AuditStored bool
	Audit       string
}

// ReadBeadNote reads the BeadNote of the bead of k (BeadKeyOf), from the
// stored entity of that bead. It reads the snapshot's issue_show.comments when
// the connector stored them; a snapshot without the member is "not stored",
// which is not the same as "no audit comment".
func ReadBeadNote(in Inputs, k Key) BeadNote {
	bk, ok := BeadKeyOf(in, k)
	if !ok {
		return BeadNote{}
	}
	e, stored := in.Entities[bk]
	if !stored {
		return BeadNote{Present: true, Key: bk}
	}
	n := BeadNote{Present: true, Key: bk, Marker: metaString(decodeEntityFacts(e).metadata, metaFocusHold)}
	var f struct {
		IssueShow struct {
			Comments *[]json.RawMessage `json:"comments"`
		} `json:"issue_show"`
	}
	if json.Unmarshal([]byte(e.Facts), &f) != nil || f.IssueShow.Comments == nil {
		return n
	}
	n.AuditStored = true
	for _, raw := range *f.IssueShow.Comments {
		if body := commentBody(raw); isAuditBody(body) {
			n.Audit = body // the last one wins
		}
	}
	return n
}

// commentBody is the text of one stored comment: a bare string, or an object
// whose body, text or content member is a string.
func commentBody(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Body    string `json:"body"`
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	for _, v := range []string{o.Body, o.Text, o.Content} {
		if v != "" {
			return v
		}
	}
	return ""
}

// isAuditBody reports whether a comment is a focus-rule audit comment: its
// rule line (the first line, or the second when a one-line heading precedes
// it) starts with auditRulePrefix.
func isAuditBody(body string) bool {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i := 0; i < len(lines) && i < 2; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), auditRulePrefix) {
			return true
		}
	}
	return false
}
