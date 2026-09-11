package textsafe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// hasRawControlOrEscape reports whether s still contains a raw ESC byte, a
// standalone C0 control byte (or DEL), or a standalone 8-bit C1 control
// byte. Because every CSI/OSC/DCS/APC sequence is ESC-prefixed, "no raw ESC
// byte remains" already subsumes all four of those classes; this is the
// same structural check the fuzz test (fuzz_test.go) uses. It walks s the
// same way Sanitize does — decoding multi-byte UTF-8 runes whole — so a C1-
// range byte that is a continuation byte of a legitimate multi-byte
// sequence (e.g. a combining mark) is correctly NOT flagged.
func hasRawControlOrEscape(s string) bool {
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		if c < 0x80 {
			if c == 0x1b || c < 0x20 || c == 0x7f {
				return true
			}
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size > 1 {
			i += size
			continue
		}
		if c <= 0x9f {
			return true // standalone raw C1 control byte.
		}
		i++
	}
	return false
}

// combiningMarkText is "cafe" + COMBINING ACUTE ACCENT (U+0301, encodes as
// 0xCC 0x81) + " na" + "i" + COMBINING DIAERESIS (U+0308, encodes as 0xCC
// 0x88) + "ve". Each combining mark's continuation byte (0x81, 0x88) falls
// inside the same 0x80-0x9F range a standalone 8-bit C1 control byte would
// occupy — exactly the trap the negative case in TestSanitizeCaseTable
// exists to catch: Sanitize must recognize these as part of a valid
// multi-byte sequence, not as raw C1 bytes to strip.
const combiningMarkText = "café naïve"

// TestSanitizeCaseTable is the red-first test (Task 3.7 Step 1): before this
// package existed, this failed to compile at all. It covers all 8 classes
// from the Binding decisions' case table, plus the negative case proving
// ordinary UTF-8 text (with a combining mark) passes through untouched.
func TestSanitizeCaseTable(t *testing.T) {
	cases := []struct {
		name  string
		input string
		// want is the exact expected output for this class under this
		// package's chosen strip strategy. The design's acceptance bar is
		// behavioral (never panic, never leave a raw control byte in the
		// output), not a fixed output per input — this exact value
		// additionally pins down THIS implementation's specific behavior
		// for regression purposes.
		want string
	}{
		{
			name:  "bare C0 (BEL)",
			input: "a\x07b",
			want:  "ab",
		},
		{
			name:  "8-bit C1 (0x9b, 0x9d) standalone",
			input: "a\x9bb\x9dc",
			want:  "abc",
		},
		{
			name:  "CSI with parameters",
			input: "\x1b[31mRED\x1b[0m",
			want:  "RED",
		},
		{
			name:  "OSC-8 hyperlink with terminator",
			input: "\x1b]8;;http://example.com\x1b\\click\x1b]8;;\x1b\\",
			want:  "click",
		},
		{
			name:  "OSC-8 hyperlink without a terminator",
			input: "text\x1b]8;;http://example.com",
			want:  "text",
		},
		{
			name:  "DCS sequence",
			input: "a\x1bPsome-dcs-data\x1b\\b",
			want:  "ab",
		},
		{
			name:  "APC sequence",
			input: "a\x1b_some-apc-data\x1b\\b",
			want:  "ab",
		},
		{
			name:  "truncated trailing escape (CSI cut off)",
			input: "text\x1b[",
			want:  "text",
		},
		{
			name:  "lone ESC with no following byte at all",
			input: "text\x1b",
			want:  "text",
		},
		{
			name:  "negative: UTF-8 text with combining marks passes through untouched",
			input: combiningMarkText,
			want:  combiningMarkText,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.input)
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if hasRawControlOrEscape(got) {
				t.Fatalf("Sanitize(%q) = %q still contains a raw C0/C1/CSI/OSC/DCS byte", tc.input, got)
			}
		})
	}
}

// --- additional coverage: branches the 8-class table above does not reach ---

// OSC terminated by BEL rather than ST — a real xterm-accepted OSC
// terminator this package also recognizes (skipEscape's ']' case, BEL arm).
func TestSanitize_oscTerminatedByBEL(t *testing.T) {
	got := Sanitize("a\x1b]0;window-title\x07b")
	if got != "ab" {
		t.Fatalf("Sanitize(...) = %q, want %q", got, "ab")
	}
}

// A truncated DCS/APC sequence (no ST terminator before end of string) must
// be dropped up to the end of the string, not panic or hang.
func TestSanitize_truncatedDCS(t *testing.T) {
	got := Sanitize("text\x1bPunterminated-dcs")
	if got != "text" {
		t.Fatalf("Sanitize(...) = %q, want %q", got, "text")
	}
}

// An escape sequence this package does not specifically recognize (ESC
// followed by some other byte) is consumed as a short 2-byte sequence
// (skipEscape's default arm) rather than left in the output or mis-parsed.
func TestSanitize_unrecognizedEscape(t *testing.T) {
	got := Sanitize("a\x1bZb")
	if got != "ab" {
		t.Fatalf("Sanitize(...) = %q, want %q", got, "ab")
	}
	if hasRawControlOrEscape(got) {
		t.Fatalf("Sanitize(...) = %q still contains a raw ESC", got)
	}
}

// DEL (0x7F) is a C0-adjacent control byte this package also strips, even
// though it is not one of the 8 named classes.
func TestSanitize_del(t *testing.T) {
	got := Sanitize("a\x7fb")
	if got != "ab" {
		t.Fatalf("Sanitize(...) = %q, want %q", got, "ab")
	}
}

// A stray invalid byte outside the C1 range (0x80-0x9F) — not one of this
// package's four mandated classes — passes through unchanged.
func TestSanitize_strayInvalidByteOutsideC1RangePassesThrough(t *testing.T) {
	input := "a" + string([]byte{0xff}) + "b"
	got := Sanitize(input)
	if got != input {
		t.Fatalf("Sanitize(%q) = %q, want unchanged %q", input, got, input)
	}
}

// Empty string in, empty string out — no index-out-of-range on the trivial
// input.
func TestSanitize_empty(t *testing.T) {
	if got := Sanitize(""); got != "" {
		t.Fatalf("Sanitize(\"\") = %q, want empty", got)
	}
}

// A long run of ordinary printable ASCII text passes through byte-for-byte,
// confirming the fast path does not mutate ordinary content.
func TestSanitize_plainTextPassesThroughUnchanged(t *testing.T) {
	input := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 8)
	if got := Sanitize(input); got != input {
		t.Fatalf("Sanitize(plain text) changed the input")
	}
}
