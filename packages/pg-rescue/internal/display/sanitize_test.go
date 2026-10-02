package display

import "testing"

func TestSanitize(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"plain", "hello world", "hello world"},
		{"keeps newline and tab", "a\n\tb", "a\n\tb"},
		{"CSI sequence removed whole", "\x1b[31mred\x1b[0m", "red"},
		{"CSI with parameters and intermediates", "\x1b[1;2;3 qx", "x"},
		{"OSC terminated by BEL", "\x1b]0;title\x07after", "after"},
		{"OSC terminated by ST", "\x1b]8;;http://x\x1b\\link", "link"},
		{"two-byte escape", "a\x1bMb", "ab"},
		{"lone ESC", "a\x1b", "a"},
		{"C0 controls", "a\x00b\x01c\x07d\x08e\x0bf\x0cg\x0eh\x1fi", "abcdefghi"},
		{"carriage return", "one\rtwo", "onetwo"},
		{"DEL", "a\x7fb", "ab"},
		{"C1 controls", "a\u0085b\u009bc\u009fd", "abcd"},
		{"unicode kept", "héllo ✓ 日本", "héllo ✓ 日本"},
		{"invalid UTF-8", "a\xffb", "a�b"},
	} {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("%s: Sanitize(%q) = %q; want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestIsDelimiter(t *testing.T) {
	for line, want := range map[string]bool{
		"== resolved by x ==":     true,
		"-- verify: passed --":    true,
		"== x ==  ":               true,
		"==  ==":                  true,
		"== x":                    false,
		"x ==":                    false,
		" == x ==":                false,
		"=== x ===":               false,
		"== x --":                 false,
		"-- x ==":                 false,
		"==":                      false,
		"== ==":                   false,
		"plain text":              false,
		"":                        false,
		"-- a -- b --":            true,
		"--- not a delimiter ---": false,
	} {
		if got := IsDelimiter(line); got != want {
			t.Errorf("IsDelimiter(%q) = %v; want %v", line, got, want)
		}
	}
}

func TestHandlerText(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"blank", " \n\t\n", ""},
		{"adds the final newline", "a", "a\n"},
		{"collapses trailing newlines", "a\n\n\n", "a\n"},
		{"trims trailing spaces", "a  \nb\t\n", "a\nb\n"},
		{"defangs a delimiter line", "ok\n== done ==\n", "ok\n == done ==\n"},
		{"defangs a sub-section line", "-- details --", " -- details --\n"},
		{"keeps interior blank lines", "a\n\nb", "a\n\nb\n"},
		{"sanitizes", "\x1b[1mx\x1b[0m", "x\n"},
	} {
		if got := handlerText(c.in); got != c.want {
			t.Errorf("%s: handlerText(%q) = %q; want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "",
		"one":                   "one\n",
		"one\ntwo":              "one\n",
		"\nsecond":              "",
		"x\r\ny":                "x\n",
		"\x1b[31mone\x1b[0m\nz": "one\n",
		"== forged ==\nmore":    " == forged ==\n",
	} {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestInlineKeepsTextOnOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb":      "a b",
		"a\tb":      "a b",
		"a\x1b[0mb": "ab",
		"a\r\nb":    "a b",
	} {
		if got := inline(in); got != want {
			t.Errorf("inline(%q) = %q; want %q", in, got, want)
		}
	}
}
