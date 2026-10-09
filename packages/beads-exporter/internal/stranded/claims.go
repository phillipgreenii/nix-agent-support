// Package stranded decides, for every claimed bead, whether its claim still
// has a live owner, using Claude session transcript activity.
//
// A claim is live when its claimant's session shows recent activity. The
// signal is the modification time of a transcript file, never a process check
// (an actor id is not a session id) and never a bare mention of the assignee:
// only a transcript that uses the assignee as a claim value counts. Detection
// is report-only; nothing here releases or clears a claim.
package stranded

import (
	"bytes"
	"strings"
)

// Claim values are recognised in three shapes, each in raw or JSON-escaped form
// (a transcript stores a shell command or a tool result as an escaped JSON
// string, so a quote there appears as \"):
//
//	--actor <value>   --actor "<value>"   --actor=<value>
//	BEADS_ACTOR=<value>   BEADS_ACTOR="<value>"
//	"assignee": "<value>"
//
// The scan is anchor-driven: it finds the three anchors, parses the value that
// follows and reports it. It does not know the candidate assignees, so one pass
// over a file serves every candidate and a candidate that appears later needs
// no rescan.
const (
	anchorActorFlag  = "--actor"
	anchorActorEnv   = "BEADS_ACTOR="
	anchorAssignee   = "assignee"
	maxAnchorLen     = len(anchorActorEnv) // the longest anchor
	maxGapLen        = 16                  // longest run of quote, backslash, space and colon bytes between an anchor and its value
	maxValueLen      = 128                 // longest claim value recognised; a longer run of value bytes is ignored
	lookbehindLen    = 1                   // the assignee anchor must be preceded by a quote
	terminatorLen    = 1                   // a value is decided only once the byte after it has been seen
	gapQuoteBytes    = "\"'\\"
	gapSpaceBytes    = " \t"
	valueBytes       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-:@/+"
	valueTrimmedTail = ".:"
)

// maxNeedleLen is the longest span of bytes one claim occurrence can occupy:
// the lookbehind byte, the anchor, the gap, the value and the byte that ends
// the value. A scan that resumes maxNeedleLen-1 bytes before the end of what it
// has already read therefore re-sees every occurrence that was cut off at the
// old end of the file, and decides it once the following bytes exist.
const maxNeedleLen = lookbehindLen + maxAnchorLen + maxGapLen + maxValueLen + terminatorLen

var isValueByte = func() [256]bool {
	var t [256]bool
	for i := 0; i < len(valueBytes); i++ {
		t[valueBytes[i]] = true
	}
	return t
}()

func inSet(b byte, set string) bool { return strings.IndexByte(set, b) >= 0 }

// scanClaims reports every claim value decided inside buf. An occurrence whose
// value runs up to the end of buf is undecided and is NOT reported: the caller
// re-reads the last maxNeedleLen-1 bytes with the next chunk.
func scanClaims(buf []byte, emit func(string)) {
	for _, a := range [...]string{anchorActorFlag, anchorActorEnv, anchorAssignee} {
		anchor := []byte(a)
		for from := 0; from < len(buf); {
			i := bytes.Index(buf[from:], anchor)
			if i < 0 {
				break
			}
			at := from + i
			from = at + len(anchor)
			if v, ok := parseClaim(buf, a, at); ok {
				emit(v)
			}
		}
	}
}

// parseClaim parses the value of the anchor that starts at buf[at].
func parseClaim(buf []byte, anchor string, at int) (string, bool) {
	pos := at + len(anchor)
	gapStart := pos
	skip := func(set string) {
		for pos < len(buf) && inSet(buf[pos], set) {
			pos++
		}
	}
	switch anchor {
	case anchorActorFlag:
		// "--actors" and "--actor-x" are different flags: the flag must be
		// followed by a space, a tab or "=".
		if pos >= len(buf) || !(inSet(buf[pos], gapSpaceBytes) || buf[pos] == '=') {
			return "", false
		}
		skip(gapSpaceBytes + "=")
		skip(gapQuoteBytes)
	case anchorActorEnv:
		skip(gapQuoteBytes)
	case anchorAssignee:
		// Only the JSON key "assignee": the anchor is preceded by a quote,
		// followed by the key's closing quote, a colon and an opening quote.
		// A bare word, or a null/number value, is not a claim.
		if at < lookbehindLen || buf[at-1] != '"' {
			return "", false
		}
		skip(gapQuoteBytes)
		skip(gapSpaceBytes)
		if pos >= len(buf) || buf[pos] != ':' {
			return "", false
		}
		pos++
		skip(gapSpaceBytes)
		valueGap := pos
		skip(gapQuoteBytes)
		if pos == valueGap {
			return "", false
		}
	}
	if pos-gapStart > maxGapLen {
		return "", false
	}
	start := pos
	for pos < len(buf) && isValueByte[buf[pos]] && pos-start <= maxValueLen {
		pos++
	}
	if pos >= len(buf) {
		return "", false // undecided: the value may continue in bytes not yet read
	}
	if pos-start == 0 || pos-start > maxValueLen {
		return "", false
	}
	v := buf[start:pos]
	for len(v) > 0 && inSet(v[len(v)-1], valueTrimmedTail) {
		v = v[:len(v)-1]
	}
	if len(v) == 0 {
		return "", false
	}
	return string(v), true
}
