package marker

import (
	"strings"
	"testing"
)

// A reader viewing a PR must be able to tell an agent-posted comment apart from
// a human's — pg-pr posts under the user's own GitHub account, so there is no
// bot badge. The invisible HTML marker alone (used for machine detection) shows
// the reader nothing; Stamp must also add a human-visible attribution.
func TestStampAddsVisibleBotAttribution(t *testing.T) {
	got := Stamp("Please rename this variable.")

	if !strings.Contains(got, HTMLMarker) {
		t.Fatalf("invisible HTML marker missing from stamped body:\n%s", got)
	}
	// Strip the invisible marker; a human-visible attribution must remain.
	visible := strings.ReplaceAll(got, HTMLMarker, "")
	if !strings.Contains(visible, Glyph) {
		t.Errorf("visible attribution missing the bot glyph %q; stamped body:\n%s", Glyph, got)
	}
	if !strings.Contains(strings.ToLower(visible), "pg-pr") {
		t.Errorf("visible attribution should name the agent (pg-pr); stamped body:\n%s", got)
	}
	if !strings.Contains(got, "Please rename this variable.") {
		t.Errorf("original body content lost; stamped body:\n%s", got)
	}
}

// Strip recovers the underlying human-meaningful content from a stamped body so
// callers can dedup/fingerprint on the content itself, not on attribution
// boilerplate that changes as the marker policy evolves.
func TestStripRoundTripsStamp(t *testing.T) {
	for _, body := range []string{
		"Please rename this variable.",
		"Multi\nline\n\nbody with blank lines.",
		"a body that itself mentions <!-- something --> but not ours",
	} {
		if got := Strip(Stamp(body)); got != body {
			t.Errorf("Strip(Stamp(%q)) = %q, want %q", body, got, body)
		}
	}
}

func TestStripRemovesLegacyGlyphMarkup(t *testing.T) {
	if got := Strip("old reply\n\n" + Glyph); got != "old reply" {
		t.Errorf("Strip(legacy) = %q, want %q", got, "old reply")
	}
}

func TestStripLeavesUnmarkedBodyContent(t *testing.T) {
	if got := Strip("a plain human comment"); got != "a plain human comment" {
		t.Errorf("Strip(unmarked) = %q, want unchanged", got)
	}
}

func TestIsOursMatchesNewAndLegacyMarkers(t *testing.T) {
	if !IsOurs(Stamp("hello")) {
		t.Fatal("new HTML marker not recognized")
	}
	// Legacy glyph-marked bodies still recognized during transition.
	if !IsOurs("an old pg-pr reply\n\n" + Glyph) {
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
