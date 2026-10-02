package notify

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxValueChars caps the title, the body and the sound name, each, in
// characters (runes, not bytes).
const MaxValueChars = 200

// Sanitize prepares one value for a notification. The value is never part of
// script text (it travels as argv), so this is not what keeps the script
// fixed; it keeps the notification itself well-behaved. Newlines, carriage
// returns, tabs and the Unicode line and paragraph separators become one space
// so a multi-line summary stays readable. Every other control character
// (C0, DEL, C1) is removed. Invalid UTF-8 becomes U+FFFD. Surrounding space is
// trimmed, and the result is cut to MaxValueChars characters.
func Sanitize(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == ' ':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > MaxValueChars {
		out = string([]rune(out)[:MaxValueChars])
		// A cut can leave a trailing space that TrimSpace would have removed.
		out = strings.TrimRightFunc(out, unicode.IsSpace)
	}
	return out
}
