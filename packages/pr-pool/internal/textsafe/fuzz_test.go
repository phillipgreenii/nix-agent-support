package textsafe

import "testing"

// FuzzSanitize is seeded with the case-table inputs (Task 3.7 Step 3) and
// asserts Sanitize never panics on arbitrary byte input and never leaves a
// raw C0/C1/CSI/OSC/DCS sequence in its output. Fuzzing itself is NOT part
// of the CI gate — run manually:
//
//	go test ./internal/textsafe/... -fuzz FuzzSanitize -fuzztime 30s
func FuzzSanitize(f *testing.F) {
	seeds := []string{
		"",
		"a\x07b",             // bare C0 (BEL)
		"a\x9bb\x9dc",        // 8-bit C1 standalone
		"\x1b[31mRED\x1b[0m", // CSI with parameters
		"\x1b]8;;http://example.com\x1b\\click\x1b]8;;\x1b\\", // OSC-8 with terminator
		"text\x1b]8;;http://example.com",                      // OSC-8 without terminator
		"a\x1bPsome-dcs-data\x1b\\b",                          // DCS
		"a\x1b_some-apc-data\x1b\\b",                          // APC
		"text\x1b[",                                           // truncated trailing escape
		"text\x1b",                                            // lone ESC, nothing following
		"a\x1b]0;window-title\x07b",                           // OSC terminated by BEL
		"text\x1bPunterminated-dcs",                           // truncated DCS
		"a\x1bZb",                                             // unrecognized escape
		"a\x7fb",                                              // DEL
		"a" + string([]byte{0xff}) + "b",                      // stray invalid byte, non-C1
		combiningMarkText,                                     // negative: combining marks
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := Sanitize(s) // must not panic on any input, including invalid UTF-8.
		if hasRawControlOrEscape(got) {
			t.Fatalf("Sanitize(%q) = %q still contains a raw C0/C1/CSI/OSC/DCS byte", s, got)
		}
	})
}
