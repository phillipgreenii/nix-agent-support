// Package textsafe provides Sanitize, a pure function that strips terminal
// control sequences from a string before it is measured for display width
// or printed (Task 3.7). It is a LEAF package: it imports nothing from any
// other Phase 3 task. Its first consumer is Task 3.8's status human-form
// renderer.
//
// Sanitize-before-measure (Task 3.7 Step 2): ANY display-width computation
// (rune count, cell width, truncation to a terminal column budget) MUST run
// on the string Sanitize returns, never on the raw one — a raw CSI/OSC/DCS
// sequence has zero display width but nonzero byte/rune length, so measuring
// the raw string first and sanitizing only for print silently corrupts the
// width budget.
package textsafe

import "unicode/utf8"

// Sanitize strips C0 control bytes, 8-bit C1 control bytes, CSI escape
// sequences, OSC (including OSC-8 hyperlink) sequences, and DCS/APC
// sequences from s, returning the result. Every byte it removes is one that
// a terminal would otherwise interpret as a control code rather than render
// as a glyph, so the returned string's rune count is safe to use as an
// approximation of display width; ordinary UTF-8 text — including combining
// marks, whose continuation bytes can fall in the same numeric range as a
// standalone 8-bit C1 byte — passes through byte-for-byte untouched.
//
// Sanitize never panics on arbitrary byte input, including a truncated or
// unterminated escape sequence (an OSC/DCS/APC sequence with no terminator,
// or a CSI/escape sequence cut off mid-string): every scan for a terminator
// is bounded by len(s), so an unterminated sequence is dropped up to the end
// of the string rather than scanned past it or left half-consumed.
func Sanitize(s string) string {
	n := len(s)
	out := make([]byte, 0, n)
	for i := 0; i < n; {
		c := s[i]

		if c == 0x1b { // ESC: the lead byte of every escape-based class below.
			i = skipEscape(s, i)
			continue
		}

		if c < 0x80 {
			// ASCII fast path: C0 controls (0x00-0x1F) and DEL (0x7F) are
			// dropped; everything else (printable ASCII) passes through.
			if c < 0x20 || c == 0x7f {
				i++
				continue
			}
			out = append(out, c)
			i++
			continue
		}

		// c >= 0x80: may be the lead byte of a valid multi-byte UTF-8
		// sequence, or a standalone (invalid-as-UTF-8) byte — which is
		// exactly how an 8-bit C1 control code (0x80-0x9F) shows up when it
		// was written as a single raw byte rather than its ESC-prefixed
		// 7-bit equivalent.
		_, size := utf8.DecodeRuneInString(s[i:])
		if size > 1 {
			// A genuine multi-byte UTF-8 sequence (e.g. a combining mark) —
			// pass it through whole, untouched.
			out = append(out, s[i:i+size]...)
			i += size
			continue
		}

		// size == 1 and c >= 0x80: not a valid multi-byte lead byte, so this
		// byte stands alone. In the C1 range it is a raw control code and is
		// dropped; outside that range it is some other stray invalid byte,
		// which this package has no mandate to touch and passes through.
		if c <= 0x9f {
			i++
			continue
		}
		out = append(out, c)
		i++
	}
	return string(out)
}

// skipEscape consumes one escape-introduced sequence starting at s[i], where
// s[i] == 0x1b (ESC), and returns the index of the first byte after it. It
// recognizes CSI ("ESC ["), OSC ("ESC ]", including OSC-8 hyperlinks), and
// DCS/APC ("ESC P" / "ESC _") sequences, each scanned only up to len(s) — an
// unterminated sequence is consumed to the end of the string rather than
// scanned past it. A lone ESC with nothing following it, or an ESC followed
// by a byte that starts none of the recognized classes, is consumed as a
// short (1- or 2-byte) sequence so the caller never re-examines a consumed
// byte.
func skipEscape(s string, i int) int {
	n := len(s)
	if i+1 >= n {
		return i + 1 // lone ESC: nothing follows.
	}

	switch s[i+1] {
	case '[': // CSI: ESC [ params... final-byte, final byte in 0x40-0x7E.
		j := i + 2
		for j < n {
			c := s[j]
			if c >= 0x40 && c <= 0x7e {
				return j + 1
			}
			j++
		}
		return n // truncated: no final byte before end of string.

	case ']': // OSC (e.g. OSC-8 hyperlink): terminated by BEL or ST (ESC \).
		j := i + 2
		for j < n {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < n && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return n // no terminator found before end of string.

	case 'P', '_': // DCS / APC: terminated by ST (ESC \).
		j := i + 2
		for j < n {
			if s[j] == 0x1b && j+1 < n && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return n // no terminator found before end of string.

	default:
		// An escape sequence this package does not specifically recognize;
		// treat it as a short 2-byte sequence so the caller advances past
		// it rather than re-processing the same ESC.
		return i + 2
	}
}
