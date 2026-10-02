package display

import (
	"regexp"
	"strings"
)

// Escape sequences are removed whole, so "ESC[31mred" shows as "red" and not
// as "[31mred".
var (
	reCSI  = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	reOSC  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	reEsc2 = regexp.MustCompile(`\x1b[@-Z\\-_]`)
)

// Sanitize makes text a handler reported safe to print: whole ANSI escape
// sequences are removed, and every remaining C0 control character (including
// ESC), DEL and C1 control character is stripped, except newline and tab. A
// carriage return is stripped too, so text cannot overwrite what came before
// it on the line. Invalid UTF-8 becomes U+FFFD.
func Sanitize(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = reOSC.ReplaceAllString(s, "")
	s = reCSI.ReplaceAllString(s, "")
	s = reEsc2.ReplaceAllString(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n', r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsDelimiter reports whether a line reads as one of the tool's delimiter
// lines: "== ... ==" or "-- ... --" starting in column 0.
func IsDelimiter(line string) bool {
	line = strings.TrimRight(line, " \t")
	for _, d := range []string{"==", "--"} {
		if strings.HasPrefix(line, d+" ") && strings.HasSuffix(line, " "+d) && len(line) >= 2*len(d)+2 {
			return true
		}
	}
	return false
}

// handlerText prepares text a handler (or verify) produced for printing: it is
// sanitized, a line that would read as a delimiter line is defanged with a
// leading space (so a handler cannot forge a section boundary), and the result
// ends in exactly one newline. Empty text yields "".
func handlerText(s string) string {
	s = Sanitize(s)
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, " \t")
		if IsDelimiter(l) {
			l = " " + l
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n") + "\n"
}

// firstLine is the first line of a handler's summary, sanitized. Only this
// line is ever shown.
func firstLine(s string) string {
	s = Sanitize(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return handlerText(s)
}

// inline makes text fit on a delimiter line: control characters are removed
// and newlines become spaces, so it can neither end the line nor forge another.
func inline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = Sanitize(s)
	return strings.ReplaceAll(s, "\t", " ")
}
