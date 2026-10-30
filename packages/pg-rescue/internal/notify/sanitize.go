package notify

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize prepares one value for a notification. It keeps the notification
// itself well-behaved; the daemon's contract rejects over-cap input, so the
// value is also cut to maxBytes (the daemon's byte cap), on a rune boundary.
// Newlines, carriage
// returns, tabs and the Unicode line and paragraph separators become one space
// so a multi-line summary stays readable. Every other control character
// (C0, DEL, C1) is removed. Invalid UTF-8 becomes U+FFFD. Surrounding space is
// trimmed, and the result is cut to maxBytes bytes without splitting a rune.
func Sanitize(s string, maxBytes int) string {
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
	if len(out) > maxBytes {
		for maxBytes > 0 && !utf8.RuneStart(out[maxBytes]) {
			maxBytes--
		}
		// A cut can leave a trailing space that TrimSpace would have removed.
		out = strings.TrimRightFunc(out[:maxBytes], unicode.IsSpace)
	}
	return out
}
