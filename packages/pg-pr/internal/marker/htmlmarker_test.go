package marker

import "testing"

func TestIsOursMatchesNewAndLegacyMarkers(t *testing.T) {
	if !IsOurs(Stamp("hello")) {
		t.Fatal("new HTML marker not recognized")
	}
	// Legacy glyph-marked bodies still recognized during transition.
	if !IsOurs(Markerify("an old pg-pr reply")) {
		t.Fatal("legacy glyph marker not recognized")
	}
	if IsOurs("a human comment with no marker") {
		t.Fatal("false positive on unmarked body")
	}
	if Stamp("x") == "x" {
		t.Fatal("Stamp did not add the marker")
	}
	// Stamp is idempotent.
	if Stamp(Stamp("y")) != Stamp("y") {
		t.Fatal("Stamp double-stamped")
	}
}
