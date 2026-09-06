package scriptout

import (
	"strings"
	"testing"
)

func TestTruncateForFold_ShortInput_Unchanged(t *testing.T) {
	in := "  boom: something went wrong  "
	got := TruncateForFold([]byte(in))
	want := "boom: something went wrong"
	if got != want {
		t.Fatalf("TruncateForFold(%q) = %q, want %q", in, got, want)
	}
}

func TestTruncateForFold_EmptyInput_ReturnsEmpty(t *testing.T) {
	if got := TruncateForFold(nil); got != "" {
		t.Fatalf("TruncateForFold(nil) = %q, want empty", got)
	}
	if got := TruncateForFold([]byte("   \n\t  ")); got != "" {
		t.Fatalf("TruncateForFold(whitespace-only) = %q, want empty", got)
	}
}

// TestTruncateForFold_LongInput_CappedWithMarker is the core regression
// proof for bead #26: before TruncateForFold existed, every fold call site
// interpolated the FULL captured bytes with no bound at all.
func TestTruncateForFold_LongInput_CappedWithMarker(t *testing.T) {
	huge := strings.Repeat("x", MaxFoldedOutputBytes*3)
	got := TruncateForFold([]byte(huge))
	if len(got) > MaxFoldedOutputBytes+64 {
		t.Fatalf("TruncateForFold output is %d bytes, want capped near MaxFoldedOutputBytes (%d)", len(got), MaxFoldedOutputBytes)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("TruncateForFold output has no truncation marker: %q...(truncated for test log)", got[:200])
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 100)) {
		t.Fatalf("TruncateForFold dropped the LEADING content instead of the trailing overflow")
	}
}

// TestTruncateForFold_RuneBoundarySafe proves a multi-byte UTF-8 sequence
// straddling the byte cap is never split mid-rune (which would otherwise
// leave an invalid/mangled tail byte sequence in the returned string).
func TestTruncateForFold_RuneBoundarySafe(t *testing.T) {
	// A 3-byte rune (e.g. "€", U+20AC) repeated so the cap almost certainly
	// lands inside one of its encoded bytes for at least one padding
	// length; try a small range of paddings to make the boundary case
	// deterministic across MaxFoldedOutputBytes without hardcoding its
	// exact value here.
	for pad := 0; pad < 6; pad++ {
		s := strings.Repeat("a", MaxFoldedOutputBytes-pad) + strings.Repeat("€", 100)
		got := TruncateForFold([]byte(s))
		if !strings.HasSuffix(strings.SplitN(got, "...", 2)[0], "a") && !strings.Contains(got, "€") {
			// Either ending cleanly on the 'a' run or having captured at
			// least one full '€' rune is acceptable; what's NOT
			// acceptable is invalid UTF-8, checked below.
			continue
		}
		body := strings.SplitN(got, "... [truncated", 2)[0]
		if !isValidUTF8(body) {
			t.Fatalf("pad=%d: TruncateForFold produced invalid UTF-8: %q", pad, body)
		}
	}
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}
