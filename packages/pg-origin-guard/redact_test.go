package originguard

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactTailMasksLoginURLAndPrincipal(t *testing.T) {
	in := "Open https://idp.example/auth?state=abc123&code_challenge=Zq9&code=7788 now\nprincipal: alice@example.com\n"
	got := RedactTail(in)
	for _, leak := range []string{"abc123", "Zq9", "7788", "alice@example.com"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in %q", leak, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("nothing redacted: %q", got)
	}
}

func TestRedactTailKeepsAtMost300Bytes(t *testing.T) {
	got := RedactTail(strings.Repeat("line of text\n", 100))
	if len(got) > MaxErrorLen || !strings.HasSuffix(got, "line of text\n") {
		t.Errorf("len=%d", len(got))
	}
	if short := "short error"; RedactTail(short) != short {
		t.Error("short text must be kept whole")
	}
}

func TestRedactTailCutsOnUTF8Boundary(t *testing.T) {
	// "é" is two bytes. 151 of them is 302 bytes, so a byte cut lands inside one.
	got := RedactTail(strings.Repeat("é", 151))
	if !utf8.ValidString(got) || len(got) > MaxErrorLen {
		t.Errorf("valid=%v len=%d", utf8.ValidString(got), len(got))
	}
}

func TestRedactTailRedactsBeforeCutting(t *testing.T) {
	// A 64-hex credential that straddles the 300-byte cut. Cut first and the
	// surviving 30-character tail would be too short for any pattern.
	secret := strings.Repeat("ab12cd34", 8)
	text := strings.Repeat("x ", 10) + secret + " " + strings.Repeat("y ", 132) + "y"
	cutAt := len(text) - MaxErrorLen
	if idx := strings.Index(text, secret); idx >= cutAt || cutAt >= idx+len(secret) {
		t.Fatalf("test setup: secret [%d,%d) must straddle the cut at %d", idx, idx+len(secret), cutAt)
	}
	got := RedactTail(text)
	for n := 8; n <= len(secret); n++ {
		if strings.Contains(got, secret[len(secret)-n:]) {
			t.Fatalf("credential fragment survived: %q", got)
		}
	}
	if len(got) > MaxErrorLen {
		t.Errorf("len = %d", len(got))
	}
}
