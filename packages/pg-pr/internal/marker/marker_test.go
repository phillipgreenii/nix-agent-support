package marker

import (
	"strings"
	"testing"
)

func TestMarkerify_AppendsWhenAbsent(t *testing.T) {
	got := Markerify("hello world")
	if !strings.Contains(got, Glyph) {
		t.Fatalf("expected glyph: %q", got)
	}
	if !strings.HasPrefix(got, "hello world") {
		t.Fatalf("body should be preserved: %q", got)
	}
}

func TestMarkerify_IdempotentWhenPresent(t *testing.T) {
	in := "hello\n\n" + Glyph
	if got := Markerify(in); got != in {
		t.Fatalf("idempotent failed: %q vs %q", got, in)
	}
}

func TestMarkerify_EmptyBody(t *testing.T) {
	if got := Markerify(""); got != Glyph {
		t.Fatalf("empty body: %q", got)
	}
}
