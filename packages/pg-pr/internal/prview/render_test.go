package prview

import (
	"bytes"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sectionrender"
)

// This file pins the human-readable `pr view` render (pg2-4dz88.5.6),
// mirroring prview_json_test.go's structure for the --json contract but
// over sections()/RenderHuman instead of MarshalView. fullView and
// emptyView are the SAME package-level fixtures prview_json_test.go
// defines (built via Assemble, not hand-built View literals) — reused here
// rather than redefined, so the human and JSON renders are always tested
// against identical underlying data.
//
// Golden fixtures live in testdata/pr-view-{full,empty}.txt, colocated next
// to this test file per pkg/provider/vcs/github/testdata/'s convention
// (hand-maintained, read via os.ReadFile, no regeneration harness).

func mustRenderHuman(t *testing.T, v View) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderHuman(&buf, v); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	return buf.Bytes()
}

func readHumanGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return raw
}

// ---------------------------------------------------------------------------
// 1. Full-fixture golden compare: byte-for-byte over the FULL buffer (a
//    substring assertion could not catch a silently dropped section).
// ---------------------------------------------------------------------------

func TestRenderHuman_Full_MatchesGolden(t *testing.T) {
	got := mustRenderHuman(t, fullView)
	want := readHumanGolden(t, "pr-view-full.txt")
	if !bytes.Equal(got, want) {
		t.Fatalf("RenderHuman(fullView) does not match golden fixture byte-for-byte.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// ---------------------------------------------------------------------------
// 2. Empty-fixture golden compare, plus the mechanical checks the bead's
//    acceptance criteria call for: distinct unknown/none markers, and
//    identical section-heading sets (same set, same order) vs. the full
//    fixture.
// ---------------------------------------------------------------------------

func TestRenderHuman_Empty_MatchesGolden(t *testing.T) {
	got := mustRenderHuman(t, emptyView)
	want := readHumanGolden(t, "pr-view-empty.txt")
	if !bytes.Equal(got, want) {
		t.Fatalf("RenderHuman(emptyView) does not match golden fixture byte-for-byte.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRenderHuman_UnknownAndNoneMarkersAreDistinct pins that this render's
// two "absent" markers are different text: sectionrender.Unknown ("-", "not
// known / no data source at all") vs noneMarker ("(none)", "asked, got
// zero"). Confirmed mechanically here (string inequality), not just by
// eyeballing the golden files.
func TestRenderHuman_UnknownAndNoneMarkersAreDistinct(t *testing.T) {
	if noneMarker == sectionrender.Unknown {
		t.Fatalf("noneMarker %q must not equal sectionrender.Unknown %q — they mark different things", noneMarker, sectionrender.Unknown)
	}
}

// TestRenderHuman_Empty_UsesDistinctMarkersPerAxis checks, against the
// built Section values directly (not the rendered text), that:
//   - collections emptyView sets to non-nil-but-empty (Feedback, Revisions,
//     LinkedTicketKeys, BeadLinks — "asked the store, got zero") render the
//     noneMarker, not the unknown one;
//   - axes emptyView leaves nil (Ownership, Enrichment — "no store row at
//     all") render sectionrender.Unknown;
//   - the three not-yet-existing axes (Approvals, PolicyBot, HideWIP) also
//     render sectionrender.Unknown, per this bead's carried-forward ruling.
func TestRenderHuman_Empty_UsesDistinctMarkersPerAxis(t *testing.T) {
	byHeading := sectionsByHeading(t, sections(emptyView))

	for _, heading := range []string{"Feedback", "Revisions", "Linked Tickets", "Bead Links"} {
		s := byHeading[heading]
		if len(s.Details) != 1 || s.Details[0] != noneMarker {
			t.Errorf("section %q Details = %v, want a single %q marker (emptyView's collections are non-nil-empty, not nil)", heading, s.Details, noneMarker)
		}
	}

	own := byHeading["Ownership"]
	if len(own.Fields) != 1 || own.Fields[0].Value != sectionrender.Unknown {
		t.Errorf("Ownership section = %+v, want a single field carrying sectionrender.Unknown (no store row)", own)
	}

	enrich := byHeading["Enrichment"]
	for _, f := range enrich.Fields {
		if f.Value != sectionrender.Unknown {
			t.Errorf("Enrichment field %q = %q, want %q (no store row)", f.Label, f.Value, sectionrender.Unknown)
		}
	}

	for _, heading := range []string{"Approvals", "Policy Bot", "Hide/WIP State"} {
		s := byHeading[heading]
		if len(s.Fields) != 1 || s.Fields[0].Value != sectionrender.Unknown {
			t.Errorf("section %q = %+v, want a single field carrying sectionrender.Unknown", heading, s)
		}
	}
}

// TestRenderHuman_SectionHeadingsMatchBetweenFullAndEmpty is the assertion
// with real teeth per this bead's testing plan: the SET AND ORDER of
// section headings sections() builds for the full fixture must be IDENTICAL
// to the empty fixture's, so a future regression that silently drops a
// section when its data is missing is caught mechanically. Compared
// programmatically over the Section values built for each case, not by
// eyeballing the golden files.
func TestRenderHuman_SectionHeadingsMatchBetweenFullAndEmpty(t *testing.T) {
	fullHeadings := headingsOf(sections(fullView))
	emptyHeadings := headingsOf(sections(emptyView))

	if len(fullHeadings) != len(emptyHeadings) {
		t.Fatalf("full fixture has %d sections %v, empty fixture has %d sections %v — same set/order required, none may be dropped",
			len(fullHeadings), fullHeadings, len(emptyHeadings), emptyHeadings)
	}
	for i := range fullHeadings {
		if fullHeadings[i] != emptyHeadings[i] {
			t.Errorf("section[%d]: full=%q empty=%q — heading sets must match, same order", i, fullHeadings[i], emptyHeadings[i])
		}
	}
}

func headingsOf(secs []sectionrender.Section) []string {
	out := make([]string, len(secs))
	for i, s := range secs {
		out[i] = s.Heading
	}
	return out
}

func sectionsByHeading(t *testing.T, secs []sectionrender.Section) map[string]sectionrender.Section {
	t.Helper()
	out := make(map[string]sectionrender.Section, len(secs))
	for _, s := range secs {
		out[s.Heading] = s
	}
	return out
}
