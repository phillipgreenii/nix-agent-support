package originguard

import (
	"unicode/utf8"

	failsig "github.com/phillipgreenii/pg-failsig"
)

// MaxErrorLen is the longest failure text kept in state and results.
const MaxErrorLen = 300

// RedactTail masks credentials in the WHOLE text first (through the shared
// failure-signature package, so there is one redaction table), then keeps the
// last MaxErrorLen bytes on a UTF-8 boundary. Cutting first could split a
// credential into a fragment no pattern recognizes any more.
func RedactTail(text string) string {
	red := failsig.Redact(text)
	if len(red) <= MaxErrorLen {
		return red
	}
	cut := red[len(red)-MaxErrorLen:]
	for len(cut) > 0 && !utf8.RuneStart(cut[0]) {
		cut = cut[1:]
	}
	return cut
}
