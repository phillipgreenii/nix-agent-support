package groups

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Normalise is the compared form of a summary: as_of, stale, version,
// updated_at and the updated token are zeroed, because they MUST NOT take part
// in a comparison. The argument is not modified.
func Normalise(c SummaryContent) SummaryContent {
	c.Issue.AsOf = ""
	c.Issue.Stale = false
	c.Issue.Version = 0
	c.Issue.UpdatedAt = ""
	c.Updated = ""
	return c
}

// Stored is the form the persister stores, what the change detection encodes
// for the engine and what the classifier receives: Normalise with the Updated
// token kept.
func Stored(c SummaryContent) SummaryContent {
	n := Normalise(c)
	n.Updated = c.Updated
	return n
}

// NormaliseDetail is the compared form of a detail: the two operator facts are
// zeroed (they are derived) and each comment body is cut to at most bodyCap
// bytes on a UTF-8 boundary. A bodyCap of zero or less means no cut. The
// argument is not modified.
func NormaliseDetail(d DetailContent, bodyCap int) DetailContent {
	d.StatusChangedAt = ""
	d.OperatorUpdatedAt = ""
	if d.Comments != nil {
		cs := make([]Comment, len(d.Comments))
		for i, c := range d.Comments {
			c.Body = cutUTF8(c.Body, bodyCap)
			cs[i] = c
		}
		d.Comments = cs
	}
	return d
}

// cutUTF8 returns s cut to at most n bytes, never inside a multi-byte rune. A
// limit of zero or less leaves s whole.
func cutUTF8(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Fingerprint is the sha256 hex of the JSON encoding of the normalised summary,
// so a change to updated alone never changes it.
func Fingerprint(c SummaryContent) string {
	b, _ := json.Marshal(Normalise(c)) // a plain struct always encodes
	return groupkit.Fingerprint(b)
}

// FingerprintDetail is the sha256 hex of the JSON encoding of the normalised
// detail (bodyCap as for NormaliseDetail).
func FingerprintDetail(d DetailContent, bodyCap int) string {
	b, _ := json.Marshal(NormaliseDetail(d, bodyCap)) // a plain struct always encodes
	return groupkit.Fingerprint(b)
}

// IsClosed is true exactly when the issue's status category is done.
func IsClosed(i schema.Issue) bool { return i.StatusCategory == "done" }
