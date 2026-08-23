package sectionrender

import (
	"errors"
	"strings"
	"testing"
)

// TestRender_SinglePopulatedSection pins the header/indented-detail shape
// (matching cmd/pg-pr/sync_duplicates.go's renderDuplicateReports) with a
// short golden string literal.
func TestRender_SinglePopulatedSection(t *testing.T) {
	sections := []Section{
		{
			Heading: "workspace demo",
			Fields: []Field{
				{Label: "scanned", Value: "12 open PRs"},
			},
			Details: []string{
				"ok no duplicated bead identities",
			},
		},
	}

	var buf strings.Builder
	if err := Render(&buf, sections...); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "workspace demo\n" +
		"  scanned: 12 open PRs\n" +
		"  ok no duplicated bead identities\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestRender_UnknownMarkerField proves the exported Unknown marker renders
// verbatim, and is distinguishable from an empty value.
func TestRender_UnknownMarkerField(t *testing.T) {
	section := Section{
		Heading: "pr 42",
		Fields: []Field{
			{Label: "reviewer", Value: Unknown},
		},
	}

	var buf strings.Builder
	if err := Render(&buf, section); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "pr 42\n  reviewer: -\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Distinguishable from an empty value: an empty Value renders
	// differently from the Unknown marker.
	var emptyBuf strings.Builder
	emptySection := Section{Heading: "pr 42", Fields: []Field{{Label: "reviewer", Value: ""}}}
	if err := Render(&emptyBuf, emptySection); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}
	if buf.String() == emptyBuf.String() {
		t.Errorf("Unknown marker output must differ from an empty value's output, both rendered as %q", buf.String())
	}

	// OrUnknown is the helper form of the same marker.
	if got := OrUnknown(""); got != Unknown {
		t.Errorf("OrUnknown(\"\") = %q, want %q", got, Unknown)
	}
	if got := OrUnknown("assigned"); got != "assigned" {
		t.Errorf("OrUnknown(%q) = %q, want the original value unchanged", "assigned", got)
	}
}

// TestRender_ZeroFieldSection proves a Section with no Fields and no
// Details still renders its Heading line rather than disappearing. A
// sibling caller's "no section silently dropped" acceptance criterion
// depends on this structurally.
func TestRender_ZeroFieldSection(t *testing.T) {
	section := Section{Heading: "empty section"}

	var buf strings.Builder
	if err := Render(&buf, section); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	const want = "empty section\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestRender_MultipleSections proves multiple sections render in call
// order, each with its own heading, separated by the exact pinned
// Separator (a single blank line) between (not before the first, not
// after the last) section.
func TestRender_MultipleSections(t *testing.T) {
	sections := []Section{
		{Heading: "first", Fields: []Field{{Label: "a", Value: "1"}}},
		{Heading: "second", Fields: []Field{{Label: "b", Value: "2"}}},
	}

	var buf strings.Builder
	if err := Render(&buf, sections...); err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}

	want := "first\n  a: 1\n" + Separator + "second\n  b: 2\n"
	if got := buf.String(); got != want {
		t.Errorf("Render output mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Pin the exact separator text itself: a single blank line, i.e. one
	// literal "\n" written between the two sections' rendered blocks.
	if Separator != "\n" {
		t.Errorf("Separator = %q, want %q", Separator, "\n")
	}
}

// errWriter is a fake io.Writer that always fails, for
// TestRender_WriteErrorPropagates.
type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

// TestRender_WriteErrorPropagates proves Render returns a Write error to
// the caller rather than panicking or silently swallowing it. This is a
// deliberate departure from cmd/pg-pr/sync_duplicates.go's
// renderDuplicateReports, which swallows write errors as "non-fatal — the
// caller may have closed the writer"; that judgment call is left to each
// caller here, not made unilaterally by this package.
func TestRender_WriteErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: writer closed")
	w := errWriter{err: wantErr}

	err := Render(w, Section{Heading: "anything", Fields: []Field{{Label: "x", Value: "y"}}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Render error = %v, want %v", err, wantErr)
	}
}
