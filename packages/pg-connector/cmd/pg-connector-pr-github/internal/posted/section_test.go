package posted

import "testing"

const sectionHead = "0123456789abcdef0123456789abcdef01234567"

func TestFindSection(t *testing.T) {
	sec := "<!-- pg-section head=0123456789ab -->\nfindings\n<!-- /pg-section -->"
	cases := []struct {
		name, body, head string
		want             string
		ok               bool
	}{
		{"full head finds the section", "intro\n" + sec + "\ntail", sectionHead, sec, true},
		{"short head finds the same section", sec, "0123456789ab", sec, true},
		{"upper-case head", sec, "0123456789AB0000", sec, true},
		{"other head", sec, "ffffffffffff0000", "", false},
		{"no section", "just a summary", sectionHead, "", false},
		{"open without close", "<!-- pg-section head=0123456789ab -->\nfindings", sectionHead, "", false},
		{"head too short", sec, "0123456", "", false},
		{"empty head", sec, "", "", false},
		{
			"two heads, second one asked", sec + "\n<!-- pg-section head=ffffffffffff -->\nx\n<!-- /pg-section -->", "ffffffffffff1111",
			"<!-- pg-section head=ffffffffffff -->\nx\n<!-- /pg-section -->", true,
		},
		{"CRLF body", "a\r\n" + sec + "\r\nb", sectionHead, sec, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, ok := FindSection(c.body, c.head)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && c.body[start:end] != c.want {
				t.Fatalf("section = %q, want %q", c.body[start:end], c.want)
			}
		})
	}
}

func TestSectionOpen(t *testing.T) {
	if got := SectionOpen(sectionHead); got != "<!-- pg-section head=0123456789ab -->" {
		t.Errorf("SectionOpen = %q", got)
	}
	if got := SectionOpen("abc"); got != "" {
		t.Errorf("SectionOpen(short) = %q, want empty", got)
	}
}
